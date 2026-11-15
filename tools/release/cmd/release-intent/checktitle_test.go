package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	"gopkg.in/yaml.v3"
)

// titleSetup names the repository shape one check-title row runs on.
type titleSetup string

const (
	// titleSetupBaseReadFailure fails the base config read with a non-absent
	// error, through a scripted runner.
	titleSetupBaseReadFailure titleSetup = "base-read-failure"
	// titleSetupProtectionRemoved drops the version-file protection in the
	// head config and bumps the version.
	titleSetupProtectionRemoved titleSetup = "protection-removed"
	// titleSetupPRBehindBase lands a commit on the base after the PR branched.
	titleSetupPRBehindBase titleSetup = "pr-behind-base"
	// titleSetupScriptsOnlyAfterRelease lands a release on the base after
	// the PR branched; the PR edits only an ordinary field.
	titleSetupScriptsOnlyAfterRelease titleSetup = "scripts-only-after-release"
	// titleSetupVersionFieldEdit bumps the protected field in the head.
	titleSetupVersionFieldEdit titleSetup = "version-field-edit"
	// titleSetupVersionFieldRemoved drops the protected field in the head.
	titleSetupVersionFieldRemoved titleSetup = "version-field-removed"
	// titleSetupGroupShapedBase diffs two divergent queue entries.
	titleSetupGroupShapedBase titleSetup = "group-shaped-base"
	// titleSetupOrdinaryScriptsEdit edits only an ordinary field.
	titleSetupOrdinaryScriptsEdit titleSetup = "ordinary-scripts-edit"
	// titleSetupAdopting has no config at the base and adds it in the head.
	titleSetupAdopting titleSetup = "adopting"
	// titleSetupRawVersionDocument replaces the version file in the head
	// with the row's raw bytes.
	titleSetupRawVersionDocument titleSetup = "raw-version-document"
	// titleSetupReleaseContent writes a changelog section and bumps the
	// protected version field, which is all the release PR writes.
	titleSetupReleaseContent titleSetup = "release-content"
	// titleSetupReleaseSourceEdit writes the release content and also adds
	// a source file.
	titleSetupReleaseSourceEdit titleSetup = "release-source-edit"
	// titleSetupCutReleaseEmpty is the one empty commit of a cut-release.
	titleSetupCutReleaseEmpty titleSetup = "cut-release-empty"
	// titleSetupCutReleaseFileEdit is a cut-release commit that adds a file.
	titleSetupCutReleaseFileEdit titleSetup = "cut-release-file-edit"
)

var allTitleSetups = []titleSetup{
	titleSetupBaseReadFailure, titleSetupProtectionRemoved, titleSetupPRBehindBase,
	titleSetupScriptsOnlyAfterRelease, titleSetupVersionFieldEdit, titleSetupVersionFieldRemoved,
	titleSetupGroupShapedBase, titleSetupOrdinaryScriptsEdit, titleSetupAdopting,
	titleSetupRawVersionDocument, titleSetupReleaseContent, titleSetupReleaseSourceEdit,
	titleSetupCutReleaseEmpty, titleSetupCutReleaseFileEdit,
}

func (s *titleSetup) UnmarshalYAML(node *yaml.Node) error {
	member, err := decodeMember(node, allTitleSetups, "check-title setup")
	*s = member
	return err
}

type checkTitleInput struct {
	// Title is the PR title under test, in its wire spelling.
	Title string `yaml:"title"`
	// Author and HeadBranch may be placeholders for the config's bot author
	// and release bot branch.
	Author     string     `yaml:"author"`
	HeadBranch string     `yaml:"headBranch"`
	Setup      titleSetup `yaml:"setup"`
	// RawVersionDocument is corrupt on purpose; only a must-fail row carries it.
	RawVersionDocument string `yaml:"rawVersionDocument"`
}

type checkTitleExpected struct {
	Exit wantExit `yaml:"exit"`
	// Code is the typed rejection, empty for a row that is not rejected.
	Code       intent.ErrorCode `yaml:"code"`
	Message    string           `yaml:"message"`
	Stdout     []wantLine       `yaml:"stdout"`
	AbsentKeys []wantKey        `yaml:"absent_keys"`
}

