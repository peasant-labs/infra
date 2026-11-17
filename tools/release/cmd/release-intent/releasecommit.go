package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// The preparation job uses a fixed local identity; it is not an App login or
// a credential. The PR itself is created separately by the workflow.
const (
	releaseCommitName  = "peasant-labs-releaser[bot]"
	releaseCommitEmail = "peasant-labs-releaser[bot]@users.noreply.github.com"
)

// releaseFile is both the source held open without following symlinks and the
// exact output that may replace it. Mode and blob come from the Git index.
type releaseFile struct {
	name, field, mode string
	file              *os.File
	before, after     []byte
	blob              string
	added             bool
}

// prepareGit is intentionally separate from the read-only GitRunner. Its
// callers construct only the fixed local Git operations below; it has no push,
// tag, checkout, shell, or user-supplied command entry point.
type prepareGit struct{ path string }

func (g prepareGit) run(ctx context.Context, input []byte, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, g.path, args...)
	cmd.Stdin = bytes.NewReader(input)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (g prepareGit) text(ctx context.Context, args ...string) (string, error) {
	out, err := g.run(ctx, nil, nil, args...)
	return strings.TrimSpace(string(out)), err
}

func runReleaseCommit(flags flagValues) error {
	// These variables redirect Git's repository, object store, index, refs or
	// config independently of the checked-out worktree. Do not let inherited
	// process state make a local preparation touch another repository or index.
	for _, key := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE", "GIT_EXEC_PATH"} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("release-pr --commit: inherited %s can redirect Git outside the checkout; unset it and retry in an isolated repository", key)
		}
	}
	ctx := context.Background()
	r, err := newExecGit()
	if err != nil {
		return err
	}
	g := prepareGit{path: r.(*execGit).gitPath}
	branch := flags.Value(intent.FlagBranch)
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("release-pr --commit: invalid source branch %q; pass a readable source commit or ref", branch)
	}
	tip, err := g.text(ctx, "rev-parse", "--verify", "--end-of-options", branch+"^{commit}")
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot resolve source branch %q: %w", branch, err)
	}

	// Validate the configured paths before Gather can pass the changelog as a
	// Git pathspec. A config from the requested source, not the worktree,
	// decides the output set.
	cfgBytes, err := r.ReadFile(ctx, tip, intent.DefaultConfigPath)
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot read source config at %q: %w", branch, err)
	}
	cfg, err := intent.ParseConfig(cfgBytes)
	if err != nil {
		return rejectAs(intent.CommandReleasePR, err)
	}
	if err := validateReleasePaths(cfg); err != nil {
		return err
	}
	bot := cfg.BotBranches[intent.PRKindRelease]
	botRef, err := intent.ParseGitRef(intent.BranchRef(bot).String())
	if bot == "" || err != nil {
		return fmt.Errorf("release-pr --commit: invalid or missing configured release bot branch %q; configure a valid automation branch", bot)
	}

	root, err := g.text(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot find repository root: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
	}
	if realCwd != realRoot {
		return fmt.Errorf("release-pr --commit: run from the repository root %q, not %q", root, cwd)
	}
	headRef, err := g.text(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil || headRef != botRef.String() {
		return fmt.Errorf("release-pr --commit: checked-out branch must be the configured release bot branch %q; check it out at the source tip first", botRef)
	}
	protected, err := releaseProtectedChanges(ctx, g, cfg)
	if err != nil {
		return err
	}
	if err := requireNoReferenceHook(ctx, g); err != nil {
		return err
	}

	facts, gatheredCfg, err := Gather(ctx, r, intent.CommandReleasePR, tip, intent.DefaultConfigPath, flags.Value(intent.FlagFloor), intent.ModeTag, intent.VerdictNone)
	if err != nil {
		return rejectAs(intent.CommandReleasePR, err)
	}
	if len(facts.Commits) == 0 {
		return fmt.Errorf("release-pr --commit: source branch %q has no commit", branch)
	}
	if facts.Commits[0].SHA != tip {
		return fmt.Errorf("release-pr --commit: source history does not start at pinned tip %s", tip)
	}
	head, err := g.text(ctx, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil || head != tip {
		return fmt.Errorf("release-pr --commit: bot branch HEAD must equal source tip %s; recreate the bot branch at the source tip before preparing content", tip)
	}
	if !reflect.DeepEqual(cfg, gatheredCfg) {
		return fmt.Errorf("release-pr --commit: source config changed while reading pinned tip %s; retry preparation", tip)
	}
	decision, err := intent.Plan(cfg, facts)
	if err != nil {
		return rejectAs(intent.CommandReleasePR, err)
	}
	content, err := releaseContent(ctx, r, cfg, facts, decision)
	if err != nil {
		return err
	}

	files, err := openReleaseFiles(ctx, g, r, cfg, content, tip)
	if err != nil {
		return err
	}
	var extraProtected []string
	for _, name := range protected {
		if !slices.Contains(versionPaths(cfg), name) {
			extraProtected = append(extraProtected, name)
		}
	}
	extra, err := openProtectedFiles(ctx, g, r, cfg, tip, extraProtected)
	if err != nil {
		for i := range files {
			_ = files[i].file.Close()
		}
		return err
	}
	files = append(files, extra...)
	defer func() {
		for i := range files {
			_ = files[i].file.Close()
		}
	}()

	var freezePlan *intent.FreezePlan
	if flags.Has(intent.FlagFreezePlan) {
		data, err := os.ReadFile(flags.Value(intent.FlagFreezePlan))
		if err != nil {
			return fmt.Errorf("release-pr --commit: cannot read freeze plan: %w", err)
		}
		plan, err := intent.ParseFreezePlan(data, cfg)
		if err != nil {
			return err
		}
		states := intent.SurfaceStates{Status: intent.SurfaceStateSchemaVersion}
		for _, s := range plan.Surfaces {
			state := intent.SurfaceClean
			if s.Freeze {
				state = intent.SurfaceAhead
			}
			states.Surfaces = append(states.Surfaces, intent.SurfaceState{Name: s.Name, Label: s.Label, Frozen: s.From, State: state})
		}
		computed, err := intent.PlanFreeze(cfg, states, decision.Full.Pending)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(plan, computed) {
			return fmt.Errorf("release-pr --commit: freeze plan does not match the landed titles; regenerate it from the current source range")
		}
		freezePlan = &plan
	}
	title, err := releaseCommitTitle(cfg, facts, decision, freezePlan)
	if err != nil {
		return err
	}
	output := func(push bool) error {
		lines := releasePRLines(push, decision)
		lines = append(lines[:len(lines)-1], out(intent.OutputSource, tip), out(intent.OutputTitle, title), out(intent.OutputError, ""))
		return emit(lines...)
	}
	push := true
	if flags.Has(intent.FlagCurrent) {
		push, err = releaseCommitNeedsPush(ctx, g, r, cfg, tip, flags.Value(intent.FlagCurrent), content, files)
		if err != nil {
			return err
		}
	}
	if !push {
		return output(false)
	}

	changed := make([]int, 0, len(files))
	changes := make([]intent.ProtectedChange, 0, len(files))
	for i := range files {
		f := &files[i]
		if !f.added && bytes.Equal(f.before, f.after) {
			continue
		}
		changed = append(changed, i)
		change := intent.ProtectedChange{Path: f.name}
		if f.field != "" {
			change.ProtectedFieldChanged = true
			change.OtherContentChanged = otherContentChanged(f.before, f.after, f.field, content.Versions[f.name])
		}
		changes = append(changes, change)
	}
	if len(changed) == 0 {
		return fmt.Errorf("release-pr --commit: push is required but generated output has no changes at source tip %s; refusing an empty release commit and bundle", tip)
	}
	// Landed candidates are identified by a changelog change. A version-only
	// commit would not be recognized as a release candidate by the planner.
	if !slices.ContainsFunc(changed, func(i int) bool { return files[i].name == cfg.Changelog }) {
		return fmt.Errorf("release-pr --commit: generated changelog has no change at source tip %s; a version-only commit cannot be recognized as a release candidate, so stop and repair the source before retrying", tip)
	}
	if err := intent.CheckProtectedPaths(cfg, changes, intent.PRKindRelease); err != nil {
		return rejectAs(intent.CommandReleasePR, err)
	}
	// Install only the already-computed bytes, then stage blobs directly. This
	// avoids pathspec expansion, clean filters, and commit hooks. The index is
	// inspected again before a commit object or branch ref is created.
	for _, i := range changed {
		f := &files[i]
		if _, err := f.file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := f.file.Truncate(0); err != nil {
			return err
		}
		if _, err := f.file.Write(f.after); err != nil {
			return fmt.Errorf("release-pr --commit: writing %q: %w; discard the incomplete worktree before retrying", f.name, err)
		}
		blob, err := g.run(ctx, f.after, nil, "hash-object", "--no-filters", "-w", "--stdin")
		if err != nil {
			return fmt.Errorf("release-pr --commit: writing blob for %q: %w; discard the incomplete worktree before retrying", f.name, err)
		}
		f.blob = strings.TrimSpace(string(blob))
	}
	var index bytes.Buffer
	for _, i := range changed {
		f := files[i]
		fmt.Fprintf(&index, "%s %s\t%s\x00", f.mode, f.blob, f.name)
	}
	if _, err := g.run(ctx, index.Bytes(), nil, "update-index", "-z", "--index-info"); err != nil {
		return fmt.Errorf("release-pr --commit: staging generated blobs: %w; discard the incomplete worktree before retrying", err)
	}
	if err := verifyStagedRelease(ctx, g, files, changed, tip); err != nil {
		return err
	}
	tree, err := g.text(ctx, "write-tree")
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot write validated tree: %w", err)
	}
	if err := verifyReleaseTree(ctx, g, files, changed, tip, tree); err != nil {
		return err
	}
	date, err := g.text(ctx, "show", "-s", "--format=%cI", "--end-of-options", tip)
	if err != nil || date == "" {
		return fmt.Errorf("release-pr --commit: cannot read source commit date for a reproducible commit: %v", err)
	}
	env := []string{"GIT_AUTHOR_NAME=" + releaseCommitName, "GIT_AUTHOR_EMAIL=" + releaseCommitEmail, "GIT_COMMITTER_NAME=" + releaseCommitName, "GIT_COMMITTER_EMAIL=" + releaseCommitEmail, "GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	commit, err := g.run(ctx, []byte(title+"\n"), env, "commit-tree", tree, "-p", tip)
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot create release commit: %w", err)
	}
	// Check the symbolic ref again and update only the expected old value. A
	// concurrent ref advance cannot be overwritten.
	headRef, err = g.text(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil || headRef != botRef.String() {
		return fmt.Errorf("release-pr --commit: checked-out branch changed during preparation; discard the incomplete worktree and retry")
	}
	if _, err := g.run(ctx, nil, nil, "update-ref", botRef.String(), strings.TrimSpace(string(commit)), tip); err != nil {
		return fmt.Errorf("release-pr --commit: source or bot branch moved during preparation: %w; discard the incomplete worktree and retry", err)
	}
	return output(true)
}

func validateReleasePaths(cfg intent.Config) error {
	paths := []string{cfg.Changelog}
	for _, vf := range cfg.VersionFiles {
		paths = append(paths, vf.Path)
	}
	seen := map[string]bool{}
	for _, name := range paths {
		if name == "" || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, ":*?[]\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return fmt.Errorf("release-pr --commit: unsafe configured output path %q; use a literal repository-relative file path without pathspec characters", name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".github") {
				return fmt.Errorf("release-pr --commit: unsafe configured output path %q; do not target Git metadata, workflow configuration, or traversing paths", name)
			}
		}
		if seen[name] {
			return fmt.Errorf("release-pr --commit: unsafe duplicate configured output path %q; each output must have one role", name)
		}
		seen[name] = true
	}
	if strings.EqualFold(path.Ext(cfg.Changelog), ".json") {
		return fmt.Errorf("release-pr --commit: unsafe changelog %q: a changelog cannot replace a JSON document", cfg.Changelog)
	}
	return nil
}

