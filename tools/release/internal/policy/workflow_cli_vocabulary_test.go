package policy

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
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

// workflowCLIProblems checks the wire spellings in run blocks against the
// shared CLI vocabulary. Expressions and quoted words remain intact through
// the policy checker's shell-word splitter. A build's binary and package path
// are arguments, not invocations, so only executable words are inspected.
func workflowCLIProblems(run string) (bool, []string) {
	joined := strings.ReplaceAll(strings.ReplaceAll(run, "\\\r\n", " "), "\\\n", " ")
	found := false
	var problems []string
	for _, text := range workflowShellSegments(joined) {
		segment := shellWords(text)
		for i, word := range segment {
			if path.Base(bareWord(word)) != intent.ProgramName {
				continue
			}
			// A bare executable, or one following a shell wrapper. A build
			// or echo mentioning the program is not a tool invocation.
			if i > 0 && (bareWord(segment[0]) == "go" || bareWord(segment[0]) == "echo") {
				continue
			}
			found = true
			if i+1 >= len(segment) {
				problems = append(problems, "missing command")
				continue
			}
			command := bareWord(segment[i+1])
			if _, err := intent.ParseCommand(command); err != nil {
				problems = append(problems, fmt.Sprintf("unknown command %q", command))
			}
			for _, arg := range segment[i+2:] {
				arg = bareWord(arg)
				if !strings.HasPrefix(arg, "-") {
					continue
				}
				flag, _, _ := strings.Cut(arg, "=")
				if _, err := intent.ParseFlag(flag); err != nil {
					problems = append(problems, fmt.Sprintf("unknown flag %q", flag))
				}
			}
			break
		}
	}
	return found, problems
}

// workflowShellSegments splits commands even when a separator touches a word.
// Shell quotes and GitHub expressions can themselves contain separators.
func workflowShellSegments(run string) []string {
	var segments []string
	var quote byte
	start := 0
	for i := 0; i < len(run); i++ {
		if strings.HasPrefix(run[i:], "${{") && quote != '\'' {
			end := strings.Index(run[i:], "}}")
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		ch := run[i]
		if ch == '\\' && quote != '\'' && i+1 < len(run) {
			i++
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ';' || ch == '|' || ch == '&' || ch == '\n' {
			segments = append(segments, run[start:i])
			if i+1 < len(run) && (ch == '|' || ch == '&') && run[i+1] == ch {
				i++
			}
			start = i + 1
		}
	}
	return append(segments, run[start:])
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
// including overlays not selected by the current corpus. An exemption must
// name the specific must-fail row that deliberately misspells a CLI word;
// no current workflow fixture needs an exemption.
func TestWorkflowFixturesUseCLIContract(t *testing.T) {
	t.Parallel()
	found := false
	err := fs.WalkDir(workflowQueueFixtureFS, "testdata/workflow_queue", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Base(path.Dir(name)) != fixtureWorkflowsDir || (path.Ext(name) != ".yml" && path.Ext(name) != ".yaml") {
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
