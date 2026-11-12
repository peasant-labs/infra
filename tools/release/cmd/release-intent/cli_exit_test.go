package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/policy"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

var testBinaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", intent.ProgramName+"-bin")
	if err != nil {
		os.Stderr.WriteString("TestMain: cannot create temp dir for the test binary: " + err.Error() + "\n")
		os.Exit(1)
	}
	bin := filepath.Join(dir, intent.ProgramName)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Stderr.WriteString("TestMain: cannot build the " + intent.ProgramName + " binary once for process tests: " + err.Error() + "\n" + string(out))
		os.RemoveAll(dir)
		os.Exit(1)
	}
	testBinaryPath = bin
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// processResult is what one run of the built binary produced.
type processResult struct {
	Exit   exitStatus
	Stdout string
	Stderr string
}

// runBinary runs the built binary in dir with a hermetic environment: no
// ambient step-output file or git variables, so a caller running under git
// cannot redirect the binary at the wrong repository. extraEnv adds to it.
func runBinary(t *testing.T, dir string, argv []string, stdin string, extraEnv []string) processResult {
	t.Helper()
	cmd := exec.Command(testBinaryPath, argv...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(hermeticGitEnv(t), extraEnv...)
	status := exitAccepted
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run binary %v: %v\nstderr: %s", argv, err, stderr.String())
		}
		status = exitStatus(ee.ExitCode())
	}
	if !isMemberOf(allExitStatuses, status) {
		t.Fatalf("run binary %v: exit status %d is outside the closed set %v\nstderr: %s", argv, status, allExitStatuses, stderr.String())
	}
	return processResult{Exit: status, Stdout: stdout.String(), Stderr: stderr.String()}
}

// processSetup names the state one process row runs in.
type processSetup string

const (
	// setupNone is an empty directory.
	setupNone processSetup = "none"
	// setupConfigValid holds one valid release config file.
	setupConfigValid processSetup = "config-valid"
	// setupConfigRaw holds the row's raw config bytes as a config file.
	setupConfigRaw processSetup = "config-raw"
	// setupQueue is a pending repository plus the row's queue response file.
	setupQueue processSetup = "queue"
	// setupGitPending is a released repository with one pending minor.
	setupGitPending processSetup = "git-pending"
	// setupGitRawSubject is a released repository whose tip carries the
	// row's raw subject.
	setupGitRawSubject processSetup = "git-raw-subject"
	// setupGitEmptyRelease is a released repository whose only later commit
	// is an empty release commit.
	setupGitEmptyRelease processSetup = "git-empty-release"
	// setupClassifyAllBots commits a config naming every bot branch.
	setupClassifyAllBots processSetup = "classify-all-bots"
	// setupClassifyRawConfig commits the row's raw config bytes.
	setupClassifyRawConfig processSetup = "classify-raw-config"
	// setupClassifyHeadTamper has a base config and a head config that maps
	// the release kind onto another branch.
	setupClassifyHeadTamper processSetup = "classify-head-tamper"
	// setupWorkflowsAccept is a queue policy and workflows that satisfy it.
	setupWorkflowsAccept processSetup = "workflows-accept"
	// setupWorkflowsDefaultPolicy is the accepted tree with the policy at
	// the policy package's default path and nowhere else.
	setupWorkflowsDefaultPolicy processSetup = "workflows-default-policy"
	// setupWorkflowsViolation is a policy an empty workflows directory
	// cannot satisfy.
	setupWorkflowsViolation processSetup = "workflows-violation"
	// setupWorkflowsPolicyOnly is a loadable policy and an empty workflows
	// directory.
	setupWorkflowsPolicyOnly processSetup = "workflows-policy-only"
	// The current-* setups hold an open release branch with freshly written
	// content; each moves one side. See setupCurrentBranch.
	setupCurrentSame     processSetup = "current-same"
	setupCurrentCleanTip processSetup = "current-clean-tip"
	setupCurrentDiffers  processSetup = "current-differs"
	setupCurrentConflict processSetup = "current-conflict"
)

var allProcessSetups = []processSetup{
	setupNone, setupConfigValid, setupConfigRaw, setupQueue,
	setupGitPending, setupGitRawSubject, setupGitEmptyRelease,
	setupClassifyAllBots, setupClassifyRawConfig, setupClassifyHeadTamper,
	setupWorkflowsAccept, setupWorkflowsDefaultPolicy, setupWorkflowsViolation, setupWorkflowsPolicyOnly,
	setupCurrentSame, setupCurrentCleanTip, setupCurrentDiffers, setupCurrentConflict,
}

