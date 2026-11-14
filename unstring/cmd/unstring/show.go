package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
	"github.com/peasant-labs/infra/unstring/internal/catalog"
	"github.com/peasant-labs/infra/unstring/internal/config"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// ShowFormat is the closed set of formats `unstring show` prints.
type ShowFormat string

const (
	// ShowText is the human listing.
	ShowText ShowFormat = "text"
	// ShowJSON is one JSON document.
	ShowJSON ShowFormat = "json"
)

// AllShowFormats lists every ShowFormat.
var AllShowFormats = []ShowFormat{ShowText, ShowJSON}

// String returns the format as typed after --format.
func (f ShowFormat) String() string { return string(f) }

// IsValid reports whether f is a member of AllShowFormats.
func (f ShowFormat) IsValid() bool {
	switch f {
	case ShowText, ShowJSON:
		return true
	}
	return false
}

// ParseShowFormat converts a --format value into a ShowFormat.
func ParseShowFormat(s string) (ShowFormat, error) {
	if f := ShowFormat(s); f.IsValid() {
		return f, nil
	}
	return "", fmt.Errorf("unknown show format %q: show prints exactly %s", s, joinNames(AllShowFormats, " or "))
}

func (p process) runShow(args []string) int {
	topicFix := "run one of: " + joinNames(AllShowTopics, ", ") + ", for example `unstring show version`."
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return environmentFailure(p.stderr,
			"`unstring show` needs a topic.",
			"show prints exactly one of these topics: "+joinNames(AllShowTopics, ", ")+".",
			"command-line parsing, before any file was read.",
			topicFix)
	}
	topic, err := ParseShowTopic(args[0])
	if err != nil {
		return environmentFailure(p.stderr,
			err.Error()+".",
			"show prints exactly one of these topics: "+joinNames(AllShowTopics, ", ")+".",
			"command-line parsing, before any file was read.",
			topicFix)
	}
	f, ok := p.parseFlags(CommandShow.String()+" "+topic.String(), args[1:], func(fs *flag.FlagSet, f *flags) {
		configFlag(fs, f)
		astGrepFlag(fs, f)
		fs.StringVar(&f.format, "format", ShowText.String(), "output format: "+joinNames(AllShowFormats, " or "))
	})
	if !ok {
		return exitEnvironment
	}
	format, err := ParseShowFormat(f.format)
	if err != nil {
		return environmentFailure(p.stderr, err.Error()+".",
			"show prints a text listing, or one JSON document with --format "+ShowJSON.String()+".",
			"command-line parsing, before any file was read.",
			"pass --format "+joinNames(AllShowFormats, " or --format ")+".")
	}

	switch topic {
	case TopicVersion:
		return p.showVersion(format)
	case TopicRules:
		return p.showRules(f, format)
	case TopicCatalog:
		return p.showCatalog(f, format)
	}
	return environmentFailure(p.stderr,
		fmt.Sprintf("topic %q has no handler.", topic),
		"the topic is in the closed set but this build does not dispatch it.",
		"`unstring show`, before any file was read.",
		"report this to the infra maintainers with the command line you ran.")
}

func (p process) writeJSON(v any) int {
	enc := json.NewEncoder(p.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return environmentFailure(p.stderr, "the output could not be written: "+err.Error()+".",
			"show writes one JSON document to standard output.",
			"`unstring show`, writing to standard output.",
			"check that standard output is writable.")
	}
	return exitClean
}

func (p process) showVersion(format ShowFormat) int {
	if format == ShowJSON {
		return p.writeJSON(struct {
			Tool    string `json:"tool"`
			AstGrep string `json:"astGrep"`
		}{p.version.String(), astgrep.RequiredVersion})
	}
	if _, err := fmt.Fprintf(p.stdout, "unstring %s (requires ast-grep %s)\n", p.version, astgrep.RequiredVersion); err != nil {
		return environmentFailure(p.stderr,
			"the version could not be written.",
			err.Error(),
			"`unstring show version`, writing to standard output.",
			"check that standard output is writable.")
	}
	return exitClean
}

