package catalog_test

import (
	"os"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/stringly/internal/catalog"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

type explainFixture struct {
	Cases []struct {
		Name    string          `yaml:"name"`
		Lang    string          `yaml:"lang"`
		Value   string          `yaml:"value"`
		Members []memberFixture `yaml:"members"`
		Why     []string        `yaml:"why"`
		Fix     []string        `yaml:"fix"`
	} `yaml:"cases"`
}

func TestExplainFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/explain.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f explainFixture
	decodeStrict(t, "testdata/explain.yaml", data, &f)
	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			lang, err := rules.ParseLang(tc.Lang)
			if err != nil {
				t.Fatal(err)
			}
			c := catalog.Catalog{Lang: lang}
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
		})
	}
}