func (s *processSetup) UnmarshalYAML(node *yaml.Node) error {
	member, err := decodeMember(node, allProcessSetups, "process setup")
	*s = member
	return err
}

// placeholder is a token a row's argv carries for a value only the setup
// knows: a temp path, a commit, or a name read from the repository's config.
type placeholder string

const (
	phDir               placeholder = "SETUP"
	phBase              placeholder = "SETUP_BASE"
	phHead              placeholder = "SETUP_HEAD"
	phRev               placeholder = "SETUP_REV"
	phConfig            placeholder = "SETUP_CONFIG"
	phQueue             placeholder = "SETUP_QUEUE"
	phPolicy            placeholder = "SETUP_POLICY"
	phWorkflows         placeholder = "SETUP_WORKFLOWS"
	phBotAuthor         placeholder = "SETUP_BOT_AUTHOR"
	phReleaseBranch     placeholder = "SETUP_RELEASE_BRANCH"
	phHeadReleaseBranch placeholder = "SETUP_HEAD_RELEASE_BRANCH"
)

var allPlaceholders = []placeholder{
	phDir, phBase, phHead, phRev, phConfig, phQueue, phPolicy, phWorkflows,
	phBotAuthor, phReleaseBranch, phHeadReleaseBranch,
}

// placeholderPattern finds every placeholder-shaped token in an argv word.
var placeholderPattern = regexp.MustCompile(`SETUP(?:_[A-Z]+)*`)

type cliInput struct {
	Argv []string `yaml:"argv"`
	// ArgvUnchecked marks a must-fail row whose argv is outside the command
	// line's closed sets on purpose. Every other row's command and flags are
	// parsed into their typed owners at load.
	ArgvUnchecked bool         `yaml:"argvUnchecked"`
	Stdin         string       `yaml:"stdin"`
	Setup         processSetup `yaml:"setup"`
	QueueFile     string       `yaml:"queueFile"`
	// GithubOutput runs the row with a step-output file and asserts the file
	// gains exactly the row's typed stdout lines.
	GithubOutput bool `yaml:"githubOutput"`
	// RawConfig and RawSubject are invalid on purpose; only must-fail rows
	// carry them.
	RawConfig  string `yaml:"rawConfig"`
	RawSubject string `yaml:"rawSubject"`
}

type cliExpected struct {
	Exit   wantExit   `yaml:"exit"`
	Stdout []wantLine `yaml:"stdout"`
	// BareStdout is exact stdout that is not key=value lines: the required
	// context list.
	BareStdout []string  `yaml:"bare_stdout"`
	AbsentKeys []wantKey `yaml:"absent_keys"`
	// StderrRule is the queue rule the diagnostics on stderr must name.
	StderrRule policy.QueueRule `yaml:"stderr_rule"`
}

type cliCase = testcase.Case[cliInput, cliExpected]

func loadCLIFixtures(t *testing.T) []cliCase {
	t.Helper()
	cases := loadCorpus[cliInput, cliExpected](t, "cli-exit.yaml")
	queueFiles := map[string]bool{}
	for _, c := range cases {
		mustFail := c.Classification == testcase.MustFail
		if (c.Input.ArgvUnchecked || c.Input.RawConfig != "" || c.Input.RawSubject != "") && !mustFail {
			t.Fatalf("cli-exit fixtures: %q carries raw or unchecked input but is not must-fail", c.Name)
		}
		if !c.Input.ArgvUnchecked {
			checkTypedArgv(t, c.Name, c.Input.Argv)
		}
		for _, word := range c.Input.Argv {
			for _, token := range placeholderPattern.FindAllString(word, -1) {
				if !isMemberOf(allPlaceholders, placeholder(token)) {
					t.Fatalf("cli-exit fixtures: %q uses unknown placeholder %q", c.Name, token)
				}
			}
		}
		if c.Expected.StderrRule != "" && !c.Expected.StderrRule.IsValid() {
			t.Fatalf("cli-exit fixtures: %q names unknown queue rule %q", c.Name, c.Expected.StderrRule)
		}
		if len(c.Expected.Stdout) > 0 && len(c.Expected.BareStdout) > 0 {
			t.Fatalf("cli-exit fixtures: %q expects both typed and bare stdout", c.Name)
		}
		if (c.Input.Setup == setupQueue) != (c.Input.QueueFile != "") {
			t.Fatalf("cli-exit fixtures: %q: a queue file goes with the %q setup and nothing else", c.Name, setupQueue)
		}
		if c.Input.QueueFile != "" {
			queueFiles[c.Input.QueueFile] = true
		}
	}
	// Exact membership: every embedded queue response is driven by a row,
	// and every row names an embedded response.
	entries, err := fs.ReadDir(queueJSONFS, queueFixtureDir)
	if err != nil {
		t.Fatalf("list embedded queue responses: %v", err)
	}
	embedded := map[string]bool{}
	for _, e := range entries {
		embedded[e.Name()] = true
		if !queueFiles[e.Name()] {
			t.Fatalf("cli-exit fixtures: queue response %q is driven by no row", e.Name())
		}
	}
	for name := range queueFiles {
		if !embedded[name] {
			t.Fatalf("cli-exit fixtures: a row names queue response %q, which is not embedded", name)
		}
	}
	return cases
}

