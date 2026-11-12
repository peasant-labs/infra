package catalog_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/stringly/internal/catalog"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// catalogFixture is one testdata/catalog/*.yaml file: trees in, exact members
// (or the exact failure) out.
type catalogFixture struct {
	Cases []catalogCase `yaml:"cases"`
}

type catalogCase struct {
	Name   string            `yaml:"name"`
	Lang   string            `yaml:"lang"`
	Roots  []string          `yaml:"roots"`
	Files  map[string]string `yaml:"files"`
	Schema *schemaFixture    `yaml:"schema"`
	// Members is the exact member list, in any order.
	Members []memberFixture `yaml:"members"`
	// Error, when set, is text the BuildError's what must contain; Members
	// must then be empty.
	Error string `yaml:"error"`
}

// schemaFixture names the schema module of a case. The module tree is either
// the committed fixture module (tree: schema-module) or inline files. The
// fixture resolver serves exactly module@version.
type schemaFixture struct {
	Module  string            `yaml:"module"`
	Version string            `yaml:"version"`
	Tree    string            `yaml:"tree"`
	Files   map[string]string `yaml:"files"`
}

type memberFixture struct {
	Value  string `yaml:"value"`
	Source string `yaml:"source"`
	Set    string `yaml:"set"`
	Name   string `yaml:"name"`
	Module string `yaml:"module"`
	File   string `yaml:"file"`
	Line   int    `yaml:"line"`
}

func (m memberFixture) String() string {
	return fmt.Sprintf("%q %s set=%q name=%q %s %s:%d", m.Value, m.Source, m.Set, m.Name, m.Module, m.File, m.Line)
}

func loadCatalogFixtures(t *testing.T) map[string]catalogFixture {
	t.Helper()
	paths, err := filepath.Glob("testdata/catalog/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no catalog fixtures under testdata/catalog: %v", err)
	}
	out := map[string]catalogFixture{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f catalogFixture
		decodeStrict(t, p, data, &f)
		out[strings.TrimSuffix(filepath.Base(p), ".yaml")] = f
	}
	return out
}

// buildFixtureCase writes the case's tree and schema module and builds its
// catalog through the production Builder.
func buildFixtureCase(t *testing.T, lang rules.Lang, roots []string, files map[string]string, schema *schemaFixture) (catalog.Catalog, string, error) {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, files)
	deps := catalog.BuilderDeps{Runner: newRunner(t)}
	req := catalog.BuildRequest{Dir: dir, Lang: lang, Roots: roots}
	if schema != nil {
		moduleDir := schemaFixtureDir
		if schema.Tree != "" {
			moduleDir = filepath.Join("testdata", schema.Tree)
		} else {
			moduleDir = t.TempDir()
			writeTree(t, moduleDir, schema.Files)
		}
		abs, err := filepath.Abs(moduleDir)
		if err != nil {
			t.Fatal(err)
		}
		deps.Modules = fixtureModules{module: schema.Module, version: schema.Version, dir: abs}
		req.SchemaModule = schema.Module
	}
	c, err := catalog.NewBuilder(deps).Build(context.Background(), req)
	return c, dir, err
}

func TestBuildCatalogFixtures(t *testing.T) {
	for file, f := range loadCatalogFixtures(t) {
		for _, tc := range f.Cases {
			t.Run(file+"/"+tc.Name, func(t *testing.T) {
				t.Parallel()
				lang, err := rules.ParseLang(tc.Lang)
				if err != nil {
					t.Fatal(err)
				}
				roots := tc.Roots
				if roots == nil {
					roots = []string{"."}
				}
				c, _, err := buildFixtureCase(t, lang, roots, tc.Files, tc.Schema)
				if tc.Error != "" {
					var be *catalog.BuildError
					if !errors.As(err, &be) {
						t.Fatalf("Build error = %v; want a *catalog.BuildError whose what contains %q", err, tc.Error)
					}
					if !strings.Contains(be.What, tc.Error) {
						t.Errorf("BuildError.What = %q; want it to contain %q", be.What, tc.Error)
					}
					for part, text := range map[string]string{"what": be.What, "why": be.Why, "where": be.Where, "fix": be.Fix} {
						if strings.TrimSpace(text) == "" {
							t.Errorf("BuildError has an empty %s part", part)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("Build: %v", err)
				}
				if c.Lang != lang {
					t.Errorf("catalog language = %q, want %q", c.Lang, lang)
				}
				got := make([]memberFixture, len(c.Members))
				for i, m := range c.Members {
					got[i] = memberFixture{
						Value: m.Value, Source: m.Source.String(), Set: m.Set, Name: m.Name,
						Module: m.Site.Module, File: m.Site.File, Line: m.Site.Line,
					}
				}
				want := slices.Clone(tc.Members)
				order := func(a, b memberFixture) int { return cmp.Compare(a.String(), b.String()) }
				slices.SortFunc(got, order)
				slices.SortFunc(want, order)
				for _, w := range want {
					if !slices.Contains(got, w) {
						t.Errorf("missing member %s", w)
					}
				}
				for _, g := range got {
					if !slices.Contains(want, g) {
						t.Errorf("unexpected member %s", g)
					}
				}
			})
		}
	}
}

// TestCatalogFixtureManifest fails when a case the manifest requires is
// missing from its fixture file, or a case name is used twice in one file.
func TestCatalogFixtureManifest(t *testing.T) {
	const path = "testdata/catalog-manifest.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Required map[string][]string `yaml:"required"`
	}
	decodeStrict(t, path, data, &manifest)
	fixtures := loadCatalogFixtures(t)
	for file, f := range fixtures {
		seen := map[string]bool{}
		for _, tc := range f.Cases {
			if seen[tc.Name] {
				t.Errorf("testdata/catalog/%s.yaml has two cases named %q", file, tc.Name)
			}
			seen[tc.Name] = true
		}
	}
	for file, names := range manifest.Required {
		f, ok := fixtures[file]
		if !ok {
			t.Errorf("%s requires testdata/catalog/%s.yaml, which does not exist", path, file)
			continue
		}
		for _, name := range names {
			if !slices.ContainsFunc(f.Cases, func(c catalogCase) bool { return c.Name == name }) {
				t.Errorf("%s requires case %q in testdata/catalog/%s.yaml", path, name, file)
			}
		}
	}
}

// The fixtures must cover every source kind: a kind with no fixture member
// could break unseen.
func TestCatalogFixturesCoverEverySourceKind(t *testing.T) {
	covered := map[string]bool{}
	for _, f := range loadCatalogFixtures(t) {
		for _, tc := range f.Cases {
			for _, m := range tc.Members {
				covered[m.Source] = true
			}
		}
	}
	for _, k := range catalog.AllSourceKinds {
		if !covered[k.String()] {
			t.Errorf("no catalog fixture expects a member of source kind %q", k)
		}
	}
}
