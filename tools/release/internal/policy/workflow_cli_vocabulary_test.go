package policy

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	"gopkg.in/yaml.v3"
)

//go:embed testdata/workflow_cli_vocabulary.yaml
var workflowCLIVocabularyCases []byte

type workflowCLIExample struct {
	Name     string   `yaml:"name"`
	Run      string   `yaml:"run"`
	Problems []string `yaml:"problems"`
	Sibling  string   `yaml:"sibling"`
}

// workflowCLIProblems runs the production tool-call reader and checker over
// one run block: whether it holds an invocation, and every problem the
// tool-calls-match-the-cli rule reports for its invocations, in order.
func workflowCLIProblems(run string) (bool, []string) {
	calls, _ := toolCalls(run)
	var problems []string
	for _, call := range calls {
		problems = append(problems, toolCallProblems(call)...)
	}
	return len(calls) > 0, problems
}

func TestWorkflowCLIVocabularyExamples(t *testing.T) {
	t.Parallel()
	var corpus struct {
		RequiredNames []string             `yaml:"required_names"`
		Cases         []workflowCLIExample `yaml:"cases"`
	}
	if err := yaml.Unmarshal(workflowCLIVocabularyCases, &corpus); err != nil {
		t.Fatal(err)
	}
	byName := map[string]workflowCLIExample{}
	for _, row := range corpus.Cases {
		if row.Name == "" || !slices.Contains(corpus.RequiredNames, row.Name) {
			t.Fatalf("case %q has no required-name manifest entry", row.Name)
		}
		if _, ok := byName[row.Name]; ok {
			t.Fatalf("duplicate case %q", row.Name)
		}
		byName[row.Name] = row
	}
	for _, name := range corpus.RequiredNames {
		if _, ok := byName[name]; !ok {
			t.Fatalf("required case %q is missing", name)
		}
	}
	// Every command has an accepted call, and every command with a required
	// flag has a reject that drops one, so a command added to the vocabulary
	// reddens here until the corpus samples it.
	accepted := map[intent.Command]bool{}
	missing := map[intent.Command]bool{}
	for _, row := range corpus.Cases {
		calls, _ := toolCalls(row.Run)
		for _, call := range calls {
			if len(call) < 2 {
				continue
			}
			command, err := intent.ParseCommand(call[1].bare)
			if err != nil {
				continue
			}
			problems := toolCallProblems(call)
			if len(problems) == 0 {
				accepted[command] = true
			}
			for _, flag := range command.RequiredFlags() {
				if slices.Contains(problems, "missing required flag "+strconv.Quote(flag.String())) {
					missing[command] = true
				}
			}
		}
	}
	for _, command := range intent.AllCommands {
		if !accepted[command] {
			t.Errorf("no accepted call samples command %s", command)
		}
		if len(command.RequiredFlags()) > 0 && !missing[command] {
			t.Errorf("no reject drops a required flag of command %s", command)
		}
	}
	for _, row := range corpus.Cases {
		if len(row.Problems) != 0 {
			sibling, ok := byName[row.Sibling]
			if !ok || len(sibling.Problems) != 0 {
				t.Fatalf("reject case %q has no accepting sibling", row.Name)
			}
		}
		t.Run(row.Name, func(t *testing.T) {
			found, got := workflowCLIProblems(row.Run)
			if !found {
				t.Fatal("no tool invocation found")
			}
			if !slices.Equal(got, row.Problems) {
				t.Errorf("problems = %q, want %q", got, row.Problems)
			}
		})
	}
}

// TestWorkflowFixturesUseCLIContract scans every base and overlay workflow,
// including overlays not selected by the current corpus. The only exemption
// is an overlay whose corpus row is a must-fail sample of the tool-call rule:
// it misspells a CLI word on purpose.
func TestWorkflowFixturesUseCLIContract(t *testing.T) {
	t.Parallel()
	exempt := map[string]bool{}
	for _, row := range loadQueueWorkflowFixtures(t).Rows {
		if row.Classification == testcase.MustFail && row.Expected.Rule == QueueRuleToolCalls {
			exempt[row.Input.Overlay] = true
		}
	}
	found := false
	err := fs.WalkDir(workflowQueueFixtureFS, "testdata/workflow_queue", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Base(path.Dir(name)) != fixtureWorkflowsDir || (path.Ext(name) != ".yml" && path.Ext(name) != ".yaml") {
			return nil
		}
		if overlay := path.Base(path.Dir(path.Dir(name))); exempt[overlay] {
			return nil
		}
		data, err := workflowQueueFixtureFS.ReadFile(name)
		if err != nil {
			return err
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		if len(document.Content) == 0 {
			return fmt.Errorf("empty workflow %s", name)
		}
		doc := document.Content[0]
		for _, job := range jobNames(doc) {
			for _, step := range stepsOf(jobNode(doc, job)) {
				seen, problems := workflowCLIProblems(stepRunText(step))
				found = found || seen
				for _, problem := range problems {
					t.Errorf("%s job %s step %q: %s", name, job, stepID(step), problem)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no release tool invocations found in workflow fixtures")
	}
}