// checkTypedArgv parses a row's command and flag words into the contract's
// closed sets, so a typo in a fixture fails at load and never reaches the
// binary as an accidental usage error.
func checkTypedArgv(t *testing.T, name string, argv []string) {
	t.Helper()
	if len(argv) == 0 {
		t.Fatalf("cli-exit fixtures: %q has an empty argv without argvUnchecked", name)
	}
	if _, err := intent.ParseCommand(argv[0]); err != nil {
		t.Fatalf("cli-exit fixtures: %q: %v", name, err)
	}
	for _, word := range argv[1:] {
		if !strings.HasPrefix(word, "--") {
			continue
		}
		if _, err := intent.ParseFlag(word); err != nil {
			t.Fatalf("cli-exit fixtures: %q: %v", name, err)
		}
	}
}

// processState is one prepared row: where the binary runs and what each
// placeholder stands for.
type processState struct {
	dir     string
	replace map[placeholder]string
}

func setupProcessCase(t *testing.T, in cliInput) processState {
	t.Helper()
	fix := loadRepoFixtures(t)
	standard := fix.Configs[repoStandard]
	switch in.Setup {
	case setupNone:
		dir := t.TempDir()
		return processState{dir, map[placeholder]string{phDir: dir}}
	case setupConfigValid, setupConfigRaw:
		dir := t.TempDir()
		// Any file name works: the command is told the path.
		path := filepath.Join(dir, "candidate-config.yaml")
		content := string(marshalConfig(t, standard))
		if in.Setup == setupConfigRaw {
			content = in.RawConfig
		}
		writeTreeFile(t, dir, filepath.Base(path), content)
		return processState{dir, map[placeholder]string{phDir: dir, phConfig: path}}
	case setupQueue:
		r := newPendingRepo(t, repoStandard)
		data, err := queueJSONFS.ReadFile(queueFixtureDir + "/" + in.QueueFile)
		if err != nil {
			t.Fatalf("read embedded queue file %q: %v", in.QueueFile, err)
		}
		queue := filepath.Join(t.TempDir(), in.QueueFile)
		if err := os.WriteFile(queue, data, 0o644); err != nil {
			t.Fatalf("write queue file: %v", err)
		}
		return processState{r.Dir(), map[placeholder]string{phDir: r.Dir(), phQueue: queue}}
	case setupGitPending:
		r := newPendingRepo(t, repoStandard)
		return processState{r.Dir(), map[placeholder]string{phDir: r.Dir(), phBase: r.Parent(), phHead: r.Head()}}
	case setupGitRawSubject:
		r := newReleasedRepo(t, repoStandard)
		r.WriteFile("work.txt", "try\n")
		r.CommitRaw(in.RawSubject)
		return processState{r.Dir(), map[placeholder]string{phDir: r.Dir()}}
	case setupGitEmptyRelease:
		r := newReleasedRepo(t, repoStandard)
		r.Commit(releaseTitled(intent.LevelPatch, "cut it"), empty())
		return processState{r.Dir(), map[placeholder]string{phDir: r.Dir()}}
	case setupClassifyAllBots:
		r := newSeededRepo(t, repoAllBots)
		return processState{r.Dir(), map[placeholder]string{
			phDir:           r.Dir(),
			phRev:           r.Head(),
			phBotAuthor:     r.Config().AutomationAuthor,
			phReleaseBranch: r.branchName(r.BotBranch(intent.PRKindRelease)),
		}}
	case setupClassifyRawConfig:
		r := newTestRepo(t, repoStandard)
		r.WriteRawConfig(in.RawConfig)
		r.Commit(titleSeed)
		return processState{r.Dir(), map[placeholder]string{phDir: r.Dir(), phRev: r.Head()}}
	case setupClassifyHeadTamper:
		r := newSeededRepo(t, repoStandard)
		base := r.Head()
		tampered := fix.Configs[repoReleaseOnWorkBranch]
		r.WriteConfig(tampered)
		head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "tamper"))
		return processState{r.Dir(), map[placeholder]string{
			phDir:               r.Dir(),
			phBase:              base,
			phHead:              head,
			phBotAuthor:         r.Config().AutomationAuthor,
			phHeadReleaseBranch: tampered.BotBranches[intent.PRKindRelease],
		}}
	case setupWorkflowsAccept, setupWorkflowsDefaultPolicy, setupWorkflowsViolation, setupWorkflowsPolicyOnly:
		return setupWorkflows(t, in.Setup, standard)
	case setupCurrentSame, setupCurrentCleanTip, setupCurrentDiffers, setupCurrentConflict:
		r := setupCurrentBranch(t, in.Setup)
		return processState{r.Dir(), map[placeholder]string{
			phDir:           r.Dir(),
			phReleaseBranch: r.branchName(r.BotBranch(intent.PRKindRelease)),
		}}
	default:
		t.Fatalf("process setup %q is in the closed set but has no builder", in.Setup)
		return processState{}
	}
}

