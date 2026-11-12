package astgrep_test

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
	"github.com/peasant-labs/infra/stringly/internal/astgrep/astgreptest"
	"github.com/peasant-labs/infra/stringly/internal/failure"
)

//go:embed testdata/runner.yaml
var runnerYAML []byte

type wantMatch struct {
	RuleID   string            `yaml:"ruleId"`
	File     string            `yaml:"file"`
	Line     int               `yaml:"line"`
	EndLine  int               `yaml:"endLine"`
	Column   int               `yaml:"column"`
	Text     string            `yaml:"text"`
	LineText string            `yaml:"lineText"`
	Vars     map[string]string `yaml:"vars"`
}

type runnerFixture struct {
	Versions []struct {
		Name    string   `yaml:"name"`
		Real    bool     `yaml:"real"`
		Missing bool     `yaml:"missing"`
		Output  string   `yaml:"output"`
		OK      bool     `yaml:"ok"`
		Error   []string `yaml:"error"`
	} `yaml:"versions"`
	Scans []struct {
		Name      string            `yaml:"name"`
		Files     map[string]string `yaml:"files"`
		Rules     map[string]string `yaml:"rules"`
		Roots     []string          `yaml:"roots"`
		Want      []wantMatch       `yaml:"want"`
		WantError []string          `yaml:"wantError"`
	} `yaml:"scans"`
}

var requiredRunnerCases = []string{
	"pinned-version-passes",
	"older-version-refused",
	"missing-binary-refused",
	"records-carry-line-column-and-line-text",
	"every-ts-and-js-extension-parsed-as-tsx",
	"only-the-roots-are-scanned",
	"native-suppressions-surface-without-error",
	"invalid-rule-fails-with-ast-grep-stderr",
}

func loadRunnerFixture(t *testing.T) runnerFixture {
	t.Helper()
	var f runnerFixture
	dec := yaml.NewDecoder(bytes.NewReader(runnerYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/runner.yaml: %v", err)
	}
	return f
}

func TestRequiredRunnerCasesExist(t *testing.T) {
	f := loadRunnerFixture(t)
	var names []string
	for _, v := range f.Versions {
		names = append(names, v.Name)
	}
	for _, s := range f.Scans {
		names = append(names, s.Name)
	}
	for _, want := range requiredRunnerCases {
		if !slices.Contains(names, want) {
			t.Errorf("testdata/runner.yaml is missing required case %q", want)
		}
	}
}

func TestCheckVersion(t *testing.T) {
	for _, c := range loadRunnerFixture(t).Versions {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			var b *astgrep.Binary
			switch {
			case c.Real:
				b = astgrep.NewBinary(astgreptest.Require(t))
			case c.Missing:
				b = astgrep.NewBinary(filepath.Join(dir, "ast-grep"))
			default:
				stub := filepath.Join(dir, "ast-grep")
				script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(c.Output, "'", "") + "'\n"
				if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				b = astgrep.NewBinary(stub)
			}
			err := astgrep.CheckVersion(context.Background(), b)
			if c.OK {
				if err != nil {
					t.Fatalf("CheckVersion: %v", err)
				}
				return
			}
			var ferr *failure.Error
			if !errors.As(err, &ferr) {
				t.Fatalf("CheckVersion = %v, want a *failure.Error", err)
			}
			var out bytes.Buffer
			failure.Render(&out, ferr)
			for _, want := range c.Error {
				if !strings.Contains(out.String(), want) {
					t.Errorf("the failure does not contain %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestScan(t *testing.T) {
	bin := astgreptest.Require(t)
	for _, c := range loadRunnerFixture(t).Scans {
		t.Run(c.Name, func(t *testing.T) {
			tree := t.TempDir()
			for name, body := range c.Files {
				p := filepath.Join(tree, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			tmp := t.TempDir()
			b := astgrep.NewBinary(bin)
			b.TempDir = tmp
			var rules []astgrep.RuleFile
			for _, name := range slices.Sorted(maps.Keys(c.Rules)) {
				rules = append(rules, astgrep.RuleFile{Name: name, Body: []byte(c.Rules[name])})
			}
			got, err := b.Scan(context.Background(), astgrep.ScanRequest{Dir: tree, Roots: c.Roots, Rules: rules})

			// The temporary rule directory is gone after every scan, failed or not.
			if left, _ := os.ReadDir(tmp); len(left) > 0 {
				t.Errorf("the scan left %d entries in its temporary directory", len(left))
			}
			if len(c.WantError) > 0 {
				if err == nil {
					t.Fatalf("Scan succeeded with %d records, want an error", len(got))
				}
				var serr *astgrep.ScanError
				if !errors.As(err, &serr) {
					t.Errorf("Scan error is %T, want *astgrep.ScanError", err)
				}
				for _, want := range c.WantError {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("the error does not contain %q:\n%v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(got) != len(c.Want) {
				t.Fatalf("Scan returned %d records, want %d:\n%+v", len(got), len(c.Want), got)
			}
			for _, w := range c.Want {
				if !slices.ContainsFunc(got, func(m astgrep.Match) bool { return matches(m, w) }) {
					t.Errorf("no record matches %+v; got:\n%+v", w, got)
				}
			}
		})
	}
}

// matches compares the fields the fixture sets.
func matches(m astgrep.Match, w wantMatch) bool {
	switch {
	case m.RuleID != w.RuleID, m.File != w.File, m.Line != w.Line:
		return false
	case w.EndLine != 0 && m.EndLine != w.EndLine:
		return false
	case w.Column != 0 && m.Column != w.Column:
		return false
	case w.Text != "" && m.Text != w.Text:
		return false
	case w.LineText != "" && m.LineText != w.LineText:
		return false
	}
	for k, v := range w.Vars {
		if m.Vars[k] != v {
			return false
		}
	}
	return true
}
