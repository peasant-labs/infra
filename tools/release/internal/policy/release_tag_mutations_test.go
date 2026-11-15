package policy

import (
	_ "embed"
	"slices"
	"testing"
)

//go:embed testdata/release_tag_mutations.yaml
var releaseTagMutationsFixtureYAML []byte

// releaseTagWorkflow is the file every release-tag mutation changes: the
// queue corpus base's release workflow, whose release job pushes the tag.
const releaseTagWorkflow = "release-batch.yml"

// TestReleaseTagMutations proves the tag cannot leave the release job before
// a check that the job's own condition pins to the default branch: the
// ancestor check has to run after the fetch and before the push, under the
// push's own if, with no continue-on-error; the trigger set and the job if
// have to make github.sha a default-branch commit; and the push's if carries
// no status function, so a failed check skips the push.
func TestReleaseTagMutations(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string            `yaml:"required_names"`
		Cases         []tagVerifyMutation `yaml:"cases"`
	}
	if err := strictUnmarshal(releaseTagMutationsFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/release_tag_mutations.yaml: %v", err)
	}
	var names []string
	accepted := map[string]bool{}
	for _, c := range fx.Cases {
		names = append(names, c.Name)
		if len(c.Rules) == 0 {
			accepted[c.Name] = true
		}
	}
	manifestNames(t, "release_tag_mutations.yaml", fx.RequiredNames, names)
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
			case len(tc.Rules) > 0 && !slices.Contains(tc.Rules, QueueRuleReleaseTagBoundary):
				t.Fatalf("rejected row does not fire %s (fixture error)", QueueRuleReleaseTagBoundary)
			}
			runWorkflowMutation(t, "", pushMutation{
				Name:         tc.Name,
				Workflow:     releaseTagWorkflow,
				Find:         tc.Find,
				Replace:      tc.Replace,
				Rules:        tc.Rules,
				WantContains: tc.WantContains,
			})
		})
	}
}
