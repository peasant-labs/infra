package catalog_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
	"github.com/peasant-labs/infra/unstring/internal/catalog"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// ruleFixture is one testdata/rules/<rule-id>.yaml file for a generated rule:
// a tiny tree per case and the exact hit lines the rule reports in it.
type ruleFixture struct {
	Rule  string     `yaml:"rule"`
	Cases []ruleCase `yaml:"cases"`
}

type ruleCase struct {
	Name   string            `yaml:"name"`
	Roots  []string          `yaml:"roots"`
	Files  map[string]string `yaml:"files"`
	Schema *schemaFixture    `yaml:"schema"`
	// Hits maps each file to its exact hit lines; a file with no hit is
	// absent.
	Hits map[string][]int `yaml:"hits"`
}

// generatedRuleIDs are the rule ids built from the catalog.
func generatedRuleIDs() []rules.RuleID {
	var ids []rules.RuleID
	for _, id := range rules.AllRuleIDs {
		if id.Origin() == rules.OriginGenerated {
			ids = append(ids, id)
		}
	}
	return ids
}

func loadRuleFixtures(t *testing.T) map[rules.RuleID]ruleFixture {
	t.Helper()
	out := map[rules.RuleID]ruleFixture{}
	paths, err := filepath.Glob("testdata/rules/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if filepath.Base(p) == "manifest.yaml" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f ruleFixture
		decodeStrict(t, p, data, &f)
		id, err := rules.ParseRuleID(f.Rule)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if want := id.String() + ".yaml"; filepath.Base(p) != want {
			t.Fatalf("%s holds rule %s; name the file %s", p, id, want)
		}
		out[id] = f
	}
	return out
}

