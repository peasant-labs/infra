package ratchet_test

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/unstring/internal/failure"
	"github.com/peasant-labs/infra/unstring/internal/ratchet"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

//go:embed testdata/ratchet.yaml
var ratchetYAML []byte

type versions struct {
	Tool    string `yaml:"tool"`
	AstGrep string `yaml:"astGrep"`
}

type entries map[string]map[string]int

type ratchetCase struct {
	Name         string            `yaml:"name"`
	Command      string            `yaml:"command"`
	Lang         string            `yaml:"lang"`
	Running      *versions         `yaml:"running"`
	Header       *versions         `yaml:"header"`
	Baseline     entries           `yaml:"baseline"`
	BaselineText string            `yaml:"baselineText"`
	RawHits      []hitRow          `yaml:"hits"`
	Comments     []commentRow      `yaml:"comments"`
	Files        map[string]string `yaml:"files"`
	Renames      map[string]string `yaml:"renames"`
	Untracked    []string          `yaml:"untracked"`

	WantError        []string  `yaml:"wantError"`
	WantExit         int       `yaml:"wantExit"`
	WantKinds        []string  `yaml:"wantKinds"`
	WantText         []string  `yaml:"wantText"`
	WantNoText       []string  `yaml:"wantNoText"`
	WantWritten      *bool     `yaml:"wantWritten"`
	WantEntries      entries   `yaml:"wantEntries"`
	WantHeader       *versions `yaml:"wantHeader"`
	WantBaselineText string    `yaml:"wantBaselineText"`
	Repeat           bool      `yaml:"repeat"`
}

type hitRow struct {
	Rule string `yaml:"rule"`
	File string `yaml:"file"`
	Line int    `yaml:"line"`
	Text string `yaml:"text"`
}

type commentRow struct {
	File    string `yaml:"file"`
	Line    int    `yaml:"line"`
	EndLine int    `yaml:"endLine"`
	Text    string `yaml:"text"`
}

// The cases every change to the checker must keep: one per finding kind of
// the message contract, and the update, init, header and language rules.
var requiredRatchetCases = []string{
	"check-clean-tree",
	"check-new-file",
	"check-raised-lists-every-line",
	"check-stale-lower",
	"check-stale-missing-file",
	"check-dedupes-two-matches-on-one-line",
	"marker-trailing-removes-hit",
	"marker-line-above-removes-hit",
	"marker-stacked-above-one-line",
	"marker-jsx-comment",
	"marker-no-reason",
	"marker-with-double-hyphen-is-no-reason",
	"marker-reason-under-three-words",
	"marker-unused",
	"marker-unknown-rule",
	"marker-not-accepted-closed-set",
	"marker-not-accepted-go-closed-set",
	"native-ignore-on-guard-rule",
	"native-ignore-bare",
	"native-ignore-on-strict-rule-is-not-a-finding",
	"baseline-header-check-still-compares",
	"unknown-baseline-format-version",
	"update-lowers-count",
	"update-removes-entry",
	"update-removes-missing-file-entry",
	"update-refuses-raise",
	"update-refuses-new-file",
	"update-moves-entry-on-rename",
	"update-moves-entry-on-rename-with-lower-count",
	"update-refuses-rename-with-higher-count",
	"update-refuses-unstaged-rename-with-staging-fix",
	"update-rewrites-header-only",
	"update-rewrites-header-and-lowers",
	"update-refuses-raise-leaves-header",
	"update-sorts-rules-and-paths",
	"update-lang-go-keeps-ts-entries",
	"check-lang-go-ignores-ts-entries",
	"init-writes-full-baseline",
}

