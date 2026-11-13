package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// GitRunner is the release-intent git seam: read-only history and file queries
// for release tooling. It shells out to `git` with a structurally-built argv,
// never a shell.
type GitRunner interface {
	// FirstParent returns first-parent commits from ref, newest first.
	FirstParent(ctx context.Context, ref string, changelog string) ([]intent.Commit, error)
	// Tags returns tag name to peeled commit for tags with the given prefix in
	// one git call.
	Tags(ctx context.Context, prefix string) (map[string]string, error)
	// FirstCommitAdding returns the oldest commit on branch's first-parent
	// history that adds path, or "" when no commit on that chain adds it.
	FirstCommitAdding(ctx context.Context, branch, path string) (string, error)
	// ChangedPaths diffs the merge base of base and head against head, with no
	// rename detection so a renamed protected file surfaces as both names. It
	// fails only when the two revisions share no merge base.
	ChangedPaths(ctx context.Context, base, head string) (mergeBase string, paths []string, err error)
	// ReadFile returns the bytes of path at rev, or a NotFoundError when the
	// path is absent at that revision.
	ReadFile(ctx context.Context, rev, path string) ([]byte, error)
	// MergeTree reports whether branch tip merges into current cleanly. A nil
	// error means the merge tree was written; a conflict or a read failure
	// returns an error.
	MergeTree(ctx context.Context, tip, current string) error
	// IsAncestor reports whether branch reaches commit. A read failure
	// returns an error, never false.
	IsAncestor(ctx context.Context, commit, branch string) (bool, error)
}

// gitSubcommand is the typed closed allowlist of git subcommands the runner
// may invoke. Every member only reads the repository. The zero value is no
// subcommand.
type gitSubcommand string

const (
	gitMergeBase  gitSubcommand = "merge-base"
	gitLog        gitSubcommand = "log"
	gitDiff       gitSubcommand = "diff"
	gitShow       gitSubcommand = "show"
	gitLsTree     gitSubcommand = "ls-tree"
	gitForEachRef gitSubcommand = "for-each-ref"
	gitMergeTree  gitSubcommand = "merge-tree"
)

// allGitSubcommands is the canonical closed set the runner enforces.
var allGitSubcommands = []gitSubcommand{
	gitMergeBase, gitLog, gitDiff, gitShow, gitLsTree, gitForEachRef, gitMergeTree,
}

func (s gitSubcommand) String() string { return string(s) }

// IsValid reports whether the subcommand is on the allowlist.
func (s gitSubcommand) IsValid() bool { return slices.Contains(allGitSubcommands, s) }

// gitOption is one fixed option the runner passes to git. Options are
// program text, never user input.
type gitOption string

const (
	// optEndOfOptions closes the option list, so nothing after it parses as
	// an option.
	optEndOfOptions gitOption = "--end-of-options"
	// optPathSeparator separates revisions from paths.
	optPathSeparator gitOption = "--"
	optFirstParent   gitOption = "--first-parent"
	optAddedOnly     gitOption = "--diff-filter=A"
	optNameOnly      gitOption = "--name-only"
	optNoRenames     gitOption = "--no-renames"
	optWriteTree     gitOption = "--write-tree"
	optIsAncestor    gitOption = "--is-ancestor"
)

// Output formats. Fields are NUL-separated: a subject cannot hold a NUL.
const (
	// logFormatCommit prints SHA, subject and committer date (YYYY-MM-DD).
	logFormatCommit = "%H%x00%s%x00%cs"
	// logFormatSHA prints the SHA alone.
	logFormatSHA = "%H"
	// refFormatTag prints the short tag name, the peeled commit of an
	// annotated tag (empty for a lightweight tag), and the object itself.
	refFormatTag = "%(refname:strip=2)%00%(*objectname)%00%(objectname)"
	// gitFieldSeparator is the NUL the formats above put between fields.
	gitFieldSeparator = "\x00"
	// revPathSeparator joins a revision to a path in git's <rev>:<path>
	// blob syntax.
	revPathSeparator = ":"
)

func formatOption(format string) gitOption { return gitOption("--format=" + format) }

// gitInvocation is one git command line in structured form. The query
// builders below are the only places one is assembled, so every command the
// runner can run is listed in this file. Revisions and paths are the dynamic
// parts; run refuses any of them that begins with '-'.
type gitInvocation struct {
	sub       gitSubcommand
	options   []gitOption
	revisions []string
	// separatePaths puts the path separator after the revisions, with or
	// without paths following it.
	separatePaths bool
	paths         []string
}

