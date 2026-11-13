package policy

import (
	_ "embed"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

//go:embed testdata/credential_commands.yaml
var credentialCommandsFixtureYAML []byte

// credentialCommandCase is one command of a job that may hold a credential
// and the job it runs in: the job class, its own bot branch, and the step's
// effective env. The job needs the planning job of credentialCommandsDoc,
// so an env value may read its outputs. Plumbing selects the plumbing
// matcher instead of the credential template matcher. Template is the
// template the command must match exactly (an accept row); Fault is a
// fragment of the reason it is refused (a reject row, which names its
// accept sibling).
type credentialCommandCase struct {
	Name     string            `yaml:"name"`
	Class    string            `yaml:"class"`
	Branch   string            `yaml:"branch"`
	Env      map[string]string `yaml:"env"`
	Plumbing bool              `yaml:"plumbing"`
	Run      string            `yaml:"run"`
	Template string            `yaml:"template"`
	Fault    string            `yaml:"fault"`
	Sibling  string            `yaml:"sibling"`
}

// credentialCommandsDoc is the workflow every row's job sits in: a planning
// job whose plan step runs the planner and whose outputs map its tag and a
// PR number, an unrelated job, and the credential job that needs the
// planning job.
const credentialCommandsDoc = `jobs:
  plan:
    outputs:
      tag: ${{ steps.plan.outputs.tag }}
      number: ${{ steps.find.outputs.number }}
      version: ${{ steps.plan.outputs.version }}
      forged-tag: ${{ steps.find.outputs.tag }}
    steps:
      - id: plan
        run: release-intent plan --branch develop --mode tag
      - id: find
        run: echo number=7 >> "$GITHUB_OUTPUT"
  lint:
    outputs:
      tag: ${{ steps.plan.outputs.tag }}
    steps:
      - id: plan
        run: release-intent plan --branch develop --mode tag
  cred:
    needs: [plan]
    steps: []
`

// TestCredentialCommands drives the production tokenizer and the template
// and plumbing matchers over each template's accepted shape and the forms
// each refuses. Every template has an accepting row, so a template added to
// either set reddens here until the corpus samples it.
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
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(credentialCommandsDoc), &root); err != nil {
		t.Fatal(err)
	}
	doc := root.Content[0]
	accepted := map[string]bool{}
	for _, tc := range fx.Cases {
		class, ok := classes[tc.Class]
		if !ok {
			t.Fatalf("case %q names job class %q, not one of the closed classes", tc.Name, tc.Class)
		}
		if (tc.Template == "") == (tc.Fault == "") {
			t.Fatalf("case %q names neither or both of template and fault (fixture error)", tc.Name)
		}
		want := ""
		if tc.Template != "" {
			var err error
			if tc.Plumbing {
				_, err = ParsePlumbingTemplate(tc.Template)
			} else {
				_, err = ParseCredentialTemplate(tc.Template)
			}
			if err != nil {
				t.Fatalf("case %q: %v", tc.Name, err)
			}
			want = tc.Template
			accepted[want] = true
		} else if sib, ok := byName[tc.Sibling]; !ok || sib.Template == "" {
			t.Fatalf("reject case %q names no accepting sibling", tc.Name)
		}
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			step := templateStep{class: class, branch: tc.Branch, env: tc.Env, doc: doc, job: jobNode(doc, "cred")}
			got := ""
			words, why := tokenizeCommand(tc.Run)
			if why == "" {
				if tc.Plumbing {
					var tmpl PlumbingTemplate
					tmpl, why = matchPlumbing(words, step)
					got = tmpl.String()
				} else {
					var tmpl CredentialTemplate
					tmpl, why = matchTemplate(words, step)
					got = tmpl.String()
				}
			}
			if want != "" {
				if why != "" || got != want {
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
		if !accepted[tmpl.String()] {
			t.Errorf("no accepting row samples template %s", tmpl)
		}
	}
	for _, tmpl := range AllPlumbingTemplates {
		if !accepted[tmpl.String()] {
			t.Errorf("no accepting row samples plumbing template %s", tmpl)
		}
	}
}