type checkTitleCase = testcase.Case[checkTitleInput, checkTitleExpected]

func loadCheckTitleFixtures(t *testing.T) []checkTitleCase {
	t.Helper()
	cases := loadCorpus[checkTitleInput, checkTitleExpected](t, "check-title.yaml")
	for _, c := range cases {
		if c.Input.RawVersionDocument != "" && c.Classification != testcase.MustFail {
			t.Fatalf("check-title fixtures: %q carries a raw version document but is not must-fail", c.Name)
		}
		if (c.Input.Setup == titleSetupRawVersionDocument) != (c.Input.RawVersionDocument != "") {
			t.Fatalf("check-title fixtures: %q: a raw version document goes with the %q setup and nothing else", c.Name, titleSetupRawVersionDocument)
		}
		if (c.Expected.Code != "") != (c.Expected.Exit.Status == exitRejected) {
			t.Fatalf("check-title fixtures: %q: a rejection code goes with exit %d and nothing else", c.Name, exitRejected)
		}
	}
	return cases
}

// mockGitRunner returns programmed git results without touching git. It mocks
// the git dependency, never checkTitle: every method returns the scripted
// outcome for the scenario under test.
type mockGitRunner struct {
	readFile func(ctx context.Context, rev, path string) ([]byte, error)
}

func (m *mockGitRunner) unscripted(method string) error {
	return fmt.Errorf("mock runner: %s is not scripted for this scenario", method)
}

