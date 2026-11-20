package policy

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	"github.com/peasant-labs/infra/tools/release/internal/testcase/assert"
	"gopkg.in/yaml.v3"
)

// The policy loader corpus lives in testdata/workflow_policy_load.yaml: one
// inline policy document per row with its expected failure fragment. The
// runner writes each row to a temp file and runs the production loader.

func writeQueuePolicyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filepath.Base(DefaultPath))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
	return path
}

//go:embed testdata/queue_policy.yaml
var queuePolicyYAML string

func TestLoadQueuePolicy_QueueSection(t *testing.T) {
	t.Parallel()

	policy, err := LoadQueuePolicy(writeQueuePolicyFile(t, queuePolicyYAML))
	if err != nil {
		t.Fatalf("LoadQueuePolicy(queue policy) failed: %v", err)
	}
	if len(policy.Queue.Required) != 2 {
		t.Fatalf("loaded queue has %d required entries, want 2", len(policy.Queue.Required))
	}
	gate := policy.Queue.Required[0]
	if gate.Context != "merge-gate" || gate.Workflow != "ci.yml" || gate.Job != "merge-gate" || !gate.Aggregator || gate.Guard {
		t.Fatalf("loaded required[0] = %+v, want the merge-gate aggregator entry", gate)
	}
	guard := policy.Queue.Required[1]
	if guard.Context != "title-guard" || guard.Workflow != "title-guard.yml" || guard.Job != "title-guard" || guard.Aggregator || !guard.Guard {
		t.Fatalf("loaded required[1] = %+v, want the title-guard guard entry", guard)
	}
	if policy.Queue.ReleaseJob == nil {
		t.Fatalf("loaded queue has a nil release job, want release-batch.yml/release")
	}
	rel := policy.Queue.ReleaseJob
	if rel.Workflow != "release-batch.yml" || rel.Job != "release" {
		t.Fatalf("loaded release job = %+v, want the release-batch entry", rel)
	}
	if len(rel.PreTagSteps) != 2 || rel.PreTagSteps[0] != (PreTagStep{ID: "verify-config", Check: PreTagCheckConfig}) || rel.PreTagSteps[1] != (PreTagStep{ID: "verify-workflows", Check: PreTagCheckWorkflows}) {
		t.Fatalf("loaded pre_tag_steps = %+v, want typed config and workflows checks", rel.PreTagSteps)
	}
	if len(policy.Queue.BotJobs) != 1 {
		t.Fatalf("loaded queue has %d bot jobs, want 1", len(policy.Queue.BotJobs))
	}
	bot := policy.Queue.BotJobs[0]
	if bot.Workflow != "cut-release.yml" || bot.Job != "cut" || bot.Kind != intent.PRKindCutRelease {
		t.Fatalf("loaded bot job = %+v, want the %s kind entry", bot, intent.PRKindCutRelease)
	}
}

//go:embed testdata/workflow_policy_load.yaml
var workflowPolicyLoadYAML []byte

// loadCaseInput is one inline policy document from the loader corpus.
type loadCaseInput struct {
	Policy string `yaml:"policy"`
}

// loadCaseExpected is the failure fragment a must-fail loader row must carry.
type loadCaseExpected struct {
	WantContains []string `yaml:"want_contains"`
}

// loadCaseRow is one loader corpus row.
type loadCaseRow struct {
	Name           string                  `yaml:"name"`
	Input          loadCaseInput           `yaml:"input"`
	Expected       loadCaseExpected        `yaml:"expected"`
	Classification testcase.Classification `yaml:"classification"`
	Provenance     testcase.Provenance     `yaml:"provenance"`
	Mutation       testcase.Mutation       `yaml:"mutation"`
	Sibling        string                  `yaml:"sibling"`
}

func (r loadCaseRow) siblingName() string                   { return r.Name }
func (r loadCaseRow) siblingClass() testcase.Classification { return r.Classification }
func (r loadCaseRow) siblingOf() string                     { return r.Sibling }

// loadPolicyFixtures is the parsed loader corpus with its manifest.
type loadPolicyFixtures struct {
	RequiredNames []string      `yaml:"required_names"`
	Rows          []loadCaseRow `yaml:"cases"`
}

