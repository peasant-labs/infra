package policy

import (
	_ "embed"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

//go:embed testdata/credential_commands.yaml
var credentialCommandsFixtureYAML []byte

// credentialCommandCase is one credential step's command and the job it
// runs in: the job class, its own bot branch, the step's effective env, and
// the ids of the job's planner steps. Template is the template the command
// must match exactly (an accept row); Fault is a fragment of the reason it
// is refused (a reject row, which names its accept sibling).
type credentialCommandCase struct {
	Name      string            `yaml:"name"`
	Class     string            `yaml:"class"`
	Branch    string            `yaml:"branch"`
	Env       map[string]string `yaml:"env"`
	PlanSteps []string          `yaml:"plan_steps"`
	Run       string            `yaml:"run"`
	Template  string            `yaml:"template"`
	Fault     string            `yaml:"fault"`
	Sibling   string            `yaml:"sibling"`
}

// TestCredentialCommands drives the production tokenizer and template
// matcher over each template's accepted shape and the forms each refuses.
// Every template has an accepting row, so a template added to the set
// reddens here until the corpus samples it.
func TestCredentialCommands(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string                `yaml:"required_names"`
		Cases         []credentialCommandCase `yaml:"cases"`
	}
	if err := strictUnmarshal(credentialCommandsFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/credential_commands.yaml: %v", err)
	}
	byName := map[string]credentialCommandCase{}
	var names []string
	for _, c := range fx.Cases {
		names = append(names, c.Name)
		byName[c.Name] = c
	}
	manifestNames(t, "credential_commands.yaml", fx.RequiredNames, names)
	classes := map[string]credentialJobClass{}
	for _, class := range allCredentialJobClasses {
		classes[class.String()] = class
	}
	accepted := map[CredentialTemplate]bool{}
	for _, tc := range fx.Cases {
		class, ok := classes[tc.Class]
		if !ok {
			t.Fatalf("case %q names job class %q, not one of the closed classes", tc.Name, tc.Class)
		}
		if (tc.Template == "") == (tc.Fault == "") {
			t.Fatalf("case %q names neither or both of template and fault (fixture error)", tc.Name)
		}
		var want CredentialTemplate
		if tc.Template != "" {
			parsed, err := ParseCredentialTemplate(tc.Template)
			if err != nil {
				t.Fatalf("case %q: %v", tc.Name, err)
			}
			want = parsed
			accepted[want] = true
		} else if sib, ok := byName[tc.Sibling]; !ok || sib.Template == "" {
			t.Fatalf("reject case %q names no accepting sibling", tc.Name)
		}
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			step := templateStep{class: class, branch: tc.Branch, env: tc.Env, job: planJob(t, tc.PlanSteps)}
			got, why := "", ""
			words, why := tokenizeCommand(tc.Run)
			if why == "" {
				var tmpl CredentialTemplate
				tmpl, why = matchTemplate(words, step)
				got = tmpl.String()
			}
			if want != "" {
				if why != "" || got != want.String() {
					t.Fatalf("run %q = %q, %q; want template %s", tc.Run, got, why, want)
				}
				return
			}
			if why == "" || !strings.Contains(why, tc.Fault) {
				t.Fatalf("run %q refused for %q, want a reason containing %q", tc.Run, why, tc.Fault)
			}
		})
	}
	for _, tmpl := range AllCredentialTemplates {
		if !accepted[tmpl] {
			t.Errorf("no accepting row samples template %s", tmpl)
		}
	}
}

// planJob builds a job node whose steps with the given ids run the planner.
func planJob(t *testing.T, ids []string) *yaml.Node {
	t.Helper()
	var b strings.Builder
	b.WriteString("steps:\n")
	for _, id := range ids {
		b.WriteString("  - id: " + id + "\n    run: release-intent plan --branch develop --mode tag\n")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}
