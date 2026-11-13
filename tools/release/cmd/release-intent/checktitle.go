package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// runCheckTitle implements the check-title command, with the PR title on
// stdin.
func runCheckTitle(flags flagValues) error {
	title, err := readTitleStdin()
	if err != nil {
		return err
	}
	ctx := context.Background()
	runner, err := newExecGit()
	if err != nil {
		return err
	}
	return checkTitle(ctx, runner, flags.Value(intent.FlagBase), flags.Value(intent.FlagHead), flags.Value(intent.FlagAuthor), flags.Value(intent.FlagHeadBranch), title)
}

// titleOnStdinHint is the one worked example of piping a title, built from
// the grammar's own formatter so it cannot drift from what the check accepts.
func titleOnStdinHint() string {
	example := intent.FormatTitle(intent.Title{Type: intent.TypeFeat, Scopes: []string{"x"}, Level: intent.LevelMinor, Subject: "..."})
	return "pipe the title, e.g. echo " + strconv.Quote(example) + " | " + strings.TrimPrefix(usageOf(intent.CommandCheckTitle), "usage: ")
}

func readTitleStdin() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("%s: cannot stat stdin for the PR title: %v. Fix: %s", intent.CommandCheckTitle, err, titleOnStdinHint())
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", usageErr("%s reads the PR title on stdin (got a terminal): %s", intent.CommandCheckTitle, titleOnStdinHint())
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			return t, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("%s: cannot read the PR title on stdin: %v", intent.CommandCheckTitle, err)
	}
	return "", usageErr("%s reads the PR title on stdin (got empty input): %s", intent.CommandCheckTitle, titleOnStdinHint())
}

func checkTitle(ctx context.Context, runner GitRunner, base, head, author, headBranch, title string) error {
	cfgData, err := runner.ReadFile(ctx, base, intent.DefaultConfigPath)
	adopting := false
	if err != nil {
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			return fmt.Errorf("%s: cannot read release config %q at base %q: %v. Confirm the base revision is readable; only a missing file means the adopting PR", intent.CommandCheckTitle, intent.DefaultConfigPath, base, err)
		}
		headData, herr := runner.ReadFile(ctx, head, intent.DefaultConfigPath)
		if herr != nil {
			return fmt.Errorf("%s: cannot read release config %q at base %q or head %q: base has no file; head read: %v. Confirm the head revision carries the config the adopting PR adds", intent.CommandCheckTitle, intent.DefaultConfigPath, base, head, herr)
		}
		cfgData = headData
		adopting = true
	}
	cfg, err := intent.ParseConfig(cfgData)
	if err != nil {
		return rejectAs(intent.CommandCheckTitle, err)
	}
	tags, err := runner.Tags(ctx, cfg.TagPrefix)
	if err != nil {
		return fmt.Errorf("%s: cannot list tags with prefix %q: %w. Confirm tags are fetched", intent.CommandCheckTitle, cfg.TagPrefix, err)
	}
	_, version, ok := intent.HighestFinalTag(tags, cfg.TagPrefix)
	var current intent.SemVer
	if ok {
		current = version
	}
	if _, perr := intent.ParseTitle(cfg, current, title); perr != nil {
		return rejectAs(intent.CommandCheckTitle, perr)
	}
	if !adopting {
		mergeBase, paths, err := runner.ChangedPaths(ctx, base, head)
		if err != nil {
			return fmt.Errorf("%s: cannot read the PR change between %q and %q: %w", intent.CommandCheckTitle, base, head, err)
		}
		changes, cerr := buildProtectedChanges(ctx, runner, cfg, mergeBase, head, paths)
		if cerr != nil {
			return cerr
		}
		kind := intent.ClassifyPR(cfg, author, headBranch)
		if cerr := intent.CheckProtectedPaths(cfg, changes, kind); cerr != nil {
			return rejectAs(intent.CommandCheckTitle, cerr)
		}
	}
	return emit(out(intent.OutputError, ""))
}

// buildProtectedChanges reads every touched version file at the merge base
// and at head: whether its protected field moved, and whether anything else
// in it moved. A corrupt file fails closed; any other blob read error exits 1
// and never becomes an accept or a stale verdict.
func buildProtectedChanges(ctx context.Context, runner GitRunner, cfg intent.Config, base, head string, paths []string) ([]intent.ProtectedChange, error) {
	changed := map[string]bool{}
	for _, p := range paths {
		changed[p] = true
	}
	var out []intent.ProtectedChange
	for p := range changed {
		out = append(out, intent.ProtectedChange{Path: p})
	}
	for _, vf := range cfg.VersionFiles {
		if !changed[vf.Path] {
			continue
		}
		baseData, baseVal, berr := readProtectedField(ctx, runner, base, vf.Path, vf.Field)
		if berr != nil {
			return nil, berr
		}
		headData, headVal, herr := readProtectedField(ctx, runner, head, vf.Path, vf.Field)
		if herr != nil {
			return nil, herr
		}
		other := otherContentChanged(baseData, headData, vf.Field, headVal)
		for i := range out {
			if out[i].Path == vf.Path {
				out[i].ProtectedFieldChanged = baseVal != headVal
				out[i].OtherContentChanged = other
			}
		}
	}
	return out, nil
}

// otherContentChanged reports whether a version file moved anywhere but its
// protected field: the base bytes with the head's field value written in by
// the release PR's own writer must equal the head bytes. A file added or
// removed, or a value the writer refuses, counts as other content.
func otherContentChanged(baseData, headData []byte, field, headVal string) bool {
	if len(baseData) == 0 || len(headData) == 0 {
		return true
	}
	rewritten, err := intent.WriteVersionField(baseData, field, headVal)
	if err != nil {
		return true
	}
	return !bytes.Equal(rewritten, headData)
}

// readProtectedField returns the version file's bytes and its protected
// field value. An absent file reads as empty. A blob read failure (other
// than absent) is an error for exit 1; a missing or non-string field is the
// protected-path rejection, with the contract reader's message naming the
// exact fault.
func readProtectedField(ctx context.Context, runner GitRunner, rev, path, field string) ([]byte, string, error) {
	data, err := runner.ReadFile(ctx, rev, path)
	if err != nil {
		var nf *NotFoundError
		if errors.As(err, &nf) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("%s: cannot read version file %q at %q: %v. Confirm the revision is readable; a failed fetch is never an accept", intent.CommandCheckTitle, path, rev, err)
	}
	val, ferr := versionFieldValue(data, field)
	if ferr != nil {
		return nil, "", rejectAs(intent.CommandCheckTitle, protectedFieldError(path, rev, ferr))
	}
	return data, val, nil
}

func protectedFieldError(path, rev string, cause error) error {
	return &intent.Error{
		Code:  intent.ErrProtectedPath,
		What:  "the protected field cannot be read from " + strconv.Quote(path) + " at " + strconv.Quote(rev) + ": " + firstLine(cause.Error()),
		Why:   "the protected field cannot be compared when the file does not carry it, so the PR fails closed",
		Where: "PR change, checked by " + intent.ProgramName + " " + intent.CommandCheckTitle.String(),
		When:  "pull_request opened, edited, synchronize, reopened, or the merge-group build",
		Means: "the title-guard check is red, so the merge queue will not accept this PR",
		Fix:   "restore the protected field (only the release PR may change it)",
	}
}