// setupCurrentBranch builds a repository with an open release branch, named
// by the config's release bot branch, holding freshly written content: a
// released seed, one pending minor, content written and committed on the
// release branch while the trunk tip stays bare. The setup then moves one
// side: same leaves both; clean-tip lands a docs commit on the tip; differs
// lands another releasing commit on the tip; conflict edits one file outside
// the release content on both sides, so the content matches but the tip no
// longer merges onto the release branch cleanly.
func setupCurrentBranch(t *testing.T, setup processSetup) *testRepo {
	t.Helper()
	r := newPendingRepo(t, repoStandard)
	if result := r.Run(argvOf(intent.CommandReleasePR, intent.FlagBranch, headRev, intent.FlagWrite)...); result.Exit != exitAccepted {
		t.Fatalf("setup %s %s exit = %d, want %d\nstdout: %q\nstderr: %q", intent.CommandReleasePR, intent.FlagWrite, result.Exit, exitAccepted, result.Stdout, result.Stderr)
	}
	release := r.BotBranch(intent.PRKindRelease)
	r.CreateBranch(release)
	r.Commit(releaseTitled(intent.LevelMinor, "release content"))
	r.Checkout(r.Trunk())
	switch setup {
	case setupCurrentSame:
	case setupCurrentCleanTip:
		r.WriteFile("docs.txt", "words\n")
		r.Commit(titleDocs)
	case setupCurrentDiffers:
		r.WriteFile("later.txt", "later\n")
		r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "later facet"))
	case setupCurrentConflict:
		shared := titled(intent.TypeDocs, "shared", intent.LevelNone, "edit the shared file")
		r.Checkout(release)
		r.WriteFile("shared.txt", "release side\n")
		r.Commit(shared)
		r.Checkout(r.Trunk())
		r.WriteFile("shared.txt", "tip side\n")
		r.Commit(shared)
	default:
		t.Fatalf("setup %q is not an open-release-branch setup", setup)
	}
	return r
}

// workflowTree names one embedded check-workflows fixture tree.
type workflowTree string

const (
	// treeAccept is a queue policy and the workflows that satisfy it.
	treeAccept workflowTree = "accept"
	// treeViolation is a policy with two required contexts, listed out of
	// order to prove sorting, and no workflows.
	treeViolation workflowTree = "violation"
)

const (
	workflowTreesDir = fixtureDir + "/workflows"
	treePolicyFile   = "policy.yml"
	treeWorkflowsDir = "workflows"
)