func (m *mockGitRunner) FirstParent(context.Context, string, string) ([]intent.Commit, error) {
	return nil, m.unscripted("FirstParent")
}
func (m *mockGitRunner) Tags(context.Context, string) (map[string]string, error) {
	return nil, m.unscripted("Tags")
}
func (m *mockGitRunner) FirstCommitAdding(context.Context, string, string) (string, error) {
	return "", m.unscripted("FirstCommitAdding")
}
func (m *mockGitRunner) ChangedPaths(context.Context, string, string) (string, []string, error) {
	return "", nil, m.unscripted("ChangedPaths")
}
func (m *mockGitRunner) ReadFile(ctx context.Context, rev, path string) ([]byte, error) {
	return m.readFile(ctx, rev, path)
}
func (m *mockGitRunner) IsAncestor(context.Context, string, string) (bool, error) {
	return false, m.unscripted("IsAncestor")
}
func (m *mockGitRunner) MergeTree(context.Context, string, string) error {
	return m.unscripted("MergeTree")
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe for stdout capture: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

// titleScene is one prepared check-title row: the two revisions, the runner,
// and the config that names the bot author and release branch.
type titleScene struct {
	base, head string
	runner     GitRunner
	cfg        intent.Config
}

func sceneOf(r *testRepo, base, head string) titleScene {
	return titleScene{base: base, head: head, runner: r.Runner(), cfg: r.Config()}
}

// laterVersion is the version one bump of the level above the seed.
func laterVersion(t *testing.T, r *testRepo, level intent.Level) intent.SemVer {
	t.Helper()
	next, err := intent.NextVersion(r.fix.SeedVersion, level, false)
	if err != nil {
		t.Fatalf("next version after %q at %s: %v", r.fix.SeedVersion, level, err)
	}
	return next
}

// quietLint is the ordinary, unprotected edit to a version file.
func quietLint(r *testRepo) {
	r.SetOrdinaryField(0, "scripts", map[string]string{"lint": "quiet"})
}

var titleQuietLint = titled(intent.TypeFix, "scripts", intent.LevelPatch, "quiet lint")

func buildTitleScene(t *testing.T, in checkTitleInput) titleScene {
	t.Helper()
	switch in.Setup {
	case titleSetupBaseReadFailure:
		// The base config blob read fails (a failed on-demand fetch, not an
		// absent file) while the head config reads fine. checkTitle must
		// fail closed: a base read error is never the adopting PR.
		cfg := loadRepoFixtures(t).Configs[repoStandard]
		const base, head = "base", "head"
		fetchErr := &BlobReadError{Rev: base, Path: intent.DefaultConfigPath, Err: fmt.Errorf("simulated on-demand fetch failure")}
		runner := &mockGitRunner{readFile: func(_ context.Context, rev, _ string) ([]byte, error) {
			if rev == base {
				return nil, fetchErr
			}
			return marshalConfig(t, cfg), nil
		}}
		return titleScene{base: base, head: head, runner: runner, cfg: cfg}
	case titleSetupOrdinaryScriptsEdit:
		r := newPendingRepo(t, repoStandard)
		quietLint(r)
		head := r.Commit(titleQuietLint)
		return sceneOf(r, r.Parent(), head)
	case titleSetupProtectionRemoved:
		r := newSeededRepo(t, repoStandard)
		base := r.Head()
		r.SetVersionField(0, laterVersion(t, r, intent.LevelMinor))
		r.WriteConfig(r.fix.Configs[repoUnprotected])
		head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "drop protection and bump"))
		return sceneOf(r, base, head)
	case titleSetupPRBehindBase:
		r := newSeededRepo(t, repoStandard)
		r.CreateBranch(r.Branch("feature"))
		r.WriteFile("feature.txt", "work\n")
		head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "branch work"))
		r.Checkout(r.Trunk())
		r.WriteFile("later.txt", "later\n")
		base := r.Commit(titleDocs)
		return sceneOf(r, base, head)
	case titleSetupScriptsOnlyAfterRelease:
		r := newReleasedRepo(t, repoStandard)
		r.CreateBranch(r.Branch("feature"))
		quietLint(r)
		head := r.Commit(titleQuietLint)
		r.Checkout(r.Trunk())
		next := laterVersion(t, r, intent.LevelMinor)
		r.AppendChangelog("\n## " + next.String() + "\n\n- release one (#1)\n")
		r.SetVersionField(0, next)
		release := releaseTitled(intent.LevelPatch, "publish "+r.TagName(next))
		release.PR = 1
		base := r.Commit(release)
		return sceneOf(r, base, head)
	case titleSetupVersionFieldEdit:
		r := newSeededRepo(t, repoStandard)
		base := r.Head()
		r.SetVersionField(0, laterVersion(t, r, intent.LevelPatch))
		head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelPatch, "bump"))
		return sceneOf(r, base, head)
	case titleSetupVersionFieldRemoved:
		r := newSeededRepo(t, repoStandard)
		base := r.Head()
		r.RemoveVersionField(0)
		head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "drop version field"))
		return sceneOf(r, base, head)
	case titleSetupRawVersionDocument:
		r := newPendingRepo(t, repoStandard)
		r.WriteVersionDocument(0, in.RawVersionDocument)
		head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "rewrite the version file"))
		return sceneOf(r, r.Parent(), head)
	case titleSetupGroupShapedBase:
		r := newSeededRepo(t, repoStandard)
		r.CreateBranch(r.Branch("queue-a"))
		r.WriteFile("a.txt", "a\n")
		base := r.Commit(titled(intent.TypeFeat, "a", intent.LevelMinor, "entry a"))
		r.Checkout(r.Trunk())
		r.CreateBranch(r.Branch("queue-b"))
		r.WriteFile("b.txt", "b\n")
		head := r.Commit(titled(intent.TypeFeat, "b", intent.LevelMinor, "entry b"))
		return sceneOf(r, base, head)
	case titleSetupReleaseContent, titleSetupReleaseSourceEdit, titleSetupCutReleaseEmpty, titleSetupCutReleaseFileEdit:
		r, base, head := buildBotPRRepo(t, in.Setup)
		return sceneOf(r, base, head)
	case titleSetupAdopting:
		// The adopting PR adds the config: the base commit has none and the
		// head adds it. The base is an ancestor of the head.
		r := newTestRepo(t, repoStandard)
		r.WriteFile("work.txt", "base\n")
		base := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "base work"))
		r.Seed()
		head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "adopt config"))
		return sceneOf(r, base, head)
	default:
		t.Fatalf("check-title setup %q is in the closed set but has no builder", in.Setup)
		return titleScene{}
	}
}