// argv renders the command line after the git executable: the subcommand,
// the fixed options, the end-of-options mark, the revisions, then the path
// separator and paths.
func (inv gitInvocation) argv() []string {
	argv := []string{inv.sub.String()}
	for _, o := range inv.options {
		argv = append(argv, string(o))
	}
	argv = append(argv, string(optEndOfOptions))
	argv = append(argv, inv.revisions...)
	if inv.separatePaths {
		argv = append(argv, string(optPathSeparator))
		argv = append(argv, inv.paths...)
	}
	return argv
}

// logFirstParent lists every first-parent commit of ref, newest first.
func logFirstParent(ref string) gitInvocation {
	return gitInvocation{sub: gitLog, options: []gitOption{optFirstParent, formatOption(logFormatCommit)}, revisions: []string{ref}}
}

// logFirstParentTouching lists the first-parent commits of ref that touch path.
func logFirstParentTouching(ref, path string) gitInvocation {
	return gitInvocation{sub: gitLog, options: []gitOption{optFirstParent, formatOption(logFormatSHA)}, revisions: []string{ref}, separatePaths: true, paths: []string{path}}
}

// logFirstParentAdding lists the first-parent commits of ref that add path.
func logFirstParentAdding(ref, path string) gitInvocation {
	return gitInvocation{sub: gitLog, options: []gitOption{optFirstParent, optAddedOnly, formatOption(logFormatSHA)}, revisions: []string{ref}, separatePaths: true, paths: []string{path}}
}

// tagsWithPrefix lists every tag the tag prefix opens.
func tagsWithPrefix(prefix string) gitInvocation {
	return gitInvocation{sub: gitForEachRef, options: []gitOption{formatOption(refFormatTag)}, revisions: []string{intent.TagRefGlob(prefix)}}
}

// mergeBaseOf finds the merge base of two revisions.
func mergeBaseOf(base, head string) gitInvocation {
	return gitInvocation{sub: gitMergeBase, revisions: []string{base, head}}
}

// changedNames lists the paths that differ between two revisions, with no
// rename detection.
func changedNames(from, to string) gitInvocation {
	return gitInvocation{sub: gitDiff, options: []gitOption{optNameOnly, optNoRenames}, revisions: []string{from, to}, separatePaths: true}
}

// isAncestorOf asks whether branch reaches commit; git answers with the exit
// status alone.
func isAncestorOf(commit, branch string) gitInvocation {
	return gitInvocation{sub: gitMergeBase, options: []gitOption{optIsAncestor}, revisions: []string{commit, branch}}
}

// mergeTreeOf computes the merged tree of two revisions without touching the
// working tree.
func mergeTreeOf(tip, current string) gitInvocation {
	return gitInvocation{sub: gitMergeTree, options: []gitOption{optWriteTree}, revisions: []string{tip, current}}
}

// showBlob prints the bytes of path at rev.
func showBlob(rev, path string) gitInvocation {
	return gitInvocation{sub: gitShow, revisions: []string{rev + revPathSeparator + path}}
}

// listTreeEntry lists path at rev: empty output means the path is absent.
func listTreeEntry(rev, path string) gitInvocation {
	return gitInvocation{sub: gitLsTree, revisions: []string{rev}, separatePaths: true, paths: []string{path}}
}

// execGit is the production GitRunner.
type execGit struct {
	gitPath string
	dir     string
}

// newExecGit resolves `git` on PATH once and returns the hardened runner.
func newExecGit() (GitRunner, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git runner: cannot find a `git` executable on PATH for %s history checks: %w. Install git or enter the nix dev shell (which provides it) before running %s git commands", intent.ProgramName, err, intent.ProgramName)
	}
	return &execGit{gitPath: gitPath}, nil
}

// newExecGitIn returns the runner rooted at dir so tests can point it at a
// throwaway repo without mutating the process-global cwd.
func newExecGitIn(dir string) (GitRunner, error) {
	r, err := newExecGit()
	if err != nil {
		return nil, err
	}
	g := r.(*execGit)
	g.dir = dir
	return g, nil
}

// HostileArgError is the single refusal for a dynamic git argument that
// begins with '-'. The value never reaches git, so it never executes as an
// option and never surfaces as a blob read error.
type HostileArgError struct {
	Arg string
	Sub gitSubcommand
}

