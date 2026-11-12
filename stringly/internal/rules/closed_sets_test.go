package rules_test

import (
	"bytes"
	_ "embed"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/stringly/internal/rules"
)

//go:embed testdata/closed-sets.yaml
var closedSetsYAML []byte

type closedSetsFixture struct {
	Langs           []string `yaml:"langs"`
	Origins         []string `yaml:"origins"`
	ExclusionGroups []string `yaml:"exclusionGroups"`
	RuleIDs         []struct {
		ID            string `yaml:"id"`
		Lang          string `yaml:"lang"`
		Origin        string `yaml:"origin"`
		AcceptsMarker bool   `yaml:"acceptsMarker"`
	} `yaml:"ruleIds"`
	ExcludedOperands []struct {
		ID    string `yaml:"id"`
		Group string `yaml:"group"`
		Rule  string `yaml:"rule"`
	} `yaml:"excludedOperands"`
	Invalid []string `yaml:"invalid"`
}

func loadClosedSetsFixture(t *testing.T) closedSetsFixture {
	t.Helper()
	var f closedSetsFixture
	dec := yaml.NewDecoder(bytes.NewReader(closedSetsYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/closed-sets.yaml: %v", err)
	}
	return f
}

// closedSet is the Go-style closed-set surface every type here offers.
type closedSet interface {
	~string
	String() string
	IsValid() bool
}

// assertMembership checks that all equals want as a set, without duplicates,
// and that every member round-trips through String, IsValid and parse.
func assertMembership[T closedSet](t *testing.T, name string, all []T, want []string, parse func(string) (T, error)) {
	t.Helper()
	got := make([]string, 0, len(all))
	for _, v := range all {
		if slices.Contains(got, v.String()) {
			t.Errorf("%s lists %q twice", name, v)
		}
		got = append(got, v.String())
		if !v.IsValid() {
			t.Errorf("%s member %q reports IsValid() == false", name, v)
		}
		parsed, err := parse(v.String())
		if err != nil || parsed != v {
			t.Errorf("parse %s %q = %q, %v; want the member back", name, v, parsed, err)
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("%s is missing required member %q (testdata/closed-sets.yaml)", name, w)
		}
	}
	for _, g := range got {
		if !slices.Contains(want, g) {
			t.Errorf("%s has member %q that testdata/closed-sets.yaml does not list; add it there with its attributes", name, g)
		}
	}
}

func TestLangMembership(t *testing.T) {
	f := loadClosedSetsFixture(t)
	assertMembership(t, "AllLangs", rules.AllLangs, f.Langs, rules.ParseLang)
}

func TestOriginMembership(t *testing.T) {
	f := loadClosedSetsFixture(t)
	assertMembership(t, "AllOrigins", rules.AllOrigins, f.Origins, rules.ParseOrigin)
}

func TestExclusionGroupMembership(t *testing.T) {
	f := loadClosedSetsFixture(t)
	assertMembership(t, "AllExclusionGroups", rules.AllExclusionGroups, f.ExclusionGroups, rules.ParseExclusionGroup)
}

func TestRuleIDMembershipAndAttributes(t *testing.T) {
	f := loadClosedSetsFixture(t)
	want := make([]string, len(f.RuleIDs))
	for i, r := range f.RuleIDs {
		want[i] = r.ID
	}
	assertMembership(t, "AllRuleIDs", rules.AllRuleIDs, want, rules.ParseRuleID)

	for _, r := range f.RuleIDs {
		id := rules.RuleID(r.ID)
		if got := id.Lang().String(); got != r.Lang {
			t.Errorf("%s.Lang() = %q, want %q", id, got, r.Lang)
		}
		if got := id.Origin().String(); got != r.Origin {
			t.Errorf("%s.Origin() = %q, want %q", id, got, r.Origin)
		}
		if got := id.AcceptsMarker(); got != r.AcceptsMarker {
			t.Errorf("%s.AcceptsMarker() = %v, want %v", id, got, r.AcceptsMarker)
		}
		rule := rules.Rule{ID: id}
		if rule.Lang() != id.Lang() || rule.Origin() != id.Origin() || rule.AcceptsMarker() != id.AcceptsMarker() {
			t.Errorf("Rule{ID: %s} disagrees with its id's attributes", id)
		}
	}
}

func TestExcludedOperandMembershipAndAttributes(t *testing.T) {
	f := loadClosedSetsFixture(t)
	want := make([]string, len(f.ExcludedOperands))
	for i, e := range f.ExcludedOperands {
		want[i] = e.ID
	}
	assertMembership(t, "AllExcludedOperands", rules.AllExcludedOperands, want, rules.ParseExcludedOperand)

	for _, e := range f.ExcludedOperands {
		op := rules.ExcludedOperand(e.ID)
		if got := op.Group().String(); got != e.Group {
			t.Errorf("%s.Group() = %q, want %q", op, got, e.Group)
		}
		if got := op.Rule().String(); got != e.Rule {
			t.Errorf("%s.Rule() = %q, want %q", op, got, e.Rule)
		}
		if got, want := op.Lang(), op.Rule().Lang(); got != want || !got.IsValid() {
			t.Errorf("%s.Lang() = %q, want the language of %s (%q)", op, got, op.Rule(), want)
		}
		if op.Rule().Origin() != rules.OriginGenerated {
			t.Errorf("%s belongs to %s, which is not a generated closed-set rule", op, op.Rule())
		}
	}
}

func TestParsersRefuseInvalidValues(t *testing.T) {
	f := loadClosedSetsFixture(t)
	for _, s := range f.Invalid {
		if v, err := rules.ParseLang(s); err == nil {
			t.Errorf("ParseLang(%q) = %q, want an error", s, v)
		}
		if v, err := rules.ParseOrigin(s); err == nil {
			t.Errorf("ParseOrigin(%q) = %q, want an error", s, v)
		}
		if v, err := rules.ParseExclusionGroup(s); err == nil {
			t.Errorf("ParseExclusionGroup(%q) = %q, want an error", s, v)
		}
		if v, err := rules.ParseRuleID(s); err == nil {
			t.Errorf("ParseRuleID(%q) = %q, want an error", s, v)
		}
		if v, err := rules.ParseExcludedOperand(s); err == nil {
			t.Errorf("ParseExcludedOperand(%q) = %q, want an error", s, v)
		}
		id := rules.RuleID(s)
		if id.Lang() != "" || id.Origin() != "" || id.AcceptsMarker() {
			t.Errorf("invalid RuleID %q reports attributes (lang %q, origin %q, marker %v); want zero values",
				s, id.Lang(), id.Origin(), id.AcceptsMarker())
		}
		op := rules.ExcludedOperand(s)
		if op.Group() != "" || op.Rule() != "" {
			t.Errorf("invalid ExcludedOperand %q reports group %q and rule %q; want zero values", s, op.Group(), op.Rule())
		}
	}
}

func TestHitKeyDropsText(t *testing.T) {
	a := rules.Hit{Rule: rules.JSXLiteralTestID, File: "src/a.tsx", Line: 3, Text: `"row"`}
	b := a
	b.Text = `{"row"}`
	if a.Key() != b.Key() {
		t.Errorf("two matches of one rule on one line have different keys: %+v and %+v", a.Key(), b.Key())
	}
}
