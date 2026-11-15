package policy

import (
	"context"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestPushHelperUsesGitCredentials exercises Git's credential protocol with
// synthetic data and no network. The command prefix comes from the same
// fixture checked by the production matcher; only push is replaced by
// "credential fill" so Git invokes the actual helper without a remote.
func TestPushHelperUsesGitCredentials(t *testing.T) {
	var fx struct {
		RequiredNames []string                `yaml:"required_names"`
		Cases         []credentialCommandCase `yaml:"cases"`
	}
	if err := strictUnmarshal(credentialCommandsFixtureYAML, &fx); err != nil {
		t.Fatal(err)
	}
	var command string
	for _, tc := range fx.Cases {
		if tc.Name == "accept-push-helper-branch" {
			command = tc.Run
			break
		}
	}
	if command == "" {
		t.Fatal("push helper fixture missing")
	}
	step := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "run"},
		{Kind: yaml.ScalarNode, Value: command},
	}}
	words, why := singleCommand(nil, nil, step)
	if why != "" || len(words) != 7 || literalAt(words, 3) != gitPush {
		t.Fatalf("push is not one seven-word command: %v, %s", words, why)
	}
	prefix, _, found := strings.Cut(command, " push ")
	if !found {
		t.Fatal("fixture has no push boundary")
	}
	const token = `dummy-$(printf injected)-"quoted";!`
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", prefix+" credential fill")
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"APP_TOKEN=" + token,
	}
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git credential fill failed: %v: %s", err, output)
	}
	want := "protocol=https\nhost=github.com\nusername=x-access-token\npassword=" + token + "\n"
	if string(output) != want {
		t.Fatalf("git credential fill = %q; want %q", output, want)
	}
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
