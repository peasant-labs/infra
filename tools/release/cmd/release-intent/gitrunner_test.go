package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

type hostileArgInput struct {
	Arg string `yaml:"arg"`
}

type hostileArgExpected struct {
	Refusal string `yaml:"refusal"`
}

func loadHostileArgFixtures(t *testing.T) []testcase.Case[hostileArgInput, hostileArgExpected] {
	t.Helper()
	cases := loadCorpus[hostileArgInput, hostileArgExpected](t, "git-hostile-args.yaml")
	// Every hostile row must name the dash refusal: the guard it pins.
	for _, c := range cases {
		if c.Expected.Refusal == "" {
			t.Fatalf("hostile args fixture %q declares no refusal substring", c.Name)
		}
	}
	return cases
}

// The dash guard must fire before exec for every runner method, including
// the blob read. A real git path proves the refusal happens first: without
// the guard the call would reach git and fail as a blob error instead.
func TestGitRunner_RejectsLeadingDashArgs(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cfg := loadRepoFixtures(t).Configs[repoStandard]
	dir := t.TempDir()
	for _, c := range loadHostileArgFixtures(t) {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			g, err := newExecGitIn(dir)
			if err != nil {
				t.Fatalf("newExecGitIn: %v", err)
			}
			ctx := context.Background()
			calls := map[string]func() error{
				"ReadFile": func() error {
					_, err := g.ReadFile(ctx, c.Input.Arg, cfg.Changelog)
					return err
				},
				"ChangedPaths": func() error {
					_, _, err := g.ChangedPaths(ctx, c.Input.Arg, headRev)
					return err
				},
				"FirstParent": func() error {
					_, err := g.FirstParent(ctx, c.Input.Arg, cfg.Changelog)
					return err
				},
				"FirstCommitAdding": func() error {
					_, err := g.FirstCommitAdding(ctx, c.Input.Arg, intent.DefaultConfigPath)
					return err
				},
				"MergeTree": func() error {
					return g.MergeTree(ctx, c.Input.Arg, headRev)
				},
				"Tags": func() error {
					_, err := g.Tags(ctx, c.Input.Arg)
					return err
				},
			}
			for method, call := range calls {
				assertDashRefusal(t, method, call(), c.Input.Arg, c.Expected.Refusal)
			}
			// The structured command line itself refuses a leading-dash
			// path, on a route no runner method takes.
			_, err = g.(*execGit).run(ctx, listTreeEntry(headRev, c.Input.Arg))
			assertDashRefusal(t, "run", err, c.Input.Arg, c.Expected.Refusal)
		})
	}
}

func assertDashRefusal(t *testing.T, method string, err error, arg, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected refusal for hostile arg %q, got nil", method, arg)
	}
	var hostile *HostileArgError
	if !errors.As(err, &hostile) {
		t.Fatalf("%s: error for %q is not the typed leading-dash refusal: %q", method, arg, err.Error())
	}
	if !strings.HasPrefix(hostile.Arg, arg) {
		t.Fatalf("%s: refusal names %q, want the hostile arg %q", method, hostile.Arg, arg)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: refusal for %q does not say %q: %q", method, arg, want, err.Error())
	}
	var blob *BlobReadError
	if errors.As(err, &blob) {
		t.Fatalf("%s: hostile arg %q reached git and failed as a blob read; the dash guard must refuse first", method, arg)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("%s: hostile arg %q reached exec (got ExitError); the dash guard must refuse first", method, arg)
	}
}

// TestGitRunner_SubcommandAllowlist proves run refuses a subcommand off the
// allowlist before exec: the runner below has no executable to reach.
func TestGitRunner_SubcommandAllowlist(t *testing.T) {
	t.Parallel()
	g := &execGit{gitPath: filepath.Join("/nonexistent", "definitely-not-git")}
	for _, raw := range loadVocabularyFixture(t).Rejected.GitSubcommands {
		_, err := g.run(context.Background(), gitInvocation{sub: gitSubcommand(raw), revisions: []string{headRev}})
		if err == nil {
			t.Fatalf("expected refusal for off-allowlist subcommand %q, got nil", raw)
		}
		if !strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("allowlist refusal for %q is not actionable: %q", raw, err.Error())
		}
	}
}

func TestGitRunner_FirstParentListsEmptyCommits(t *testing.T) {
	t.Parallel()
	r := newPendingRepo(t, repoStandard)
	cut := releaseTitled(intent.LevelPatch, "empty cut")
	cut.PR = 3
	tip := r.Commit(cut, empty())
	commits, err := r.Runner().FirstParent(context.Background(), headRev, r.Config().Changelog)
	if err != nil {
		t.Fatalf("FirstParent: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("FirstParent listed %d commits, want the seed, the pending commit and the empty one (empty commits are not dropped)", len(commits))
	}
	if commits[0].SHA != tip || commits[0].Subject != intent.FormatTitle(cut) {
		t.Fatalf("newest commit = %+v, want the empty release commit %q", commits[0], intent.FormatTitle(cut))
	}
	if commits[0].ChangesChangelog {
		t.Fatalf("empty commit must not be marked as changing the changelog")
	}
	if !commits[2].ChangesChangelog {
		t.Fatalf("the seed commit adds the changelog and must be marked as changing it")
	}
}

func TestGitRunner_FirstCommitAddingFollowsFirstParent(t *testing.T) {
	t.Parallel()
	r := newTestRepo(t, repoStandard)
	r.WriteFile("work.txt", "base\n")
	r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "base work"))
	side := r.Branch("side")
	r.CreateBranch(side)
	r.WriteConfig(r.Config())
	r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "adopt config"))
	r.Checkout(r.Trunk())
	merge := r.Merge(side, titled(intent.TypeChore, "merge", intent.LevelNone, "merge the adopted config"))
	got, err := r.Runner().FirstCommitAdding(context.Background(), r.Trunk().String(), intent.DefaultConfigPath)
	if err != nil {
		t.Fatalf("FirstCommitAdding: %v", err)
	}
	if got != merge {
		t.Fatalf("FirstCommitAdding = %q, want the no-ff merge commit %q", got, merge)
	}
}

