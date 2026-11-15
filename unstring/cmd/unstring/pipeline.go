package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
	"github.com/peasant-labs/infra/unstring/internal/catalog"
	"github.com/peasant-labs/infra/unstring/internal/config"
	"github.com/peasant-labs/infra/unstring/internal/failure"
	"github.com/peasant-labs/infra/unstring/internal/ratchet"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// session is one command run against one repository config.
type session struct {
	ctx     context.Context
	cfg     config.Config
	cfgPath string
	// dir is the config's directory: every path read or printed is relative
	// to it.
	dir     string
	runner  *astgrep.Binary
	deps    deps
	environ []string
}

// scanned is what the rules found, ready for the checker.
type scanned struct {
	scan     ratchet.Scan
	catalogs map[rules.Lang]catalog.Catalog
}

// languages returns the configured languages, restricted to only when it is
// set.
func (s *session) languages(only rules.Lang) []rules.Lang {
	var out []rules.Lang
	for _, l := range rules.AllLangs {
		if only != "" && l != only {
			continue
		}
		if len(s.cfg.RootsFor(l)) > 0 {
			out = append(out, l)
		}
	}
	return out
}

// registry builds the static rules, or returns the failure that stops the
// run.
func (s *session) registry() (rules.Registry, error) {
	reg, err := s.deps.Registry(s.cfg.Params())
	if err != nil {
		return nil, asFailure(err,
			"the static rules could not be built: "+err.Error()+".",
			"the rules are built from the config's endpoint and data-directory settings before any scan.",
			"rule build, before any scan. No file was scanned.",
			"check the `endpoints` and `dataDirs` settings in "+s.cfgPath+"; if they are right, report this to the infra maintainers.")
	}
	return reg, nil
}

// buildCatalog harvests one language's catalog.
func (s *session) buildCatalog(l rules.Lang) (catalog.Catalog, error) {
	cat, err := s.deps.Catalog(s.runner).Build(s.ctx, catalog.BuildRequest{
		Dir:          s.dir,
		Lang:         l,
		Roots:        s.cfg.RootsFor(l),
		SchemaModule: s.cfg.Schema.Module,
		Env:          s.environ,
	})
	if err != nil {
		if ferr, ok := s.deps.Failure(err); ok {
			return catalog.Catalog{}, ferr
		}
		module := s.cfg.Schema.Module
		if module == "" {
			module = "<schema module>"
		}
		return catalog.Catalog{}, asFailure(err,
			fmt.Sprintf("the %s catalog could not be built: %v.", l, err),
			"the closed-set rule compares literals with every member the catalog harvests from the repository and the pinned schema module; without it the counts would be wrong.",
			fmt.Sprintf("catalog build for %s, before the scan. No file was scanned.", l),
			fmt.Sprintf("check the schema module (%s) and its pinned version in go.mod or package.json; "+
				"`go mod download -json %s@<version>` must succeed with your GOPROXY and GOFLAGS.", module, module))
	}
	return cat, nil
}

