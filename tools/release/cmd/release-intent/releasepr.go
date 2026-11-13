package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// runReleasePR implements the release-pr command: write the release content
// into the working tree, or check the working tree against it.
func runReleasePR(flags flagValues) error {
	wantWrite, wantCheck := flags.Has(intent.FlagWrite), flags.Has(intent.FlagCheck)
	if wantWrite == wantCheck {
		return usageErr("%s (need exactly one of %s or %s): %s, %s",
			usageOf(intent.CommandReleasePR), intent.FlagWrite, intent.FlagCheck, shapeOf(intent.FlagWrite).Meaning, shapeOf(intent.FlagCheck).Meaning)
	}
	if flags.Has(intent.FlagCurrent) && !wantWrite {
		return usageErr("%s (%s takes no %s): %s only applies to %s", usageOf(intent.CommandReleasePR), intent.FlagCheck, intent.FlagCurrent, intent.FlagCurrent, intent.FlagWrite)
	}
	ctx := context.Background()
	runner, err := newExecGit()
	if err != nil {
		return err
	}
	// Release content is always the full pending set, never the
	// before-candidate set a verify would speak for. No pre-tag report is
	// involved, so the facts carry no verdict and the content renders from
	// Decision.Full.
	facts, cfg, err := Gather(ctx, runner, intent.CommandReleasePR, flags.Value(intent.FlagBranch), intent.DefaultConfigPath, flags.Value(intent.FlagFloor), intent.ModeTag, intent.VerdictNone)
	if err != nil {
		return rejectAs(intent.CommandReleasePR, err)
	}
	decision, err := intent.Plan(cfg, facts)
	if err != nil {
		return rejectAs(intent.CommandReleasePR, err)
	}
	content, err := releaseContent(ctx, runner, cfg, facts, decision)
	if err != nil {
		return err
	}
	if wantCheck {
		if cerr := checkWorkingTree(cfg, content); cerr != nil {
			rejected := rejectAsMode(intent.CommandReleasePR, intent.FlagCheck, cerr)
			if r, ok := asReject(rejected); ok && r.Err.Code == intent.ErrReleaseContentStale {
				r.Lines = []outputLine{outBool(intent.OutputFresh, false)}
			}
			return rejected
		}
		return emit(releasePRLines(false, decision)...)
	}
	push := true
	if flags.Has(intent.FlagCurrent) {
		if len(facts.Commits) == 0 {
			return fmt.Errorf("%s: cannot determine the branch tip for %s: the branch has no commits", intent.CommandReleasePR, intent.FlagCurrent)
		}
		push, err = needsPush(ctx, runner, cfg, facts.Commits[0].SHA, flags.Value(intent.FlagCurrent), content)
		if err != nil {
			return err
		}
	}
	if err := writeReleaseContent(cfg, content); err != nil {
		return err
	}
	return emit(releasePRLines(push, decision)...)
}

// releasePRLines is the accepted output of both release-pr modes: the tree
// holds the full set's content, whether the release branch needs a push, and
// the version that content carries.
func releasePRLines(push bool, decision intent.Decision) []outputLine {
	return []outputLine{
		outBool(intent.OutputFresh, true),
		outBool(intent.OutputPush, push),
		out(intent.OutputVersion, decision.Full.Version.String()),
		out(intent.OutputError, ""),
	}
}

// releaseContent renders the generator output for the decision's full pending
// set, whatever action the plan chose for the candidate. The base
// changelog is read at the range-base tag, or at the epoch when that is
// newer; the date is the planner's newest-pending date, so write, check and
// freshness render the same bytes on any day.
func releaseContent(ctx context.Context, runner GitRunner, cfg intent.Config, facts intent.Facts, decision intent.Decision) (intent.Content, error) {
	full := decision.Full
	if len(full.Pending) == 0 || full.Version == "" || full.Tag == "" {
		return intent.Content{}, fmt.Errorf("%s: nothing to write: no pending releasing subjects (version is empty). Open a releasing PR first", intent.CommandReleasePR)
	}
	shas := make([]string, 0, len(facts.Commits))
	for _, c := range facts.Commits {
		shas = append(shas, c.SHA)
	}
	baseRev := baseRevForRange(shas, facts.Tags, decision.RangeBase, facts.Epoch)
	base, err := runner.ReadFile(ctx, baseRev, cfg.Changelog)
	if err != nil {
		var nf *NotFoundError
		if errors.As(err, &nf) {
			base = nil
		} else {
			return intent.Content{}, fmt.Errorf("%s: cannot read changelog %q at base %q: %w. Confirm the revision is readable; a failed fetch is never written over", intent.CommandReleasePR, cfg.Changelog, baseRev, err)
		}
	}
	return intent.Generate(cfg, intent.ContentInput{Pending: full.Pending, BaseChangelog: string(base), Version: full.Version, Tag: full.Tag, Date: full.Date})
}

// checkWorkingTree compares the working tree with generator output. A nil
// error means fresh. A typed stale-content rejection means stale. Any other
// error is a failed read for exit 1.
func checkWorkingTree(cfg intent.Config, content intent.Content) error {
	got, err := fileContent(os.ReadFile, cfg, content)
	if err != nil {
		return fmt.Errorf("%s %s: cannot assemble the working-tree content for comparison: %w. Run from the repository root with the release PR checked out", intent.CommandReleasePR, intent.FlagCheck, err)
	}
	return intent.CompareContent(content, got)
}

// needsPush reports whether the open release branch needs an update: content
// differences rebuild, and a release branch the tip no longer merges onto
// cleanly rebuilds even when the content matches.
func needsPush(ctx context.Context, runner GitRunner, cfg intent.Config, tip, current string, content intent.Content) (bool, error) {
	got, err := fileContent(func(path string) ([]byte, error) {
		return runner.ReadFile(ctx, current, path)
	}, cfg, content)
	if err != nil {
		return true, nil
	}
	if err := intent.CompareContent(content, got); err != nil {
		return true, nil
	}
	if err := runner.MergeTree(ctx, tip, current); err != nil {
		return true, nil
	}
	return false, nil
}

// writeReleaseContent writes generator output into the working tree: the
// protected field of every version file is spliced in place so key order,
// whitespace and escaping everywhere else stay byte-identical, and the full
// changelog text is written whole.
func writeReleaseContent(cfg intent.Config, content intent.Content) error {
	for _, vf := range cfg.VersionFiles {
		want, ok := content.Versions[vf.Path]
		if !ok {
			continue
		}
		data, err := os.ReadFile(vf.Path)
		if err != nil {
			return fmt.Errorf("%s %s: cannot read %q: %w. Run from the repository root", intent.CommandReleasePR, intent.FlagWrite, vf.Path, err)
		}
		out, err := intent.WriteVersionField(data, vf.Field, want)
		if err != nil {
			return fmt.Errorf("%s %s: cannot write %q: %w. Run from the repository root", intent.CommandReleasePR, intent.FlagWrite, vf.Path, err)
		}
		if err := os.WriteFile(vf.Path, out, 0o644); err != nil {
			return fmt.Errorf("%s %s: cannot write %q: %w", intent.CommandReleasePR, intent.FlagWrite, vf.Path, err)
		}
	}
	if err := os.WriteFile(cfg.Changelog, []byte(content.Changelog), 0o644); err != nil {
		return fmt.Errorf("%s %s: cannot write %q: %w", intent.CommandReleasePR, intent.FlagWrite, cfg.Changelog, err)
	}
	return nil
}