func TestGitRunner_TagsResolveAnnotatedAndLightweight(t *testing.T) {
	t.Parallel()
	r := newPendingRepo(t, repoStandard)
	seed, next := r.fix.SeedVersion, laterVersion(t, r, intent.LevelMinor)
	r.TagAnnotated(next)
	tags, err := r.Runner().Tags(context.Background(), r.Config().TagPrefix)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	// Assert the peeled commits, not the tag objects: an annotated tag's own
	// object id has the same shape as a commit id, so a shape check cannot
	// tell them apart. Ignoring the peeled value resolves the annotated tag
	// to its tag object, which is not on the first-parent chain.
	want := map[string]string{r.TagName(seed): r.Parent(), r.TagName(next): r.Head()}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("Tags = %v, want exactly %v", tags, want)
	}
	// The builder's tag refs resolve to the same commits.
	if got := r.Resolve(r.TagRef(next)); got != r.Head() {
		t.Fatalf("tag ref %q resolves to %q, want %q", r.TagRef(next), got, r.Head())
	}
}

func TestGitRunner_ChangedPathsSeesRenamedBothNames(t *testing.T) {
	t.Parallel()
	r := newSeededRepo(t, repoProtectedFile)
	protected := r.Config().ProtectedPaths[0]
	renamed := filepath.Join(filepath.Dir(protected), "renamed-"+filepath.Base(protected))
	base := r.Head()
	r.Rename(protected, renamed)
	head := r.Commit(titled(intent.TypeChore, "docs", intent.LevelNone, "rename the protected file"))
	_, paths, err := r.Runner().ChangedPaths(context.Background(), base, head)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	sort.Strings(paths)
	want := []string{protected, renamed}
	sort.Strings(want)
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("ChangedPaths = %v, want exactly %v: a renamed file surfaces as both names without rename detection", paths, want)
	}
}

func TestGitRunner_ChangedPathsSeesDeleted(t *testing.T) {
	t.Parallel()
	r := newSeededRepo(t, repoProtectedFile)
	protected := r.Config().ProtectedPaths[0]
	base := r.Head()
	r.Remove(protected)
	head := r.Commit(titled(intent.TypeChore, "docs", intent.LevelNone, "delete the protected file"))
	mergeBase, paths, err := r.Runner().ChangedPaths(context.Background(), base, head)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if mergeBase != base {
		t.Fatalf("merge base = %q, want the base %q", mergeBase, base)
	}
	if !reflect.DeepEqual(paths, []string{protected}) {
		t.Fatalf("ChangedPaths = %v, want exactly the deleted protected file %q", paths, protected)
	}
}

func TestGitRunner_ChangedPathsFailsWithoutMergeBase(t *testing.T) {
	t.Parallel()
	r := newPendingRepo(t, repoStandard)
	tip := r.Head()
	r.CreateOrphan(r.Branch("side"))
	r.WriteFile("other.txt", "unrelated\n")
	side := r.Commit(titled(intent.TypeChore, "root", intent.LevelNone, "unrelated root"))
	_, _, err := r.Runner().ChangedPaths(context.Background(), side, tip)
	if err == nil {
		t.Fatal("ChangedPaths with no merge base = nil, want an error naming the missing merge base")
	}
	if !strings.Contains(err.Error(), "no merge base") {
		t.Fatalf("ChangedPaths error %q does not name the missing merge base", err.Error())
	}
}

// TestGitRunner_ReadFileTellsAbsentFromUnreadable proves the two read
// failures stay distinct: a path absent at a readable revision is not found,
// and a revision that does not exist is a blob read error, never absent.
func TestGitRunner_ReadFileTellsAbsentFromUnreadable(t *testing.T) {
	t.Parallel()
	r := newTestRepo(t, repoStandard)
	r.WriteFile("work.txt", "base\n")
	head := r.Commit(titled(intent.TypeFeat, "graph", intent.LevelMinor, "base work"))
	runner := r.Runner()
	_, err := runner.ReadFile(context.Background(), head, intent.DefaultConfigPath)
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("reading a path absent at %q = %v, want NotFoundError", head, err)
	}
	_, err = runner.ReadFile(context.Background(), r.Branch("no-such-branch").String(), intent.DefaultConfigPath)
	var blob *BlobReadError
	if !errors.As(err, &blob) {
		t.Fatalf("reading at a revision that does not exist = %v, want BlobReadError", err)
	}
}