func loadLoadPolicyFixtures(t *testing.T) loadPolicyFixtures {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(workflowPolicyLoadYAML))
	dec.KnownFields(true)
	var f loadPolicyFixtures
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("load policy loader cases (testdata/workflow_policy_load.yaml): %v", err)
	}
	cases := make([]testcase.Case[loadCaseInput, loadCaseExpected], 0, len(f.Rows))
	rows := make([]siblingCased, 0, len(f.Rows))
	for _, row := range f.Rows {
		row := row
		cases = append(cases, testcase.Case[loadCaseInput, loadCaseExpected]{
			Name: row.Name, Input: row.Input, Expected: row.Expected,
			Classification: row.Classification, Provenance: row.Provenance, Mutation: row.Mutation,
		})
		rows = append(rows, row)
	}
	corpus := testcase.Corpus[loadCaseInput, loadCaseExpected]{Cases: cases}
	assert.RequireValid(t, corpus)
	checkSiblings(t, rows)
	seen := map[string]bool{}
	for _, row := range f.Rows {
		seen[row.Name] = true
	}
	for _, name := range f.RequiredNames {
		if !seen[name] {
			t.Fatalf("required loader case %q is missing", name)
		}
	}
	if len(seen) != len(f.RequiredNames) {
		t.Fatalf("required_names names %d cases but the corpus carries %d", len(f.RequiredNames), len(seen))
	}
	return f
}

func TestLoadQueuePolicy_LoadCases(t *testing.T) {
	t.Parallel()

	for _, row := range loadLoadPolicyFixtures(t).Rows {
		row := row
		t.Run(row.Name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadQueuePolicy(writeQueuePolicyFile(t, row.Input.Policy))
			if row.Classification == testcase.MustPass {
				if err != nil {
					t.Fatalf("accept case %q failed to load: %v", row.Name, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("reject case %q loaded without error", row.Name)
			}
			for _, want := range row.Expected.WantContains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain expected fragment %q", err.Error(), want)
				}
			}
		})
	}
}

//go:embed testdata/private-install.yaml
var privateInstallFixture []byte

func TestWorkflowPrivateNpmInstall(t *testing.T) {
	var fixture struct {
		Required []string `yaml:"required_names"`
		Checks   []struct {
			Name     string `yaml:"name"`
			Pattern  string `yaml:"pattern"`
			Workflow string `yaml:"workflow"`
			Job      string `yaml:"job"`
			Step     string `yaml:"step"`
			Run      string `yaml:"run"`
		} `yaml:"checks"`
	}
	if err := yaml.Unmarshal(privateInstallFixture, &fixture); err != nil {
		t.Fatal(err)
	}
	checks := make(map[string]int)
	dir := filepath.Join(filepath.Join("..", "..", "..", ".."), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range fixture.Checks {
		checks[check.Name]++
		switch check.Name {
		case "no_global_npm":
			pattern, err := regexp.Compile(check.Pattern)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range entries {
				if entry.IsDir() || !(strings.HasSuffix(entry.Name(), ".yml") || strings.HasSuffix(entry.Name(), ".yaml")) {
					continue
				}
				found = true
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Jobs map[string]struct {
						Steps []struct {
							Name string `yaml:"name"`
							Run  string `yaml:"run"`
						} `yaml:"steps"`
					} `yaml:"jobs"`
				}
				if err := yaml.Unmarshal(data, &doc); err != nil {
					t.Fatal(err)
				}
				for job, v := range doc.Jobs {
					for _, step := range v.Steps {
						if pattern.MatchString(step.Run) {
							t.Errorf("%s job %s step %s uses global npm install", entry.Name(), job, step.Name)
						}
					}
				}
			}
			if !found {
				t.Fatal("no workflow files found")
			}
		case "private_ast_grep":
			data, err := os.ReadFile(filepath.Join(dir, check.Workflow))
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Jobs map[string]struct {
					Steps []struct {
						Name string `yaml:"name"`
						Run  string `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, step := range doc.Jobs[check.Job].Steps {
				if step.Name == check.Step {
					n++
					if step.Run != check.Run {
						t.Errorf("%s job %s step %s does not use the job-private install", check.Workflow, check.Job, check.Step)
					}
				}
			}
			if n != 1 {
				t.Errorf("%s job %s: found %d install steps, want 1", check.Workflow, check.Job, n)
			}
		default:
			t.Fatalf("unknown check %q", check.Name)
		}
	}
	if len(fixture.Required) != 2 || checks["no_global_npm"] != 1 || checks["private_ast_grep"] != 1 {
		t.Fatal("private npm install fixture must retain both required checks")
	}
	for _, name := range fixture.Required {
		if checks[name] != 1 {
			t.Errorf("required check %s appears %d times", name, checks[name])
		}
	}
	if len(fixture.Required) != len(checks) {
		t.Fatal("fixture required names differ from checks")
	}
}