// releaseProtectedChanges accepts only unstaged modifications or new files
// explicitly covered by protected_paths. The index remains clean and the
// changelog remains exclusively owned by the generator.
func releaseProtectedChanges(ctx context.Context, g prepareGit, cfg intent.Config) ([]string, error) {
	entries, err := g.run(ctx, nil, nil, "ls-files", "-v", "-z")
	if err != nil {
		return nil, fmt.Errorf("release-pr --commit: cannot inspect index flags: %w", err)
	}
	for _, entry := range nulNames(entries) {
		if !strings.HasPrefix(entry, "H ") {
			return nil, fmt.Errorf("release-pr --commit: index entry %q can hide changes or is unmerged; use a clean full checkout without assume-unchanged or skip-worktree entries", entry)
		}
	}
	out, err := g.run(ctx, nil, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return nil, fmt.Errorf("release-pr --commit: cannot inspect working tree: %w", err)
	}
	var protected []string
	for _, entry := range nulNames(out) {
		if len(entry) < 4 || (entry[:3] != " M " && entry[:3] != "?? ") {
			return nil, fmt.Errorf("release-pr --commit: working tree and index must be clean except for unstaged regular protected files; unexpected status %q", entry)
		}
		name := entry[3:]
		if !safeProtectedOutput(name) || !intent.IsProtectedPath(cfg, name) || name == cfg.Changelog {
			return nil, fmt.Errorf("release-pr --commit: working tree and index must be clean except for protected paths; unexpected path %q", name)
		}
		protected = append(protected, name)
	}
	return protected, nil
}

