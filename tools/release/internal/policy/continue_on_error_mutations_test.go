package policy

import (
	_ "embed"
	"testing"
)

//go:embed testdata/continue_on_error_mutations.yaml
var continueOnErrorMutationsFixtureYAML []byte

// continueOnErrorMutation is one row of the continue-on-error corpus: one
// workflow file, the find/replace that adds the key, the exact rule set that
// must fire, the fragments the diagnostics must carry, and the accept row it
// differs from.
type continueOnErrorMutation struct {
	Name         string      `yaml:"name"`
	Workflow     string      `yaml:"workflow"`
	Find         string      `yaml:"find"`
	Replace      string      `yaml:"replace"`
	Rules        []QueueRule `yaml:"rules"`
	WantContains []string    `yaml:"want_contains"`
	Sibling      string      `yaml:"sibling"`
}

// TestContinueOnErrorMutations proves the policy refuses the continue-on-error
// key on every step or job whose failure is a security gate, whatever the
// key's value: literal true, the documented fromJSON('true') form, a plain
// expression, a value from inputs or needs, and literal false all fire the
// rule the gate belongs to. A literal-only comparison would be a bypass,
// because GitHub evaluates the key as a value.
func TestContinueOnErrorMutations(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string                  `yaml:"required_names"`
		Cases         []continueOnErrorMutation `yaml:"cases"`
	}
	if err := strictUnmarshal(continueOnErrorMutationsFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/continue_on_error_mutations.yaml: %v", err)
	}
	var names []string
	accepted := map[string]bool{}
	for _, c := range fx.Cases {
		names = append(names, c.Name)
		if len(c.Rules) == 0 {
			accepted[c.Name] = true
		}
	}
	manifestNames(t, "continue_on_error_mutations.yaml", fx.RequiredNames, names)
	if diags, err := checkMutationTree(t, materializeQueueFixture(t, "")); err != nil || len(diags) != 0 {
		t.Fatalf("the unmutated copy does not pass: %v %+v", err, diags)
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			switch {
			case len(tc.Rules) == 0 && tc.Sibling != "":
				t.Fatal("an accepted row names a sibling (fixture error)")
			case len(tc.Rules) > 0 && !accepted[tc.Sibling]:
				t.Fatalf("rejected row's sibling %q is not an accepted row (fixture error)", tc.Sibling)
			}
			runWorkflowMutation(t, "", pushMutation{
				Name:         tc.Name,
				Workflow:     tc.Workflow,
				Find:         tc.Find,
				Replace:      tc.Replace,
				Rules:        tc.Rules,
				WantContains: tc.WantContains,
			})
		})
	}
}
