package closedset_test

import (
	"bytes"
	_ "embed"
	"errors"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/closedset.yaml
var closedSetYAML []byte

// sample is the closed set the fixture drives; its members come from the
// fixture, which the rule never sees as anything but an ordered list.
type sample string

func (s sample) String() string { return string(s) }

type contextFixture struct {
	Set   string `yaml:"set"`
	Where string `yaml:"where"`
	When  string `yaml:"when"`
	Means string `yaml:"means"`
}

type parseCase struct {
	Name           string                  `yaml:"name"`
	Classification testcase.Classification `yaml:"classification"`
	Sibling        string                  `yaml:"sibling"`
	Raw            string                  `yaml:"raw"`
	Want           sample                  `yaml:"want"`
	WantWhat       string                  `yaml:"want_what"`
	WantFix        string                  `yaml:"want_fix"`
}

type joinCase struct {
	Name    string   `yaml:"name"`
	Members []sample `yaml:"members"`
	Sep     string   `yaml:"sep"`
	Want    string   `yaml:"want"`
}

type closedSetFixture struct {
	RequiredNames []string       `yaml:"required_names"`
	Members       []sample       `yaml:"members"`
	Context       contextFixture `yaml:"context"`
	Parse         []parseCase    `yaml:"parse"`
	Join          []joinCase     `yaml:"join"`
}

func loadClosedSetFixture(t *testing.T) closedSetFixture {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(closedSetYAML))
	dec.KnownFields(true)
	var fx closedSetFixture
	if err := dec.Decode(&fx); err != nil {
		t.Fatalf("parse testdata/closedset.yaml: %v", err)
	}
	var names []string
	for _, c := range fx.Parse {
		names = append(names, c.Name)
	}
	for _, c := range fx.Join {
		names = append(names, c.Name)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			t.Fatalf("closedset case name %q is empty or repeated", name)
		}
		seen[name] = true
		if !slices.Contains(fx.RequiredNames, name) {
			t.Fatalf("closedset case %q is not in required_names", name)
		}
	}
	for _, name := range fx.RequiredNames {
		if !seen[name] {
			t.Fatalf("required closedset case %q is missing from the corpus", name)
		}
	}
	return fx
}

// TestParse holds Parse to both sides of the boundary: every listed member
// parses to itself, and every other value is a six-part *Error that yields
// the zero member, carries the caller's context unchanged and lists the
// whole set in its fix. Each must-fail case names a must-pass sibling, so it
// fails for its raw value alone.
func TestParse(t *testing.T) {
	t.Parallel()
	fx := loadClosedSetFixture(t)
	ctx := closedset.Context{Set: fx.Context.Set, Where: fx.Context.Where, When: fx.Context.When, Means: fx.Context.Means}
	byName := map[string]parseCase{}
	for _, c := range fx.Parse {
		byName[c.Name] = c
	}
	for _, c := range fx.Parse {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			got, err := closedset.Parse(fx.Members, c.Raw, ctx)
			switch c.Classification {
			case testcase.MustPass:
				if err != nil || got != c.Want {
					t.Fatalf("Parse(%q) = %q, %v; want %q", c.Raw, got, err, c.Want)
				}
			case testcase.MustFail:
				sibling, ok := byName[c.Sibling]
				if !ok || sibling.Classification != testcase.MustPass {
					t.Fatalf("sibling %q is not a must-pass parse case (fixture error)", c.Sibling)
				}
				if _, err := closedset.Parse(fx.Members, sibling.Raw, ctx); err != nil {
					t.Fatalf("sibling %q does not parse: %v", c.Sibling, err)
				}
				var refusal *closedset.Error
				if !errors.As(err, &refusal) {
					t.Fatalf("Parse(%q) = %q, %v; want a *closedset.Error", c.Raw, got, err)
				}
				if got != "" {
					t.Errorf("Parse(%q) returned %q with its refusal; want the zero member", c.Raw, got)
				}
				want := closedset.Error{
					What: c.WantWhat, Why: refusal.Why,
					Where: ctx.Where, When: ctx.When, Means: ctx.Means,
					Fix: c.WantFix,
				}
				if *refusal != want {
					t.Errorf("refusal = %+v, want %+v", *refusal, want)
				}
				if refusal.Why == "" {
					t.Errorf("refusal has no why: %+v", *refusal)
				}
			default:
				t.Fatalf("case %q has classification %q (fixture error)", c.Name, c.Classification)
			}
		})
	}
}

// TestJoin holds Join to the members in the order given, separated by sep.
func TestJoin(t *testing.T) {
	t.Parallel()
	fx := loadClosedSetFixture(t)
	for _, c := range fx.Join {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if got := closedset.Join(c.Members, c.Sep); got != c.Want {
				t.Fatalf("Join(%q, %q) = %q, want %q", c.Members, c.Sep, got, c.Want)
			}
		})
	}
}
