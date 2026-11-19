package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/policy"
)

// inheritPollInterval is how often a pending base verdict is read again
// while --wait allows it. inheritMaxWait caps --wait: a merge group's base is
// at most the group ahead of it, whose run fits well inside it. Both are
// documented in the README.
const (
	inheritPollInterval = 30 * time.Second
	inheritMaxWait      = 30 * time.Minute
)

// fullSHA is a full 40-hex commit name: the API and the inherited_from output
// need the exact commit, never a ref that can move.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// inheritClock is the wait loop's view of time, injected for tests.
type inheritClock struct {
	now   func() time.Time
	sleep func(time.Duration)
}

// inheritReader is the base-verdict reader with a way to drop its cache
// between polls.
type inheritReader interface {
	intent.BaseVerdictReader
	forget()
}

// inheritGit is what the command reads from git: blobs at a revision and
// the tree difference between two commits.
type inheritGit interface {
	ReadFile(ctx context.Context, rev, path string) ([]byte, error)
	TreeDiffer
}

func runInheritVerdict(flags flagValues) error {
	wait := time.Duration(0)
	if flags.Has(intent.FlagWait) {
		d, err := time.ParseDuration(flags.Value(intent.FlagWait))
		if err != nil || d < 0 || d > inheritMaxWait {
			return usageErr("%s %s: %q is not a duration between 0s and %s, such as 20m. %s", intent.CommandInheritVerdict, intent.FlagWait, flags.Value(intent.FlagWait), inheritMaxWait, usageOf(intent.CommandInheritVerdict))
		}
		wait = d
	}
	runner, err := newExecGit()
	if err != nil {
		return err
	}
	reader, err := newGitHubReader(githubAPIBase(os.Getenv), flags.Value(intent.FlagRepo), githubToken(os.Getenv), &http.Client{Timeout: githubHTTPTimeout})
	if err != nil {
		return err
	}
	clock := inheritClock{now: time.Now, sleep: time.Sleep}
	return inheritVerdict(context.Background(), runner.(*execGit), reader, flags.Value(intent.FlagBase), flags.Value(intent.FlagHead), wait, clock)
}

// inheritVerdict classifies the candidate's whole tree change against its
// base and, when it is release content only, judges the base's merge-queue
// verdict. It prints release_only and reason first, always; inherit=true with the
// base and the verdict source only when the verdict is inherited.
//
// A change that is not release-only exits 0 with inherit=false: the
// candidate runs the full suite. A release-only change whose base verdict
// cannot be inherited exits 2: the gate fails closed and does not fall back.
// The config and queue policy are read at the base, so a candidate cannot
// widen what counts as release content.
func inheritVerdict(ctx context.Context, git inheritGit, reader inheritReader, base, head string, wait time.Duration, clock inheritClock) error {
	for _, rev := range []struct {
		flag  intent.Flag
		value string
	}{{intent.FlagBase, base}, {intent.FlagHead, head}} {
		if !fullSHA.MatchString(rev.value) {
			return usageErr("%s %s: %q is not a full 40-hex commit SHA. Pass the exact commit, for example the merge group's base_sha and head_sha", intent.CommandInheritVerdict, rev.flag, rev.value)
		}
	}
	cfgData, err := git.ReadFile(ctx, base, intent.DefaultConfigPath)
	if err != nil {
		return configReadError(intent.DefaultConfigPath, base, err)
	}
	cfg, err := intent.ParseConfig(cfgData)
	if err != nil {
		return rejectAs(intent.CommandInheritVerdict, err)
	}
	entries, err := git.TreeChanges(ctx, base, head)
	if err != nil {
		return err
	}
	changes := make([]intent.TreeChange, 0, len(entries))
	for _, e := range entries {
		change := intent.TreeChange{Path: e.Path, Status: e.Status, ModeChanged: e.OldMode != e.NewMode}
		// A symlink's blob is its target path, so a retargeted changelog or
		// version-file symlink is never release content.
		if e.Status == intent.TreeChangeModified && (!isRegularMode(e.OldMode) || !isRegularMode(e.NewMode)) {
			change.NotRegularFile = true
		}
		for _, vf := range cfg.VersionFiles {
			if vf.Path == e.Path && e.Status == intent.TreeChangeModified {
				change.OtherContentChanged = versionFileMoved(ctx, git, base, head, vf)
			}
		}
		changes = append(changes, change)
	}
	result := intent.ClassifyReleaseOnly(cfg, changes)
	if err := emit(outBool(intent.OutputReleaseOnly, result.ReleaseOnly), out(intent.OutputReason, result.Reason.String())); err != nil {
		return err
	}
	if !result.ReleaseOnly {
		if result.Path != "" {
			fmt.Fprintf(os.Stderr, "%s: %s at %s: the candidate runs the full suite\n", intent.CommandInheritVerdict, result.Reason, result.Path)
		}
		return emit(outBool(intent.OutputInherit, false))
	}
	rule, err := inheritRuleAt(ctx, git, base)
	if err != nil {
		return err
	}
	deadline := clock.now().Add(wait)
	for {
		source, err := intent.JudgeBaseVerdict(rule, base, reader)
		if err == nil {
			return emit(
				outBool(intent.OutputInherit, true),
				out(intent.OutputInheritedFrom, base),
				out(intent.OutputVerdictSource, source.String()))
		}
		var ie *intent.Error
		if !errors.As(err, &ie) {
			return fmt.Errorf("%s: %w", intent.CommandInheritVerdict, err)
		}
		if intent.IsPendingVerdict(ie) && clock.now().Add(inheritPollInterval).Before(deadline) {
			fmt.Fprintf(os.Stderr, "%s: %s; waiting %s before reading again\n", intent.CommandInheritVerdict, ie.What, inheritPollInterval)
			clock.sleep(inheritPollInterval)
			reader.forget()
			continue
		}
		return &RejectError{Command: intent.CommandInheritVerdict, Lines: []outputLine{outBool(intent.OutputInherit, false)}, Err: ie}
	}
}