func safeProtectedOutput(name string) bool {
	if name == "" || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, ":*?[]\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".github") {
			return false
		}
	}
	return true
}

func openProtectedFiles(ctx context.Context, g prepareGit, r GitRunner, cfg intent.Config, tip string, names []string) ([]releaseFile, error) {
	var files []releaseFile
	fail := func(err error) ([]releaseFile, error) {
		for _, f := range files {
			_ = f.file.Close()
		}
		return nil, err
	}
	for _, name := range names {
		if !intent.IsProtectedPath(cfg, name) || !safeProtectedOutput(name) {
			return fail(fmt.Errorf("release-pr --commit: unsafe protected path %q", name))
		}
		f, err := openConfined(name)
		if err != nil {
			return fail(fmt.Errorf("release-pr --commit: cannot open protected path %q without symlinks: %w", name, err))
		}
		row := releaseFile{name: name, file: f}
		files = append(files, row)
		rowPtr := &files[len(files)-1]
		rowPtr.after, err = io.ReadAll(f)
		if err != nil {
			return fail(err)
		}
		source, err := r.ReadFile(ctx, tip, name)
		var absent *NotFoundError
		switch {
		case err == nil:
			rowPtr.before = source
			rowPtr.mode, _, err = indexEntry(ctx, g, name)
			if err != nil {
				return fail(fmt.Errorf("release-pr --commit: protected file %q is not a tracked regular file: %w", name, err))
			}
			info, err := f.Stat()
			if err != nil || (info.Mode().Perm()&0o111 != 0) != (rowPtr.mode == "100755") {
				return fail(fmt.Errorf("release-pr --commit: protected file %q changed mode; restore the tracked mode", name))
			}
		case errors.As(err, &absent):
			info, statErr := f.Stat()
			if statErr != nil {
				return fail(statErr)
			}
			rowPtr.added = true
			rowPtr.mode = "100644"
			if info.Mode().Perm()&0o111 != 0 {
				rowPtr.mode = "100755"
			}
		default:
			return fail(fmt.Errorf("release-pr --commit: cannot read protected file %q at source: %w", name, err))
		}
	}
	return files, nil
}

