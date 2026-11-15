package config_test

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/unstring/internal/config"
	"github.com/peasant-labs/infra/unstring/internal/failure"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

//go:embed testdata/load.yaml
var loadYAML []byte

type loadCase struct {
	Name   string            `yaml:"name"`
	Files  map[string]string `yaml:"files"`
	Config string            `yaml:"config"`
	Want   *struct {
		Baseline     string   `yaml:"baseline"`
		TSRoots      []string `yaml:"tsRoots"`
		GoRoots      []string `yaml:"goRoots"`
		Prefixes     []string `yaml:"prefixes"`
		Callees      []string `yaml:"callees"`
		MountFiles   []string `yaml:"mountFiles"`
		DataDirs     []string `yaml:"dataDirs"`
		SchemaModule string   `yaml:"schemaModule"`
	} `yaml:"want"`
	Exempted []struct {
		Rule string `yaml:"rule"`
		File string `yaml:"file"`
		Want bool   `yaml:"want"`
	} `yaml:"exempted"`
	Error []string `yaml:"error"`
}

type loadFixture struct {
	Cases []loadCase `yaml:"cases"`
	Globs []struct {
		Pattern string `yaml:"pattern"`
		File    string `yaml:"file"`
		Match   bool   `yaml:"match"`
	} `yaml:"globs"`
}

// The cases every change to config loading must keep.
var requiredLoadCases = []string{
	"defaults-applied",
	"explicit-settings-kept",
	"exempt-generated-files-for-closed-set",
	"exempt-hand-written-file-for-other-rule",
	"refuses-unknown-key",
	"refuses-other-version",
	"refuses-empty-file",
	"refuses-no-language",
	"refuses-missing-root",
	"refuses-absolute-root",
	"refuses-root-outside-config-directory",
	"refuses-unknown-exempt-rule",
	"refuses-exempt-without-reason",
	"refuses-exempt-without-paths",
	"refuses-closed-set-exempt-on-hand-written-file",
	"refuses-all-rules-exempt-on-hand-written-file",
}

func loadLoadFixture(t *testing.T) loadFixture {
	t.Helper()
	var f loadFixture
	dec := yaml.NewDecoder(bytes.NewReader(loadYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/load.yaml: %v", err)
	}
	return f
}

func TestRequiredLoadCasesExist(t *testing.T) {
	var names []string
	for _, c := range loadLoadFixture(t).Cases {
		if slices.Contains(names, c.Name) {
			t.Errorf("testdata/load.yaml has two cases named %q", c.Name)
		}
		names = append(names, c.Name)
	}
	for _, want := range requiredLoadCases {
		if !slices.Contains(names, want) {
			t.Errorf("testdata/load.yaml is missing required case %q", want)
		}
	}
}

func TestLoad(t *testing.T) {
	for _, c := range loadLoadFixture(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range c.Files {
				p := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, config.DefaultPath)
			if err := os.WriteFile(path, []byte(c.Config), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := config.Load(path)

			if len(c.Error) > 0 {
				if err == nil {
					t.Fatalf("Load accepted the config, want an error containing %q", c.Error)
				}
				var ferr *failure.Error
				if !errors.As(err, &ferr) {
					t.Fatalf("Load returned %T, want *failure.Error: %v", err, err)
				}
				for _, part := range []string{ferr.What, ferr.Why, ferr.Where, ferr.Fix} {
					if strings.TrimSpace(part) == "" {
						t.Errorf("the failure has an empty part: %+v", ferr)
					}
				}
				for _, want := range c.Error {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("the failure does not contain %q:\n%s", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if w := c.Want; w != nil {
				check := func(name string, got, want []string) {
					if want != nil && !slices.Equal(got, want) {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
				if w.Baseline != "" && got.Baseline != w.Baseline {
					t.Errorf("Baseline = %q, want %q", got.Baseline, w.Baseline)
				}
				check("ts roots", got.RootsFor(rules.LangTS), w.TSRoots)
				check("go roots", got.RootsFor(rules.LangGo), w.GoRoots)
				p := got.Params()
				check("endpoint prefixes", p.EndpointPrefixes, w.Prefixes)
				check("endpoint callees", p.EndpointCallees, w.Callees)
				check("mount files", p.MountFiles, w.MountFiles)
				check("data dirs", p.DataDirs, w.DataDirs)
				if w.SchemaModule != "" && got.Schema.Module != w.SchemaModule {
					t.Errorf("schema module = %q, want %q", got.Schema.Module, w.SchemaModule)
				}
			}
			for _, e := range c.Exempted {
				if g := got.Exempted(rules.RuleID(e.Rule), e.File); g != e.Want {
					t.Errorf("Exempted(%s, %s) = %v, want %v", e.Rule, e.File, g, e.Want)
				}
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), config.DefaultPath))
	if err == nil || !strings.Contains(err.Error(), "--config PATH") {
		t.Fatalf("Load of a missing file = %v, want a failure naming --config", err)
	}
}

func TestMatchPath(t *testing.T) {
	for _, g := range loadLoadFixture(t).Globs {
		if got := config.MatchPath(g.Pattern, g.File); got != g.Match {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", g.Pattern, g.File, got, g.Match)
		}
	}
}

// TestSectionKeyIsLangWireValue pins that each language's config section is
// keyed by the language's wire value, so `--lang X` and the `X:` section
// name one language.
func TestSectionKeyIsLangWireValue(t *testing.T) {
	for _, l := range rules.AllLangs {
		t.Run(l.String(), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, config.DefaultPath)
			body := "version: 1\n" + l.String() + ":\n  roots: [src]\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := config.Load(path)
			if err != nil {
				t.Fatalf("load a config with only the %q section: %v", l, err)
			}
			for _, other := range rules.AllLangs {
				got := c.RootsFor(other)
				if other == l && !slices.Equal(got, []string{"src"}) {
					t.Errorf("RootsFor(%s) = %v, want [src]", other, got)
				}
				if other != l && got != nil {
					t.Errorf("RootsFor(%s) = %v, want none", other, got)
				}
			}
		})
	}
}