// setupWorkflows writes a queue policy, a release config and a workflows
// directory. The release config is present because the queue rules resolve
// bot branches from it.
func setupWorkflows(t *testing.T, setup processSetup, cfg intent.Config) processState {
	t.Helper()
	dir := t.TempDir()
	writeTreeFile(t, dir, intent.DefaultConfigPath, string(marshalConfig(t, cfg)))
	workflows := filepath.Join(dir, treeWorkflowsDir)
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	tree, policyPath, withWorkflows := treeAccept, treePolicyFile, false
	switch setup {
	case setupWorkflowsAccept:
		withWorkflows = true
	case setupWorkflowsDefaultPolicy:
		policyPath, withWorkflows = policy.DefaultPath, true
	case setupWorkflowsViolation:
		tree = treeViolation
	case setupWorkflowsPolicyOnly:
	default:
		t.Fatalf("setup %q is not a workflows setup", setup)
	}
	root := workflowTreesDir + "/" + string(tree)
	policyBytes, err := workflowTreesFS.ReadFile(root + "/" + treePolicyFile)
	if err != nil {
		t.Fatalf("read embedded policy of tree %q: %v", tree, err)
	}
	writeTreeFile(t, dir, policyPath, string(policyBytes))
	if withWorkflows {
		entries, err := fs.ReadDir(workflowTreesFS, root+"/"+treeWorkflowsDir)
		if err != nil {
			t.Fatalf("list embedded workflows of tree %q: %v", tree, err)
		}
		for _, e := range entries {
			body, err := workflowTreesFS.ReadFile(root + "/" + treeWorkflowsDir + "/" + e.Name())
			if err != nil {
				t.Fatalf("read embedded workflow %q: %v", e.Name(), err)
			}
			writeTreeFile(t, workflows, e.Name(), string(body))
		}
	}
	return processState{dir, map[placeholder]string{
		phDir:       dir,
		phPolicy:    filepath.Join(dir, policyPath),
		phWorkflows: workflows,
	}}
}

// expand replaces every placeholder in a word, longest name first so one
// placeholder that prefixes another never wins.
func (s processState) expand(word string) string {
	ordered := make([]placeholder, 0, len(s.replace))
	for k := range s.replace {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, k := range ordered {
		word = strings.ReplaceAll(word, string(k), s.replace[k])
	}
	return word
}

// TestCLIExitCodes drives the built binary over the process corpus: the
// exit status, the exact stdout lines, the keys that must not print, the
// rule the diagnostics name, and what reaches the step-output file.
func TestCLIExitCodes(t *testing.T) {
	for _, c := range loadCLIFixtures(t) {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			state := setupProcessCase(t, c.Input)
			argv := make([]string, len(c.Input.Argv))
			for i, word := range c.Input.Argv {
				argv[i] = state.expand(word)
			}
			var extra []string
			var outputFile string
			// The file starts with one line from an earlier step: the
			// command must append after it and never truncate.
			earlier := outBool(intent.OutputOK, true).String() + "\n"
			if c.Input.GithubOutput {
				outputFile = filepath.Join(t.TempDir(), "step-output")
				if err := os.WriteFile(outputFile, []byte(earlier), 0o644); err != nil {
					t.Fatalf("seed the step-output file: %v", err)
				}
				extra = []string{githubOutputEnv + "=" + outputFile}
			}
			result := runBinary(t, state.dir, argv, c.Input.Stdin, extra)
			if result.Exit != c.Expected.Exit.Status {
				t.Fatalf("%s: exit = %d, want %d\nstdout: %q\nstderr: %q", c.Name, result.Exit, c.Expected.Exit.Status, result.Stdout, result.Stderr)
			}
			want := plainLines(c.Expected.Stdout)
			if len(c.Expected.BareStdout) > 0 {
				want = c.Expected.BareStdout
			}
			assertStdoutLines(t, c.Name, result.Stdout, want)
			assertKeysAbsent(t, c.Name, result.Stdout, plainKeys(c.Expected.AbsentKeys))
			if rule := c.Expected.StderrRule; rule != "" && !strings.Contains(result.Stderr, rule.String()) {
				t.Fatalf("%s: stderr %q does not name rule %q", c.Name, result.Stderr, rule)
			}
			if c.Input.GithubOutput {
				got, err := os.ReadFile(outputFile)
				if err != nil {
					t.Fatalf("read the step-output file: %v", err)
				}
				wantFile := earlier
				for _, line := range c.Expected.Stdout {
					wantFile += line.String() + "\n"
				}
				if string(got) != wantFile {
					t.Fatalf("%s: step-output file = %q, want %q (the typed stdout lines appended, nothing truncated, no bare lines)", c.Name, string(got), wantFile)
				}
			}
		})
	}
}