// update-ref can execute a reference-transaction hook even though commit-tree
// does not execute commit hooks. This path never installs or changes hooks:
// if one is active, refuse before touching the index or worktree.
func requireNoReferenceHook(ctx context.Context, g prepareGit) error {
	hook, err := g.text(ctx, "rev-parse", "--git-path", "hooks/reference-transaction")
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot inspect Git reference hook: %w", err)
	}
	info, err := os.Stat(hook)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot inspect Git reference hook %q: %w", hook, err)
	}
	if info.Mode()&0o111 != 0 {
		return fmt.Errorf("release-pr --commit: an executable Git reference-transaction hook is active at %q; use a hook-free checkout for release preparation", hook)
	}
	return nil
}

// indexEntry reads one literal index entry and refuses symlinks, submodules,
// unmerged entries, missing files, and duplicate entries.
func indexEntry(ctx context.Context, g prepareGit, name string) (mode, blob string, err error) {
	out, err := g.run(ctx, nil, nil, "--literal-pathspecs", "ls-files", "--stage", "-z", "--", name)
	if err != nil {
		return "", "", err
	}
	entries := bytes.Split(out, []byte{0})
	if len(entries) != 2 || len(entries[1]) != 0 {
		return "", "", fmt.Errorf("%q must be one tracked regular file (not a symlink or missing source)", name)
	}
	header, got, ok := strings.Cut(string(entries[0]), "\t")
	fields := strings.Fields(header)
	if !ok || got != name || len(fields) != 3 || fields[2] != "0" || (fields[0] != "100644" && fields[0] != "100755") {
		return "", "", fmt.Errorf("%q must be one tracked regular file (not a symlink, submodule or conflict)", name)
	}
	return fields[0], fields[1], nil
}

