package catalog_test

import (
	"embed"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/stringly/internal/catalog"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

//go:embed testdata/explain.yaml testdata/explain-manifest.yaml
var explainFixtures embed.FS

type explainFixture struct {
	Cases []struct {
		Name    string            `yaml:"name"`
		Lang    string            `yaml:"lang"`
		Value   string            `yaml:"value"`
		Members []memberFixture   `yaml:"members"`
		Roots   []string          `yaml:"roots"`
		Files   map[string]string `yaml:"files"`
		Schema  *schemaFixture    `yaml:"schema"`
		Why     []string          `yaml:"why"`
		Fix     []string          `yaml:"fix"`
		FixNot  []string          `yaml:"fix_not"`
	} `yaml:"cases"`
}

func TestExplainFixtures(t *testing.T) {
	data, err := explainFixtures.ReadFile("testdata/explain.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f explainFixture
	decodeStrict(t, "testdata/explain.yaml", data, &f)
	data, err = explainFixtures.ReadFile("testdata/explain-manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Required []string `yaml:"required"`
	}
	decodeStrict(t, "testdata/explain-manifest.yaml", data, &manifest)
	names := make([]string, 0, len(f.Cases))
	for _, tc := range f.Cases {
		if slices.Contains(names, tc.Name) {
			t.Errorf("duplicate explain fixture %q", tc.Name)
		}
		names = append(names, tc.Name)
	}
	for _, required := range manifest.Required {
		if !slices.Contains(names, required) {
			t.Errorf("explain manifest requires case %q", required)
		}
	}
	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			lang, err := rules.ParseLang(tc.Lang)
			if err != nil {
				t.Fatal(err)
			}
			c := catalog.Catalog{Lang: lang}
			if tc.Schema != nil {
				roots := tc.Roots
				if roots == nil {
					roots = []string{"."}
				}
				c, _, err = buildFixtureCase(t, lang, roots, tc.Files, tc.Schema)
				if err != nil {
					t.Fatalf("Build: %v", err)
				}
			}
			for _, m := range tc.Members {
				source, err := catalog.ParseSourceKind(m.Source)
				if err != nil {
					t.Fatal(err)
				}
				c.Members = append(c.Members, catalog.Member{
					Value: m.Value, Set: m.Set, Name: m.Name, Source: source,
					Site: catalog.DefinitionSite{Module: m.Module, File: m.File, Line: m.Line},
				})
			}
			msg := c.Explain(tc.Value)
			if strings.TrimSpace(msg.What) == "" {
				t.Error("Explain returned an empty what")
			}
			for _, want := range tc.Why {
				if !strings.Contains(msg.Why, want) {
					t.Errorf("why = %q; want it to contain %q", msg.Why, want)
				}
			}
			for _, want := range tc.Fix {
				if !strings.Contains(msg.Fix, want) {
					t.Errorf("fix = %q; want it to contain %q", msg.Fix, want)
				}
			}
			for _, unwanted := range tc.FixNot {
				if strings.Contains(msg.Fix, unwanted) {
					t.Errorf("fix = %q; must not contain %q", msg.Fix, unwanted)
				}
			}
		})
	}
}
