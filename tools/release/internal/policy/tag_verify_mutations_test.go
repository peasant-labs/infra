package policy

import (
	_ "embed"
	"slices"
	"testing"
)

//go:embed testdata/tag_verify_mutations.yaml
var tagVerifyMutationsFixtureYAML []byte

// tagVerifyOverlay is the tree the tag-verify mutations change: the queue
// corpus base plus the tag-triggered publish workflow, whose verify job has
// fairtrade's shape.
const tagVerifyOverlay = "accept-tag-publish"

// tagVerifyWorkflow is the file every tag-verify mutation changes.
const tagVerifyWorkflow = "release.yml"

// tagVerifyMutation is one row of the tag-verify corpus: one change to the
// publish workflow, the exact rule set it fires (none for an accepted
// change), the fragments the diagnostics carry, and, for a reject, the
// accept row it differs from.
type tagVerifyMutation struct {
	Name         string      `yaml:"name"`
	Find         string      `yaml:"find"`
	Replace      string      `yaml:"replace"`
	Rules        []QueueRule `yaml:"rules"`
	WantContains []string    `yaml:"want_contains"`
	Sibling      string      `yaml:"sibling"`
}

// TestTagVerifyMutations proves the tag-verify job is confined: every way
// the job could succeed without the tool's yes (a masked exit status, a PATH
// entry or stub binary, a build that compiles other code, a shell or env
// override, an unlisted action or input) fires tag-publish-verifies-the-tag,
// and each accepted variant passes.
func TestTagVerifyMutations(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string            `yaml:"required_names"`
		Cases         []tagVerifyMutation `yaml:"cases"`
	}
	if err := strictUnmarshal(tagVerifyMutationsFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/tag_verify_mutations.yaml: %v", err)
	}
	var names []string
	accepted := map[string]bool{}
	for _, c := range fx.Cases {
		names = append(names, c.Name)
		if len(c.Rules) == 0 {
			accepted[c.Name] = true
		}
	}
	manifestNames(t, "tag_verify_mutations.yaml", fx.RequiredNames, names)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			switch {
			case len(tc.Rules) == 0 && tc.Sibling != "":
				t.Fatal("an accepted row names a sibling (fixture error)")
			case len(tc.Rules) > 0 && !accepted[tc.Sibling]:
				t.Fatalf("rejected row's sibling %q is not an accepted row (fixture error)", tc.Sibling)
			case len(tc.Rules) > 0 && !slices.Contains(tc.Rules, QueueRuleTagVerified):
				t.Fatalf("rejected row does not fire %s (fixture error)", QueueRuleTagVerified)
			}
			runWorkflowMutation(t, tagVerifyOverlay, pushMutation{
				Name:         tc.Name,
				Workflow:     tagVerifyWorkflow,
				Find:         tc.Find,
				Replace:      tc.Replace,
				Rules:        tc.Rules,
				WantContains: tc.WantContains,
			})
		})
	}
}