func openReleaseFiles(ctx context.Context, g prepareGit, r GitRunner, cfg intent.Config, content intent.Content, tip string) ([]releaseFile, error) {
	files := []releaseFile{{name: cfg.Changelog, after: []byte(content.Changelog)}}
	for _, vf := range cfg.VersionFiles {
		files = append(files, releaseFile{name: vf.Path, field: vf.Field})
	}
	for i := range files {
		f := &files[i]
		var err error
		f.file, err = openConfined(f.name)
		if err != nil {
			for j := 0; j < i; j++ {
				_ = files[j].file.Close()
			}
			return nil, fmt.Errorf("release-pr --commit: cannot open source %q as a regular file without symlinks: %w; restore the tracked source file", f.name, err)
		}
		f.mode, _, err = indexEntry(ctx, g, f.name)
		var working []byte
		if err == nil {
			var info os.FileInfo
			info, err = f.file.Stat()
			if err == nil && (info.Mode().Perm()&0o111 != 0) != (f.mode == "100755") {
				err = fmt.Errorf("working file mode differs from source")
			}
		}
		if err == nil {
			working, err = io.ReadAll(f.file)
		}
		var source []byte
		if err == nil {
			source, err = r.ReadFile(ctx, tip, f.name)
		}
		if err == nil && !bytes.Equal(source, working) && !(f.field != "" && intent.IsProtectedPath(cfg, f.name)) {
			err = fmt.Errorf("working bytes differ from source commit")
		}
		f.before = source
		if err == nil && f.field != "" {
			var ok bool
			version, exists := content.Versions[f.name]
			ok = exists
			if !ok {
				err = fmt.Errorf("generator did not provide a version")
			} else {
				f.after, err = intent.WriteVersionField(working, f.field, version)
			}
		}
		if err != nil {
			for j := 0; j <= i; j++ {
				_ = files[j].file.Close()
			}
			return nil, fmt.Errorf("release-pr --commit: invalid source %q: %w; restore the tracked generated input before retrying", f.name, err)
		}
	}
	return files, nil
}

func releaseCommitTitle(cfg intent.Config, facts intent.Facts, decision intent.Decision, plan *intent.FreezePlan) (string, error) {
	title := intent.Title{Type: intent.TypeRelease, Subject: decision.Full.Tag}
	for _, pending := range decision.Full.Pending {
		if pending.Level > title.Level {
			title.Level = pending.Level
		}
		title.Breaking = title.Breaking || pending.Breaking
	}
	if plan != nil {
		for _, surface := range plan.Surfaces {
			if surface.Freeze {
				title.Scopes = append(title.Scopes, surface.Name)
				title.Subject += ", " + surface.Label + " " + surface.To.String()
			}
		}
	}
	text := intent.FormatTitle(title)
	_, current, ok := intent.HighestFinalTag(facts.Tags, cfg.TagPrefix)
	if !ok {
		return "", fmt.Errorf("release-pr --commit: no final version base for the generated title")
	}
	if _, err := intent.ParseTitle(cfg, current, text); err != nil {
		return "", fmt.Errorf("release-pr --commit: generated title %q is invalid: %w", text, err)
	}
	return text, nil
}