func loadRatchetCases(t *testing.T) []ratchetCase {
	t.Helper()
	var f struct {
		Cases []ratchetCase `yaml:"cases"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(ratchetYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/ratchet.yaml: %v", err)
	}
	return f.Cases
}

func TestRequiredRatchetCasesExist(t *testing.T) {
	var names []string
	for _, c := range loadRatchetCases(t) {
		if slices.Contains(names, c.Name) {
			t.Errorf("testdata/ratchet.yaml has two cases named %q", c.Name)
		}
		names = append(names, c.Name)
	}
	for _, want := range requiredRatchetCases {
		if !slices.Contains(names, want) {
			t.Errorf("testdata/ratchet.yaml is missing required case %q", want)
		}
	}
}

// Every finding kind of the message contract is produced by at least one
// case, so none can lose its path through the checker unnoticed. The
// environment kind is the command's (exit 2); its cases are wantError cases.
func TestEveryFindingKindIsExercised(t *testing.T) {
	seen := map[string]bool{}
	hasError := false
	for _, c := range loadRatchetCases(t) {
		for _, k := range c.WantKinds {
			seen[k] = true
		}
		hasError = hasError || len(c.WantError) > 0
	}
	for _, k := range ratchet.AllFindingKinds {
		if k == ratchet.KindEnvironment {
			if !hasError {
				t.Errorf("no case expects a failure that stops the run")
			}
			continue
		}
		if !seen[k.String()] {
			t.Errorf("no case in testdata/ratchet.yaml expects the finding kind %q", k)
		}
	}
}

func (c ratchetCase) running() ratchet.Versions {
	if c.Running != nil {
		return ratchet.Versions{Tool: c.Running.Tool, AstGrep: c.Running.AstGrep}
	}
	return ratchet.Versions{Tool: "v0.0.1", AstGrep: "0.51.2"}
}

func (c ratchetCase) baselineBytes() []byte {
	if c.BaselineText != "" {
		return []byte(c.BaselineText)
	}
	h := c.running()
	if c.Header != nil {
		h = ratchet.Versions{Tool: c.Header.Tool, AstGrep: c.Header.AstGrep}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "version: 1\ntool: %s\nast-grep: %s\nrules:\n", h.Tool, h.AstGrep)
	for rule, files := range c.Baseline {
		fmt.Fprintf(&sb, "  %s:\n", rule)
		for f, n := range files {
			fmt.Fprintf(&sb, "    %q: %d\n", f, n)
		}
	}
	return []byte(sb.String())
}

func (c ratchetCase) scan(t *testing.T) ratchet.Scan {
	t.Helper()
	var s ratchet.Scan
	for _, h := range c.RawHits {
		s.Hits = append(s.Hits, rules.Hit{Rule: rules.RuleID(h.Rule), File: h.File, Line: h.Line, Text: h.Text})
	}
	lines := func(file string) ([]string, error) {
		body, ok := c.Files[file]
		if !ok {
			return nil, os.ErrNotExist
		}
		return strings.Split(body, "\n"), nil
	}
	for _, cm := range c.Comments {
		src, err := lines(cm.File)
		if err != nil || cm.Line > len(src) {
			t.Fatalf("comment in %s:%d has no source line in `files`", cm.File, cm.Line)
		}
		line := src[cm.Line-1]
		first := strings.SplitN(cm.Text, "\n", 2)[0]
		idx := strings.Index(line, first)
		if idx < 0 {
			t.Fatalf("comment %q is not on %s:%d (%q)", first, cm.File, cm.Line, line)
		}
		end := cm.EndLine
		if end == 0 {
			end = cm.Line
		}
		s.Comments = append(s.Comments, ratchet.Comment{
			File: cm.File, Line: cm.Line, EndLine: end, Column: len([]rune(line[:idx])) + 1,
			Text: cm.Text, LineText: line,
		})
	}
	s.Lines = lines
	return s
}

func (c ratchetCase) options() ratchet.Options {
	langs := slices.Clone(rules.AllLangs)
	if c.Lang != "" {
		langs = []rules.Lang{rules.Lang(c.Lang)}
	}
	return ratchet.Options{
		Command:      ratchet.Command(c.Command),
		BaselinePath: ".unstring-baseline.yaml",
		Langs:        langs,
		LangFlag:     c.Lang != "",
		Running:      c.running(),
	}
}

func (c ratchetCase) run(t *testing.T, baseline []byte) (ratchet.Outcome, error) {
	t.Helper()
	o := c.options()
	switch o.Command {
	case ratchet.CommandInit:
		return ratchet.Init(c.scan(t), o)
	}
	base, err := ratchet.ParseBaseline(o.BaselinePath, baseline)
	if err != nil {
		return ratchet.Outcome{}, err
	}
	if o.Command == ratchet.CommandCheck {
		return ratchet.Check(c.scan(t), base, o)
	}
	git := ratchet.GitState{Renames: c.Renames, Untracked: map[string]bool{}}
	for _, u := range c.Untracked {
		git.Untracked[u] = true
	}
	return ratchet.Update(c.scan(t), base, git, o)
}

func TestRatchet(t *testing.T) {
	for _, c := range loadRatchetCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			out, err := c.run(t, c.baselineBytes())
			if len(c.WantError) > 0 {
				var ferr *failure.Error
				if !errors.As(err, &ferr) {
					t.Fatalf("got %v, want a *failure.Error containing %q", err, c.WantError)
				}
				for _, want := range c.WantError {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("the failure does not contain %q:\n%v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Exit != c.WantExit {
				t.Errorf("exit %d, want %d", out.Exit, c.WantExit)
			}
			if c.WantKinds != nil {
				var kinds []string
				for _, f := range out.Findings {
					kinds = append(kinds, f.Kind.String())
				}
				if !slices.Equal(kinds, c.WantKinds) && !(len(kinds) == 0 && len(c.WantKinds) == 0) {
					t.Errorf("finding kinds %q, want %q", kinds, c.WantKinds)
				}
			}
			for _, f := range out.Findings {
				for name, part := range map[string]string{"what": f.What, "why": f.Why, "where": f.Where, "fix": f.Fix} {
					if strings.TrimSpace(part) == "" {
						t.Errorf("a %s finding has an empty %s part: %+v", f.Kind, name, f)
					}
				}
			}
			var report bytes.Buffer
			ratchet.WriteReport(&report, ratchet.FormatText, c.options().Command, out.Findings)
			for _, want := range c.WantText {
				if !strings.Contains(report.String(), want) {
					t.Errorf("the report does not contain %q:\n%s", want, report.String())
				}
			}
			for _, bad := range c.WantNoText {
				if strings.Contains(report.String(), bad) {
					t.Errorf("the report contains %q:\n%s", bad, report.String())
				}
			}

			writes := c.Command != "check" && c.WantExit == 0
			if c.WantWritten != nil {
				writes = *c.WantWritten
			}
			if writes != (out.Baseline != nil) {
				t.Fatalf("wrote a baseline: %v, want %v", out.Baseline != nil, writes)
			}
			if out.Baseline == nil {
				return
			}
			if c.WantBaselineText != "" && string(out.Baseline) != c.WantBaselineText {
				t.Errorf("baseline text:\n%s\nwant:\n%s", out.Baseline, c.WantBaselineText)
			}
			written, err := ratchet.ParseBaseline("written", out.Baseline)
			if err != nil {
				t.Fatalf("the written baseline does not parse back: %v\n%s", err, out.Baseline)
			}
			if c.WantEntries != nil {
				got := entries{}
				for id, files := range written.Entries {
					got[id.String()] = files
				}
				if !reflect.DeepEqual(got, c.WantEntries) {
					t.Errorf("entries %v, want %v", got, c.WantEntries)
				}
			}
			if c.WantHeader != nil {
				want := ratchet.Versions{Tool: c.WantHeader.Tool, AstGrep: c.WantHeader.AstGrep}
				if written.Header != want {
					t.Errorf("header %+v, want %+v", written.Header, want)
				}
			}
			if c.Repeat {
				again, err := c.run(t, out.Baseline)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(again.Baseline, out.Baseline) {
					t.Errorf("a second run wrote different bytes:\n%s\nfirst:\n%s", again.Baseline, out.Baseline)
				}
			}
		})
	}
}