// buildBotPRRepo builds the bot PR shapes on a repository whose config names
// every bot branch, and returns it with the PR's base and head. The process
// corpus builds the same shapes through it.
func buildBotPRRepo(t *testing.T, setup titleSetup) (*testRepo, string, string) {
	t.Helper()
	r := newReleasedRepo(t, repoAllBots)
	base := r.Head()
	switch setup {
	case titleSetupReleaseContent, titleSetupReleaseSourceEdit:
		next := laterVersion(t, r, intent.LevelMinor)
		r.AppendChangelog("\n## " + next.String() + "\n\n- add facet (#1)\n")
		r.SetVersionField(0, next)
		if setup == titleSetupReleaseSourceEdit {
			r.WriteFile("src/graph.ts", "export const facet = 1\n")
		}
		return r, base, r.Commit(releaseTitled(intent.LevelMinor, "publish the pending changes"))
	case titleSetupCutReleaseEmpty:
		return r, base, r.Commit(releaseTitled(intent.LevelPatch, "cut a release"), empty())
	case titleSetupCutReleaseFileEdit:
		r.WriteFile("src/graph.ts", "export const facet = 1\n")
		return r, base, r.Commit(releaseTitled(intent.LevelPatch, "cut a release"))
	}
	t.Fatalf("setup %q is not a bot PR shape", setup)
	return nil, "", ""
}

// expandActor resolves the placeholders a row may use for the PR author and
// head branch from the scene's config.
func (s titleScene) expandActor(t *testing.T, word string) string {
	t.Helper()
	switch placeholder(word) {
	case phBotAuthor:
		return s.cfg.AutomationAuthor
	case phReleaseBranch:
		branch, ok := s.cfg.BotBranches[intent.PRKindRelease]
		if !ok {
			t.Fatalf("the scene's config names no release bot branch")
		}
		return branch
	case phCutReleaseBranch:
		branch, ok := s.cfg.BotBranches[intent.PRKindCutRelease]
		if !ok {
			t.Fatalf("the scene's config names no cut-release bot branch")
		}
		return branch
	}
	if placeholderPattern.MatchString(word) {
		t.Fatalf("check-title fixtures: %q is not a placeholder an author or head branch may use", word)
	}
	return word
}

// TestCheckTitleTempRepos drives checkTitle on the repositories the corpus
// names: one row per protected-path and base shape. An accept prints the
// empty error line; a rejection carries its typed code and prints nothing
// here, because the process boundary prints the error line.
func TestCheckTitleTempRepos(t *testing.T) {
	for _, c := range loadCheckTitleFixtures(t) {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			scene := buildTitleScene(t, c.Input)
			author := scene.expandActor(t, c.Input.Author)
			headBranch := scene.expandActor(t, c.Input.HeadBranch)
			var runErr error
			stdout := captureStdout(t, func() {
				runErr = checkTitle(context.Background(), scene.runner, scene.base, scene.head, author, headBranch, c.Input.Title)
			})
			assertStdoutLines(t, c.Name, stdout, plainLines(c.Expected.Stdout))
			assertKeysAbsent(t, c.Name, stdout, plainKeys(c.Expected.AbsentKeys))
			if status := statusOf(runErr); status != c.Expected.Exit.Status {
				t.Fatalf("%s: exit = %d, want %d (err %v)", c.Name, status, c.Expected.Exit.Status, runErr)
			}
			if c.Expected.Code != "" {
				rej, ok := asReject(runErr)
				if !ok {
					t.Fatalf("%s: expected rejection with code %q, got %v", c.Name, c.Expected.Code, runErr)
				}
				if rej.Err.Code != c.Expected.Code {
					t.Fatalf("%s: rejection code = %q, want %q", c.Name, rej.Err.Code, c.Expected.Code)
				}
				if rej.Command != intent.CommandCheckTitle {
					t.Fatalf("%s: rejection names command %q, want %q", c.Name, rej.Command, intent.CommandCheckTitle)
				}
			}
			if c.Expected.Message != "" && (runErr == nil || !strings.Contains(runErr.Error(), c.Expected.Message)) {
				t.Fatalf("%s: error %v must contain %q", c.Name, runErr, c.Expected.Message)
			}
		})
	}
}