// releaseCommitNeedsPush accepts a no-op only for a readable, mergeable
// current branch with both fresh content and no changes outside release scope.
// A missing --current ref is an error: omit the flag for a new branch.
func releaseCommitNeedsPush(ctx context.Context, g prepareGit, r GitRunner, cfg intent.Config, tip, current string, content intent.Content, files []releaseFile) (bool, error) {
	if current == "" || strings.HasPrefix(current, "-") {
		return false, fmt.Errorf("release-pr --commit: invalid current branch %q; omit --current only when no branch exists", current)
	}
	currentOID, err := g.text(ctx, "rev-parse", "--verify", "--end-of-options", current+"^{commit}")
	if err != nil {
		return false, fmt.Errorf("release-pr --commit: cannot resolve current branch %q: %w; fetch it or omit --current for a new branch", current, err)
	}
	got, err := fileContent(func(name string) ([]byte, error) { return r.ReadFile(ctx, currentOID, name) }, cfg, content)
	if err != nil {
		var absent *NotFoundError
		if errors.As(err, &absent) {
			return true, nil
		}
		return false, fmt.Errorf("release-pr --commit: cannot inspect current branch content: %w", err)
	}
	if intent.CompareContent(content, got) != nil {
		return true, nil
	}
	if err := r.MergeTree(ctx, tip, currentOID); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return true, nil
		}
		return false, fmt.Errorf("release-pr --commit: cannot compare current branch with source: %w", err)
	}
	base, err := g.text(ctx, "merge-base", "--end-of-options", tip, currentOID)
	if err != nil {
		return false, fmt.Errorf("release-pr --commit: current branch has no readable merge base: %w", err)
	}
	out, err := g.run(ctx, nil, nil, "diff", "--no-ext-diff", "--no-renames", "--name-only", "-z", "--end-of-options", base, currentOID, "--")
	if err != nil {
		return false, fmt.Errorf("release-pr --commit: cannot inspect current branch changes: %w", err)
	}
	paths := nulNames(out)
	changes, err := buildProtectedChanges(ctx, r, cfg, base, currentOID, paths)
	if err != nil {
		return false, fmt.Errorf("release-pr --commit: cannot validate current branch changes: %w", err)
	}
	if intent.CheckProtectedPaths(cfg, changes, intent.PRKindRelease) != nil {
		return true, nil
	}
	// Compare the entire protected output set, including bytes and modes:
	// matching changelog/version fields alone says nothing about a freeze.
	desired := map[string]releaseFile{}
	for _, file := range files {
		if file.name != cfg.Changelog && intent.IsProtectedPath(cfg, file.name) && (file.added || !bytes.Equal(file.before, file.after)) {
			desired[file.name] = file
		}
	}
	seen := map[string]bool{}
	for _, name := range paths {
		if !intent.IsProtectedPath(cfg, name) || name == cfg.Changelog {
			continue
		}
		file, ok := desired[name]
		if !ok {
			return true, nil
		}
		got, err := r.ReadFile(ctx, currentOID, name)
		if err != nil {
			var absent *NotFoundError
			if errors.As(err, &absent) {
				return true, nil
			}
			return false, fmt.Errorf("release-pr --commit: cannot inspect current protected path %q: %w", name, err)
		}
		mode, err := treeMode(ctx, g, currentOID, name)
		if err != nil {
			return false, fmt.Errorf("release-pr --commit: cannot inspect current protected path mode %q: %w", name, err)
		}
		if mode != file.mode || !bytes.Equal(got, file.after) {
			return true, nil
		}
		seen[name] = true
	}
	if len(seen) != len(desired) {
		return true, nil
	}
	// Content comparison alone cannot see a mode change or symlink blob.
	for _, name := range append([]string{cfg.Changelog}, versionPaths(cfg)...) {
		baseMode, err := treeMode(ctx, g, base, name)
		if err != nil {
			return false, fmt.Errorf("release-pr --commit: cannot validate current branch base entry %q: %w", name, err)
		}
		currentMode, err := treeMode(ctx, g, currentOID, name)
		if err != nil {
			return false, fmt.Errorf("release-pr --commit: cannot validate current branch entry %q: %w", name, err)
		}
		if baseMode != currentMode {
			return true, nil
		}
	}
	return false, nil
}

func versionPaths(cfg intent.Config) []string {
	paths := make([]string, 0, len(cfg.VersionFiles))
	for _, vf := range cfg.VersionFiles {
		paths = append(paths, vf.Path)
	}
	return paths
}

func treeMode(ctx context.Context, g prepareGit, rev, name string) (string, error) {
	mode, _, err := treeEntry(ctx, g, rev, name)
	return mode, err
}