type shownRule struct {
	ID            string `json:"id"`
	Lang          string `json:"lang"`
	Origin        string `json:"origin"`
	AcceptsMarker bool   `json:"acceptsMarker"`
	What          string `json:"what"`
	Why           string `json:"why"`
	Fix           string `json:"fix"`
}

// showRules prints every rule id with its language, message and fix. The
// static rules are built from the config when one exists, else from the
// defaults; the generated rules' text comes from the generator.
func (p process) showRules(f flags, format ShowFormat) int {
	params := config.Config{Endpoints: config.Endpoints{Prefixes: config.DefaultEndpointPrefixes()}, DataDirs: config.DefaultDataDirs()}.Params()
	if _, err := os.Stat(f.config); err == nil {
		cfg, err := config.Load(f.config)
		if err != nil {
			return failErr(p.stderr, err, "reading "+f.config+".")
		}
		params = cfg.Params()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return failErr(p.stderr, err, "reading "+f.config+".")
	}
	reg, err := p.deps.Registry(params)
	if err != nil {
		return failErr(p.stderr, asFailure(err,
			"the static rules could not be built: "+err.Error()+".",
			"show rules prints the rules this repository's settings build.",
			"`unstring show rules`, building the rules. No file was scanned.",
			"check the `endpoints` and `dataDirs` settings of the config."), "")
	}
	var out []shownRule
	for _, id := range rules.AllRuleIDs {
		var msg rules.Message
		if r, ok := reg.Rule(id); ok {
			msg = r.Message
		} else if id.Origin() == rules.OriginGenerated {
			if r, err := p.deps.Generator.Generate(catalog.Catalog{Lang: id.Lang()}); err == nil && r.ID == id {
				msg = r.Message
			}
		}
		if msg.What == "" {
			msg.What = "(this build carries no message for the rule)"
		}
		out = append(out, shownRule{
			ID: id.String(), Lang: id.Lang().String(), Origin: id.Origin().String(), AcceptsMarker: id.AcceptsMarker(),
			What: msg.What, Why: msg.Why, Fix: msg.Fix,
		})
	}
	if format == ShowJSON {
		return p.writeJSON(out)
	}
	for _, r := range out {
		marker := "accepts an unstring-allow marker"
		if !r.AcceptsMarker {
			marker = "accepts no marker"
		}
		fmt.Fprintf(p.stdout, "%s  %s  %s  %s\n", r.ID, r.Lang, r.Origin, marker)
		fmt.Fprintf(p.stdout, "  what   %s\n  why    %s\n  fix    %s\n\n", r.What, r.Why, r.Fix)
	}
	return exitClean
}

type shownMember struct {
	Lang   string `json:"lang"`
	Value  string `json:"value"`
	Set    string `json:"set,omitempty"`
	Source string `json:"source"`
	Module string `json:"module,omitempty"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// showCatalog prints the generated catalog of every configured language.
func (p process) showCatalog(f flags, format ShowFormat) int {
	s, ok := p.open(f)
	if !ok {
		return exitEnvironment
	}
	var out []shownMember
	for _, l := range s.languages("") {
		cat, err := s.buildCatalog(l)
		if err != nil {
			return failErr(p.stderr, err, "catalog build.")
		}
		for _, m := range cat.Members {
			out = append(out, shownMember{
				Lang: l.String(), Value: m.Value, Set: m.Set, Source: m.Source.String(),
				Module: m.Site.Module, File: m.Site.File, Line: m.Site.Line,
			})
		}
	}
	slices.SortStableFunc(out, func(a, b shownMember) int {
		return strings.Compare(a.Lang+"\x00"+a.Value+"\x00"+a.Module+"\x00"+a.File, b.Lang+"\x00"+b.Value+"\x00"+b.Module+"\x00"+b.File)
	})
	if format == ShowJSON {
		if out == nil {
			out = []shownMember{}
		}
		return p.writeJSON(out)
	}
	for _, m := range out {
		site := fmt.Sprintf("%s:%d", m.File, m.Line)
		if m.Module != "" {
			site = m.Module + " " + site
		}
		set := m.Set
		if set == "" {
			set = "-"
		}
		fmt.Fprintf(p.stdout, "%s  %q  %s  %s  %s\n", m.Lang, m.Value, set, m.Source, site)
	}
	return exitClean
}