// runScan builds the rules of the selected languages, scans each language's
// roots with them and the marker comment rule, and returns the hits that
// remain after exempt and definition-site hits are dropped.
func (s *session) runScan(langs []rules.Lang) (scanned, error) {
	reg, err := s.registry()
	if err != nil {
		return scanned{}, err
	}
	out := scanned{
		catalogs: map[rules.Lang]catalog.Catalog{},
		scan:     ratchet.Scan{Lines: s.readLines},
	}
	static := map[rules.RuleID]rules.Rule{}
	for _, r := range reg.Rules() {
		static[r.ID] = r
	}

	for _, l := range langs {
		cat, err := s.buildCatalog(l)
		if err != nil {
			return scanned{}, err
		}
		out.catalogs[l] = cat
		gen, err := s.deps.Generator.Generate(cat)
		if err != nil {
			return scanned{}, asFailure(err,
				fmt.Sprintf("the closed-set rule for %s could not be generated: %v.", l, err),
				"the closed-set rule is generated from the catalog on every run.",
				fmt.Sprintf("rule generation for %s, before the scan. No file was scanned.", l),
				"run `unstring show catalog` to inspect the catalog, and report this to the infra maintainers with its output.")
		}

		var files []astgrep.RuleFile
		for _, r := range reg.Rules() {
			if r.Lang() == l {
				files = append(files, astgrep.RuleFile{Name: r.ID.String() + ".yml", Body: r.Document})
			}
		}
		if len(cat.Members) > 0 {
			files = append(files, astgrep.RuleFile{Name: gen.ID.String() + ".yml", Body: gen.Document})
		}
		files = append(files, ratchet.CommentRule(l))

		matches, err := s.runner.Scan(s.ctx, astgrep.ScanRequest{
			Dir: s.dir, Roots: s.cfg.RootsFor(l), Rules: files, LanguageGlobs: s.deps.LanguageGlobs,
		})
		if err != nil {
			why := err.Error()
			var serr *astgrep.ScanError
			if errors.As(err, &serr) {
				why = "ast-grep exited with " + serr.Err.Error() + ":\n" + serr.Stderr
			}
			return scanned{}, failure.New(
				fmt.Sprintf("the %s scan failed.", l),
				why,
				fmt.Sprintf("ast-grep scan of %s, from %s. No baseline was read or written.", strings.Join(s.cfg.RootsFor(l), ", "), s.dir),
				"fix the cause ast-grep names above; if it names a rule document, report it to the infra maintainers.")
		}
		for _, m := range matches {
			if ratchet.IsCommentRule(m.RuleID) {
				out.scan.Comments = append(out.scan.Comments, ratchet.CommentFromMatch(m))
				continue
			}
			id, err := s.deps.DocumentRule(m.RuleID)
			if err != nil || id.Lang() != l {
				// ast-grep's own records, such as unused-suppression.
				continue
			}
			h := rules.Hit{Rule: id, File: m.File, Line: m.Line, Text: m.Text}
			if s.cfg.Exempted(h.Rule, h.File) || (id.Origin() == rules.OriginGenerated && s.deps.DefinitionSite(cat, h)) {
				continue
			}
			out.scan.Hits = append(out.scan.Hits, h)
		}
	}
	out.scan.Messages = s.hitMessages(static, out.catalogs)
	return out, nil
}

func (s *session) readLines(file string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(file)))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines, nil
}

// literalValue returns the value of a string literal as source text: the
// text between its quotes.
func literalValue(text string) string {
	t := strings.TrimSpace(text)
	if v, err := strconv.Unquote(t); err == nil && (strings.HasPrefix(t, `"`) || strings.HasPrefix(t, "`")) {
		return v
	}
	if len(t) >= 2 {
		q := t[0]
		if (q == '"' || q == '\'' || q == '`') && t[len(t)-1] == q {
			return t[1 : len(t)-1]
		}
	}
	return t
}

// hitMessages returns the message of a hit: its static rule's message, or
// for a closed-set hit what the catalog says about its value.
func (s *session) hitMessages(static map[rules.RuleID]rules.Rule, cats map[rules.Lang]catalog.Catalog) func(rules.Hit) rules.Message {
	return func(h rules.Hit) rules.Message {
		if r, ok := static[h.Rule]; ok {
			return r.Message
		}
		if h.Rule.Origin() == rules.OriginGenerated {
			return s.deps.Explain(cats[h.Rule.Lang()], literalValue(h.Text))
		}
		return rules.Message{}
	}
}

// asFailure returns err itself when it is already a *failure.Error, and a
// new one with the given parts otherwise.
func asFailure(err error, what, why, where, fix string) *failure.Error {
	var ferr *failure.Error
	if errors.As(err, &ferr) {
		return ferr
	}
	return failure.New(what, why, where, fix)
}