func treeEntry(ctx context.Context, g prepareGit, rev, name string) (string, string, error) {
	out, err := g.run(ctx, nil, nil, "--literal-pathspecs", "ls-tree", "-z", rev, "--", name)
	if err != nil {
		return "", "", err
	}
	entries := bytes.Split(out, []byte{0})
	if len(entries) != 2 || len(entries[1]) != 0 {
		return "", "", fmt.Errorf("missing or ambiguous tree entry %q", name)
	}
	header, got, ok := strings.Cut(string(entries[0]), "\t")
	fields := strings.Fields(header)
	if !ok || got != name || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return "", "", fmt.Errorf("tree entry %q is not a regular file", name)
	}
	return fields[0], fields[2], nil
}

// Verify the immutable tree object too, so an index mutation between the
// staged inspection and write-tree cannot enter the commit unnoticed.
func verifyReleaseTree(ctx context.Context, g prepareGit, files []releaseFile, changed []int, tip, tree string) error {
	out, err := g.run(ctx, nil, nil, "diff", "--no-ext-diff", "--no-renames", "--name-only", "-z", "--end-of-options", tip, tree, "--")
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot inspect generated tree: %w", err)
	}
	got := nulNames(out)
	want := make([]string, 0, len(changed))
	for _, i := range changed {
		want = append(want, files[i].name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("release-pr --commit: generated tree has unexpected paths; discard the incomplete worktree and retry")
	}
	for _, i := range changed {
		f := files[i]
		mode, blob, err := treeEntry(ctx, g, tree, f.name)
		if err != nil || mode != f.mode || blob != f.blob {
			return fmt.Errorf("release-pr --commit: tree content for %q is not the validated output; discard the incomplete worktree and retry", f.name)
		}
	}
	return nil
}

func nulNames(out []byte) []string {
	if len(out) == 0 {
		return nil
	}
	parts := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		names = append(names, string(p))
	}
	return names
}

// Verify both the entire staged path set and the exact mode/blob of every
// generated change. Also check that the worktree still refers to the opened
// regular files and that no unstaged or untracked content appeared.
func verifyStagedRelease(ctx context.Context, g prepareGit, files []releaseFile, changed []int, tip string) error {
	out, err := g.run(ctx, nil, nil, "diff", "--cached", "--no-ext-diff", "--no-renames", "--name-only", "-z", "--end-of-options", tip, "--")
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot inspect staged paths: %w", err)
	}
	got := nulNames(out)
	want := make([]string, 0, len(changed))
	for _, i := range changed {
		want = append(want, files[i].name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("release-pr --commit: staged paths %q differ from generated paths %q; discard the incomplete worktree and retry", got, want)
	}
	for _, i := range changed {
		f := files[i]
		mode, blob, err := indexEntry(ctx, g, f.name)
		if err != nil || mode != f.mode || blob != f.blob {
			return fmt.Errorf("release-pr --commit: staged bytes or mode for %q differ from the generated output; discard the incomplete worktree and retry", f.name)
		}
		now, err := openConfined(f.name)
		if err != nil {
			return fmt.Errorf("release-pr --commit: output %q became inaccessible or a symlink: %w", f.name, err)
		}
		originalInfo, e1 := f.file.Stat()
		currentInfo, e2 := now.Stat()
		data, e3 := io.ReadAll(now)
		_ = now.Close()
		if e1 != nil || e2 != nil || e3 != nil || !os.SameFile(originalInfo, currentInfo) || !bytes.Equal(data, f.after) {
			return fmt.Errorf("release-pr --commit: output %q changed during staging; discard the incomplete worktree and retry", f.name)
		}
	}
	// index-info deliberately creates entries without cached stat data. Status
	// refreshes that cache before judging worktree changes; diff-files alone
	// would report every such entry dirty even when the bytes match.
	status, err := g.run(ctx, nil, nil, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("release-pr --commit: cannot inspect working tree after staging: %w", err)
	}
	statusEntries := nulNames(status)
	wantStatus := make([]string, 0, len(want))
	for _, name := range want {
		status := "M  "
		for _, f := range files {
			if f.name == name && f.added {
				status = "A  "
				break
			}
		}
		wantStatus = append(wantStatus, status+name)
	}
	slices.Sort(statusEntries)
	slices.Sort(wantStatus)
	if !slices.Equal(statusEntries, wantStatus) {
		return fmt.Errorf("release-pr --commit: working tree changed during staging; discard the incomplete worktree and retry")
	}
	return nil
}