func loadRuleManifest(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile("testdata/rules/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string][]string
	decodeStrict(t, "testdata/rules/manifest.yaml", data, &m)
	return m
}

// scanGenerated runs the production path for one case: build the catalog,
// generate the rule, scan with ast-grep, drop definition-site hits, dedupe by
// line.
func scanGenerated(t *testing.T, id rules.RuleID, tc ruleCase) map[string][]int {
	t.Helper()
	roots := tc.Roots
	if roots == nil {
		roots = []string{"."}
	}
	c, dir, err := buildFixtureCase(t, id.Lang(), roots, tc.Files, tc.Schema)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rule, err := catalog.NewRuleGenerator().Generate(c)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if rule.ID != id {
		t.Fatalf("Generate returned rule %s for a %s catalog, want %s", rule.ID, c.Lang, id)
	}
	matches, err := newRunner(t).Scan(context.Background(), astgrep.ScanRequest{
		Dir: dir, Roots: roots, Rules: []astgrep.RuleFile{{Name: id.String() + ".yml", Body: rule.Document}},
	})
	if err != nil {
		t.Fatalf("scan with the generated rule: %v\n%s", err, rule.Document)
	}
	seen := map[rules.HitKey]bool{}
	got := map[string][]int{}
	for _, m := range matches {
		if m.RuleID != id.String() {
			continue
		}
		h := rules.Hit{Rule: id, File: m.File, Line: m.Line, Text: m.Text}
		if c.DefinitionSiteHit(h) || seen[h.Key()] {
			continue
		}
		seen[h.Key()] = true
		got[h.File] = append(got[h.File], h.Line)
	}
	for f := range got {
		sort.Ints(got[f])
	}
	return got
}

func TestGeneratedRuleFixtures(t *testing.T) {
	for id, f := range loadRuleFixtures(t) {
		for _, tc := range f.Cases {
			t.Run(id.String()+"/"+tc.Name, func(t *testing.T) {
				t.Parallel()
				got := scanGenerated(t, id, tc)
				want := map[string][]int{}
				for file, lines := range tc.Hits {
					want[file] = slices.Sorted(slices.Values(lines))
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("hits = %v, want %v", got, want)
				}
			})
		}
	}
}

// Every generated rule has a fixture file, and no fixture file names another
// rule: membership, not a count.
func TestGeneratedRuleFixturesCoverTheGeneratedRules(t *testing.T) {
	fixtures := loadRuleFixtures(t)
	for _, id := range generatedRuleIDs() {
		if _, ok := fixtures[id]; !ok {
			t.Errorf("generated rule %s has no testdata/rules/%s.yaml", id, id)
		}
	}
	for id := range fixtures {
		if id.Origin() != rules.OriginGenerated {
			t.Errorf("testdata/rules/%s.yaml is for a static rule; static rule fixtures live with the rules package", id)
		}
	}
}

// The manifest lists the required case names. For every excluded operand,
// read from the tool's own list, its three cases exist in the manifest and in
// the fixtures of its rule; a passes-excluded or flags-neighbour case that
// names no current exclusion is a leftover and fails.
func TestGeneratedRuleManifest(t *testing.T) {
	fixtures := loadRuleFixtures(t)
	manifest := loadRuleManifest(t)
	names := map[rules.RuleID]map[string]bool{}
	for id, f := range fixtures {
		names[id] = map[string]bool{}
		for _, tc := range f.Cases {
			if names[id][tc.Name] {
				t.Errorf("%s has two cases named %q", id, tc.Name)
			}
			names[id][tc.Name] = true
		}
	}
	for ruleName, required := range manifest {
		id, err := rules.ParseRuleID(ruleName)
		if err != nil {
			t.Errorf("manifest: %v", err)
			continue
		}
		for _, n := range required {
			if !names[id][n] {
				t.Errorf("manifest requires case %q for %s, but testdata/rules/%s.yaml has none", n, id, id)
			}
		}
	}
	for _, e := range rules.AllExcludedOperands {
		id := e.Rule()
		for _, n := range exclusionCaseNames(e) {
			if !slices.Contains(manifest[id.String()], n) {
				t.Errorf("manifest lacks %q for %s: every excluded operand needs its three cases", n, id)
			}
			if !names[id][n] {
				t.Errorf("testdata/rules/%s.yaml lacks case %q for excluded operand %s", id, n, e)
			}
		}
	}
	for id, ns := range names {
		for n := range ns {
			for _, prefix := range []string{"passes-excluded-", "flags-neighbour-of-"} {
				rest, ok := strings.CutPrefix(n, prefix)
				if !ok {
					continue
				}
				rest = strings.TrimSuffix(strings.TrimSuffix(rest, "-comparison"), "-switch")
				e, err := rules.ParseExcludedOperand(rest)
				if err != nil {
					t.Errorf("%s case %q names no current excluded operand: %v", id, n, err)
				} else if e.Rule() != id {
					t.Errorf("%s case %q is for %s, whose rule is %s", id, n, e, e.Rule())
				}
			}
		}
	}
}

func exclusionCaseNames(e rules.ExcludedOperand) []string {
	return []string{
		"passes-excluded-" + e.String() + "-comparison",
		"passes-excluded-" + e.String() + "-switch",
		"flags-neighbour-of-" + e.String(),
	}
}

// The generated document carries one util per excluded operand of its
// language, named after the exclusion, the static-rule document shape, and
// the rule's own id.
func TestGenerateDocumentShape(t *testing.T) {
	for _, lang := range rules.AllLangs {
		t.Run(lang.String(), func(t *testing.T) {
			c := catalog.Catalog{Lang: lang, Members: []catalog.Member{
				{Value: "open", Set: "s.Status", Source: catalog.SourceGoTypedConstant, Site: catalog.DefinitionSite{File: "s/s.go", Line: 1}},
				{Value: "a.b|c", Source: catalog.SourceTSUnion, Site: catalog.DefinitionSite{File: "t.ts", Line: 1}},
			}}
			rule, err := catalog.NewRuleGenerator().Generate(c)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				ID       string            `yaml:"id"`
				Language string            `yaml:"language"`
				Severity string            `yaml:"severity"`
				Message  string            `yaml:"message"`
				Metadata map[string]string `yaml:"metadata"`
				Utils    map[string]any    `yaml:"utils"`
				Rule     map[string]any    `yaml:"rule"`
			}
			decodeStrict(t, "generated document", rule.Document, &doc)
			if doc.ID != rule.ID.String() || rule.ID.Lang() != lang {
				t.Errorf("document id %q, rule id %s; want the %s closed-set rule", doc.ID, rule.ID, lang)
			}
			if doc.Severity != "warning" {
				t.Errorf("severity = %q, want warning", doc.Severity)
			}
			if doc.Message != rule.Message.What || doc.Metadata["why"] != rule.Message.Why || doc.Metadata["fix"] != rule.Message.Fix {
				t.Errorf("message/metadata do not carry the rule's what, why and fix")
			}
			for _, part := range []string{rule.Message.What, rule.Message.Why, rule.Message.Fix} {
				if strings.TrimSpace(part) == "" {
					t.Errorf("rule message has an empty part")
				}
			}
			var want []string
			for _, e := range rules.AllExcludedOperands {
				if e.Lang() == lang {
					want = append(want, catalog.ExcludedOperandUtil(e))
				}
			}
			var got []string
			for name := range doc.Utils {
				got = append(got, name)
			}
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("utils = %v, want exactly one per excluded operand of %s: %v", got, lang, want)
			}
			again, err := catalog.NewRuleGenerator().Generate(c)
			if err != nil || string(again.Document) != string(rule.Document) {
				t.Errorf("a second Generate of the same catalog is not byte-identical")
			}
		})
	}
}

func TestGenerateRefusesAnUnknownLanguage(t *testing.T) {
	if _, err := catalog.NewRuleGenerator().Generate(catalog.Catalog{Lang: "py"}); err == nil {
		t.Error("Generate accepted a catalog of language \"py\"")
	}
}