func (e *HostileArgError) Error() string {
	return fmt.Sprintf("git runner: refusing to pass %q to `git %s`: a ref/tag/SHA/pattern/path that begins with '-' could be interpreted as a git option instead of a revision. Pass a value that does not start with '-'", e.Arg, e.Sub)
}

func dashRefusal(arg string, sub gitSubcommand) error {
	return &HostileArgError{Arg: arg, Sub: sub}
}

// run executes one structured git command line with no shell. It refuses a
// subcommand off the allowlist and any revision or path that begins with
// '-'. The end-of-options mark and the path separator are also on every
// command line, so a leading-dash value could not become an option even
// without the refusal.
func (g *execGit) run(ctx context.Context, inv gitInvocation) ([]byte, error) {
	if !inv.sub.IsValid() {
		return nil, fmt.Errorf("git runner: subcommand %q is not in the %s allowlist; refusing to run it. This is a programming error in %s, not a user input issue", inv.sub, intent.ProgramName, intent.ProgramName)
	}
	for _, group := range [][]string{inv.revisions, inv.paths} {
		for _, a := range group {
			if strings.HasPrefix(a, "-") {
				return nil, dashRefusal(a, inv.sub)
			}
		}
	}
	cmd := exec.CommandContext(ctx, g.gitPath, inv.argv()...)
	cmd.Dir = g.dir
	return cmd.Output()
}

// FirstParent lists every first-parent commit newest first. The changelog set
// comes from a second call so an empty commit is still listed: history
// simplification on the first call would drop it.
func (g *execGit) FirstParent(ctx context.Context, ref string, changelog string) ([]intent.Commit, error) {
	out, err := g.run(ctx, logFirstParent(ref))
	if err != nil {
		return nil, fmt.Errorf("git runner: cannot list first-parent commits of %q via `git log --first-parent`: %w. Confirm the ref exists in this checkout", ref, err)
	}
	commits := parseLogCommits(string(out))
	names, err := g.run(ctx, logFirstParentTouching(ref, changelog))
	if err != nil {
		return nil, fmt.Errorf("git runner: cannot list the commits touching %q via `git log --first-parent`: %w. Confirm the ref exists in this checkout", changelog, err)
	}
	touched := map[string]bool{}
	for _, line := range strings.Split(string(names), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			touched[t] = true
		}
	}
	for i := range commits {
		commits[i].ChangesChangelog = touched[commits[i].SHA]
	}
	return commits, nil
}

func parseLogCommits(out string) []intent.Commit {
	var commits []intent.Commit
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, gitFieldSeparator, 3)
		if len(parts) < 2 {
			continue
		}
		c := intent.Commit{SHA: strings.TrimSpace(parts[0]), Subject: parts[1]}
		if len(parts) == 3 {
			c.Date = strings.TrimSpace(parts[2])
		}
		commits = append(commits, c)
	}
	return commits
}

// Tags resolves every tag with the given prefix to its peeled commit in one
// git call. Annotated tags peel to their commit; lightweight tags resolve
// directly. A prefix that begins with '-' is refused before exec: it would
// otherwise become a hostile pattern argument.
func (g *execGit) Tags(ctx context.Context, prefix string) (map[string]string, error) {
	if strings.HasPrefix(prefix, "-") {
		return nil, dashRefusal(prefix, gitForEachRef)
	}
	out, err := g.run(ctx, tagsWithPrefix(prefix))
	if err != nil {
		return nil, fmt.Errorf("git runner: cannot list tags with prefix %q via `git for-each-ref`: %w. Confirm the checkout has tags fetched", prefix, err)
	}
	resolved := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, gitFieldSeparator)
		if len(parts) != 3 {
			continue
		}
		name, peeled, object := parts[0], strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if name == "" || object == "" {
			continue
		}
		if peeled != "" {
			resolved[name] = peeled
		} else {
			resolved[name] = object
		}
	}
	return resolved, nil
}

// FirstCommitAdding returns the oldest commit on branch's first-parent history
// that adds path, or "" when no commit on that chain adds it. With
// --first-parent, a merge commit that brings the file in counts as the adding
// commit.
func (g *execGit) FirstCommitAdding(ctx context.Context, branch, path string) (string, error) {
	out, err := g.run(ctx, logFirstParentAdding(branch, path))
	if err != nil {
		return "", fmt.Errorf("git runner: cannot find the commit adding %q on %q via `git log --first-parent --diff-filter=A`: %w", path, branch, err)
	}
	var last string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			last = t
		}
	}
	return last, nil
}

