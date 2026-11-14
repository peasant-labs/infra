package catalog

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
)

// The discovery rules find TS and JS definition sites. Their ids carry the
// "unstring-discover-" prefix, so they can never be mistaken for a tool rule.
//
//go:embed discovery/*.yml
var discoveryFS embed.FS

// discoveryRule is one embedded discovery rule and how its matches become
// members.
type discoveryRule struct {
	id     string
	file   string
	source SourceKind
	// named reports whether the match binds $SET (and maybe $NAME).
	named bool
}

var (
	discoverClosedCall  = discoveryRule{"unstring-discover-closed-call", "closed-call.yml", SourceClosedCall, true}
	discoverRecordTable = discoveryRule{"unstring-discover-record-table", "record-table.yml", SourceRecordTable, true}
	discoverUnionAlias  = discoveryRule{"unstring-discover-union-alias", "union-alias.yml", SourceTSUnion, true}
	discoverUnionInline = discoveryRule{"unstring-discover-union-inline", "union-inline.yml", SourceTSUnion, false}
	discoverJSDocUnion  = discoveryRule{"unstring-discover-jsdoc-union", "jsdoc-union.yml", SourceJSDocUnion, false}
)

// allTSDiscovery is every repository source of TS and JS members.
var allTSDiscovery = []discoveryRule{
	discoverClosedCall, discoverRecordTable, discoverUnionAlias, discoverUnionInline, discoverJSDocUnion,
}

// unionDiscovery is what the schema module's generated TS contributes.
var unionDiscovery = []discoveryRule{discoverUnionAlias, discoverUnionInline}

// harvestTS runs the given discovery rules over roots (relative to dir) and
// turns their matches into members, with sites relative to dir.
func harvestTS(ctx context.Context, runner astgrep.Runner, dir string, roots []string, discovery []discoveryRule) ([]Member, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	if runner == nil {
		return nil, &BuildError{
			What:  "no ast-grep runner is wired into the catalog builder.",
			Why:   "TS and JS definition sites are found by ast-grep discovery rules.",
			Where: "catalog build, TS discovery step.",
			Fix:   "this is a defect in the command's wiring; report it to the infra maintainers.",
		}
	}
	files := make([]astgrep.RuleFile, 0, len(discovery))
	byID := make(map[string]discoveryRule, len(discovery))
	for _, d := range discovery {
		body, err := discoveryFS.ReadFile("discovery/" + d.file)
		if err != nil {
			return nil, &BuildError{
				What:  "the embedded discovery rule " + d.file + " is missing.",
				Why:   "the tool was built without one of its own rule files.",
				Where: "catalog build, TS discovery step.",
				Fix:   "rebuild the tool from a clean checkout; report it to the infra maintainers if it persists.",
				Err:   err,
			}
		}
		files = append(files, astgrep.RuleFile{Name: d.file, Body: body})
		byID[d.id] = d
	}
	matches, err := runner.Scan(ctx, astgrep.ScanRequest{Dir: dir, Roots: roots, Rules: files})
	if err != nil {
		return nil, &BuildError{
			What:  "ast-grep failed while discovering closed sets.",
			Why:   err.Error(),
			Where: fmt.Sprintf("catalog build, TS discovery scan of %s in %s.", strings.Join(roots, ", "), dir),
			Fix:   "run the same scan by hand with ast-grep " + astgrep.RequiredVersion + " to see the failing file, then fix the file or the roots in .unstring.yaml.",
			Err:   err,
		}
	}
	var members []Member
	for _, m := range matches {
		d, ok := byID[m.RuleID]
		if !ok {
			continue
		}
		site := DefinitionSite{File: m.File, Line: m.Line}
		if d == discoverJSDocUnion {
			members = append(members, jsdocMembers(m.Text, site)...)
			continue
		}
		value, ok := keyOrLiteralValue(m.Text)
		if !ok || value == "" {
			continue
		}
		member := Member{Value: value, Source: d.source, Site: site}
		if d.named {
			member.Set = m.Vars["SET"]
			if name, ok := keyOrLiteralValue(m.Vars["NAME"]); ok {
				member.Name = name
			}
		}
		members = append(members, member)
	}
	return members, nil
}

// keyOrLiteralValue reads an object key or a string literal: a bare
// identifier is its own value.
func keyOrLiteralValue(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	switch text[0] {
	case '\'', '"', '`':
		return unquoteJS(text)
	}
	return text, true
}

func joinStringers[T fmt.Stringer](values []T) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = v.String()
	}
	return strings.Join(parts, ", ")
}
