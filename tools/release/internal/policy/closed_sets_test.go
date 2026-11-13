package policy

import (
	"bytes"
	_ "embed"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

//go:embed testdata/closed_sets.yaml
var closedSetsFixtureYAML []byte

// closedSetCase is one closed set in its wire spelling: the exact members in
// canonical order, and the names its parser must refuse.
type closedSetCase struct {
	Name    string   `yaml:"name"`
	Members []string `yaml:"members"`
	Refused []string `yaml:"refused"`
}

type closedSetFixtures struct {
	RequiredNames []string        `yaml:"required_names"`
	Sets          []closedSetCase `yaml:"sets"`
}

// closedSetUnderTest is the production side of one fixture set: its members
// rendered in canonical order, and its parser reduced to whether it accepts
// a name and what the accepted member renders as. parse is nil for a set
// that has no parser of its own.
type closedSetUnderTest struct {
	members []string
	parse   func(name string) (string, error)
}

// memberNames renders a closed set in canonical order.
func memberNames[T interface{ String() string }](members []T) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.String())
	}
	return out
}

// parserOf reduces a typed parser to the form the fixture drives.
func parserOf[T interface{ String() string }](parse func(string) (T, error)) func(string) (string, error) {
	return func(name string) (string, error) {
		member, err := parse(name)
		return member.String(), err
	}
}

// closedSetsUnderTest binds each fixture set name to the production set.
func closedSetsUnderTest() map[string]closedSetUnderTest {
	return map[string]closedSetUnderTest{
		"queue-rule":                   {memberNames(AllQueueRules), parserOf(ParseQueueRule)},
		"github-event":                 {memberNames(AllGitHubEvents), parserOf(ParseGitHubEvent)},
		"pull-request-action":          {memberNames(AllPullRequestActions), parserOf(ParsePullRequestAction)},
		"default-pull-request-actions": {memberNames(DefaultPullRequestActions), nil},
		"permission-level":             {memberNames(AllPermissionLevels), parserOf(ParsePermissionLevel)},
		"permission-shorthand":         {memberNames(AllPermissionShorthands), parserOf(ParsePermissionShorthand)},
		"writable-scope":               {memberNames(AllWritableScopes), parserOf(ParseWritableScope)},
		"allowed-action":               {memberNames(AllAllowedActions), parserOf(ParseAllowedAction)},
		"template-env":                 {memberNames(AllTemplateEnvs), parserOf(ParseTemplateEnv)},
		"credential-template":          {memberNames(AllCredentialTemplates), parserOf(ParseCredentialTemplate)},
		"action-input":                 {memberNames(AllActionInputs), parserOf(ParseActionInput)},
		"plumbing-template":            {memberNames(AllPlumbingTemplates), parserOf(ParsePlumbingTemplate)},
		"credential-job-class":         {memberNames(allCredentialJobClasses), nil},
		"runner-label":                 {memberNames(AllRunnerLabels), parserOf(ParseRunnerLabel)},
	}
}

func loadClosedSetFixtures(t *testing.T) closedSetFixtures {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(closedSetsFixtureYAML))
	dec.KnownFields(true)
	var fx closedSetFixtures
	if err := dec.Decode(&fx); err != nil {
		t.Fatalf("parse testdata/closed_sets.yaml: %v", err)
	}
	seen := map[string]bool{}
	for _, set := range fx.Sets {
		if set.Name == "" || seen[set.Name] {
			t.Fatalf("closed-set case name %q is empty or repeated", set.Name)
		}
		seen[set.Name] = true
		if !slices.Contains(fx.RequiredNames, set.Name) {
			t.Fatalf("closed-set case %q is not in required_names", set.Name)
		}
	}
	for _, name := range fx.RequiredNames {
		if !seen[name] {
			t.Fatalf("required closed-set case %q is missing from the corpus", name)
		}
	}
	return fx
}

// TestClosedSets holds every closed set this package owns to its exact
// membership and order, and holds each parser to both sides of its boundary:
// every member parses to itself, and every refused name is an error that
// yields the zero value, never a nearby member.
func TestClosedSets(t *testing.T) {
	t.Parallel()

	production := closedSetsUnderTest()
	fx := loadClosedSetFixtures(t)
	for name := range production {
		if !slices.Contains(fx.RequiredNames, name) {
			t.Errorf("production closed set %q has no fixture case", name)
		}
	}
	for _, set := range fx.Sets {
		t.Run(set.Name, func(t *testing.T) {
			t.Parallel()
			sut, ok := production[set.Name]
			if !ok {
				t.Fatalf("fixture set %q is bound to no production set", set.Name)
			}
			if !slices.Equal(sut.members, set.Members) {
				t.Fatalf("members = %q, want exactly %q", sut.members, set.Members)
			}
			if sut.parse == nil {
				if len(set.Refused) != 0 {
					t.Fatalf("set %q has no parser, so it can refuse nothing (fixture error)", set.Name)
				}
				return
			}
			for _, member := range set.Members {
				got, err := sut.parse(member)
				if err != nil || got != member {
					t.Errorf("parse(%q) = %q, %v; want the member itself", member, got, err)
				}
			}
			if len(set.Refused) == 0 {
				t.Fatalf("set %q names no refused value (fixture error)", set.Name)
			}
			for _, raw := range set.Refused {
				got, err := sut.parse(raw)
				if err == nil || got != "" {
					t.Errorf("parse(%q) = %q, %v; want the zero value and an error", raw, got, err)
				}
			}
		})
	}
}