// ChangedPaths diffs the merge base of base and head against head with no
// rename detection. A renamed protected file appears as both names so either
// side is caught. A base that shares no history with head is an error: there
// is no merge base to diff.
func (g *execGit) ChangedPaths(ctx context.Context, base, head string) (string, []string, error) {
	out, err := g.run(ctx, mergeBaseOf(base, head))
	if err != nil {
		return "", nil, fmt.Errorf("git runner: base %q shares no history with head %q (no merge base via `git merge-base`): the PR range needs a merge base. Rebase the PR onto the base branch or pass revisions with shared history: %w", base, head, err)
	}
	mb := strings.TrimSpace(string(out))
	diff, err := g.run(ctx, changedNames(mb, head))
	if err != nil {
		return "", nil, fmt.Errorf("git runner: cannot diff %q against %q via `git diff --name-only --no-renames`: %w. Confirm both revisions exist", mb, head, err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(diff)), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			paths = append(paths, t)
		}
	}
	return mb, paths, nil
}

// MergeTree reports whether tip merges into current without conflict by
// computing the write tree. A nil error means the merge is clean.
func (g *execGit) MergeTree(ctx context.Context, tip, current string) error {
	if _, err := g.run(ctx, mergeTreeOf(tip, current)); err != nil {
		return fmt.Errorf("git runner: tip %q does not merge into %q cleanly (via `git merge-tree --write-tree`): %w. Rebuild the release branch from the tip", tip, current, err)
	}
	return nil
}

// gitAnswerNo is the exit status git merge-base --is-ancestor answers "no"
// with; any other non-zero status is a failure.
const gitAnswerNo = 1

// IsAncestor reports whether branch reaches commit, from the exit status of
// `git merge-base --is-ancestor`. Status 1 is "no"; any other failure, such
// as an unknown revision, is an error and never a "no".
func (g *execGit) IsAncestor(ctx context.Context, commit, branch string) (bool, error) {
	_, err := g.run(ctx, isAncestorOf(commit, branch))
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == gitAnswerNo {
		return false, nil
	}
	return false, fmt.Errorf("git runner: cannot tell whether %q reaches %q via `git merge-base --is-ancestor`: %w. Confirm both revisions are fetched", branch, commit, err)
}

// ReadFile returns path at rev. A path absent at rev yields NotFoundError;
// every other failure yields BlobReadError, so callers fail closed on fetch
// errors and never treat them as absent. A hostile rev is refused before any
// git call, never wrapped as a blob error.
func (g *execGit) ReadFile(ctx context.Context, rev, path string) ([]byte, error) {
	if strings.Contains(path, revPathSeparator) || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return nil, fmt.Errorf("git runner: refusing to read path %q at %q: paths must be repo-relative and must not escape via .. or absolute form", path, rev)
	}
	out, err := g.run(ctx, showBlob(rev, path))
	if err != nil {
		var hostile *HostileArgError
		if errors.As(err, &hostile) {
			return nil, err
		}
		if g.pathAbsent(ctx, rev, path) {
			return nil, &NotFoundError{Rev: rev, Path: path}
		}
		return nil, &BlobReadError{Rev: rev, Path: path, Err: err}
	}
	return out, nil
}

// pathAbsent reports whether path is absent at rev. It uses ls-tree so an
// absent path is structural (empty output, exit 0), never a message match.
func (g *execGit) pathAbsent(ctx context.Context, rev, path string) bool {
	out, err := g.run(ctx, listTreeEntry(rev, path))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == ""
}

// NotFoundError means the path is absent at the revision. Callers treat this
// as "no file here" (for example the adopting PR has no base config); every
// other read failure is a BlobReadError and must fail closed.
type NotFoundError struct {
	Rev  string
	Path string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("git runner: path %q is absent at %q: the revision has no such blob. A PR that adds the file legitimately reads this way; any other missing-blob read is a checkout problem", e.Path, e.Rev)
}

// BlobReadError means the blob could not be read (failed on-demand fetch in a
// blobless clone, unreadable object, or bad revision). It must fail closed,
// never be treated as absent.
type BlobReadError struct {
	Rev  string
	Path string
	Err  error
}

func (e *BlobReadError) Error() string {
	return fmt.Sprintf("git runner: cannot read %q at %q via `git show`: %v. In a blobless clone this can be a failed on-demand fetch; confirm the blob exists and the network allows the fetch", e.Path, e.Rev, e.Err)
}