// versionFileMoved reports whether a version file changed anywhere but its
// protected field. A side that cannot be read, or a field that cannot be
// compared, counts as moved: an unreadable object is never release content.
func versionFileMoved(ctx context.Context, git inheritGit, base, head string, vf intent.VersionFile) bool {
	baseData, err := git.ReadFile(ctx, base, vf.Path)
	if err != nil {
		return true
	}
	headData, err := git.ReadFile(ctx, head, vf.Path)
	if err != nil {
		return true
	}
	if _, err := versionFieldValue(baseData, vf.Field); err != nil {
		return true
	}
	headVal, err := versionFieldValue(headData, vf.Field)
	if err != nil {
		return true
	}
	return otherContentChanged(baseData, headData, vf.Field, headVal)
}

// inheritRuleAt resolves the inheritance rule from the queue policy and the
// aggregator's workflow as they are at the base: the base's run was built
// from that workflow, so its job names are the ones to read back.
func inheritRuleAt(ctx context.Context, git inheritGit, base string) (intent.InheritRule, error) {
	data, err := git.ReadFile(ctx, base, policy.DefaultPath)
	if err != nil {
		return intent.InheritRule{}, fmt.Errorf("%s: cannot read the queue policy %s at base %s: %w. The base must carry the policy that opts into inheritance", intent.CommandInheritVerdict, policy.DefaultPath, base, err)
	}
	p, err := policy.ParseQueuePolicy(policy.DefaultPath+" at "+base, data)
	if err != nil {
		return intent.InheritRule{}, fmt.Errorf("%s: %w", intent.CommandInheritVerdict, err)
	}
	path := policy.InheritWorkflowPath(p)
	if path == "" {
		_, err := policy.ResolveInheritRule(p, nil)
		return intent.InheritRule{}, fmt.Errorf("%s: at base %s: %w", intent.CommandInheritVerdict, base, err)
	}
	workflow, err := git.ReadFile(ctx, base, path)
	if err != nil {
		return intent.InheritRule{}, fmt.Errorf("%s: cannot read workflow %s at base %s: %w", intent.CommandInheritVerdict, path, base, err)
	}
	rule, err := policy.ResolveInheritRule(p, workflow)
	if err != nil {
		return intent.InheritRule{}, fmt.Errorf("%s: at base %s: %w", intent.CommandInheritVerdict, base, err)
	}
	return rule, nil
}
