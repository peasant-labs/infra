package main

import (
	"context"
	"fmt"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// runVerifyTag implements the verify-tag command.
func runVerifyTag(flags flagValues) error {
	runner, err := newExecGit()
	if err != nil {
		return err
	}
	return verifyTag(context.Background(), runner, flags.Value(intent.FlagTag), flags.Value(intent.FlagBranch))
}

// verifyTag accepts a release tag only when its name is the release prefix
// plus a version and the branch reaches its commit. The release config is
// read from the branch, never from the checkout: the checkout is the tag's
// own commit, so its config could name any prefix.
func verifyTag(ctx context.Context, runner GitRunner, tag, branch string) error {
	data, err := runner.ReadFile(ctx, branch, intent.DefaultConfigPath)
	if err != nil {
		return fmt.Errorf("%s: cannot read release config %q at %q: %v. Fetch the branch the tag must be on before verifying it", intent.CommandVerifyTag, intent.DefaultConfigPath, branch, err)
	}
	cfg, err := intent.ParseConfig(data)
	if err != nil {
		return rejectAs(intent.CommandVerifyTag, err)
	}
	if _, err := intent.VerifyTagName(cfg, tag); err != nil {
		return rejectAs(intent.CommandVerifyTag, err)
	}
	tags, err := runner.Tags(ctx, cfg.TagPrefix)
	if err != nil {
		return fmt.Errorf("%s: cannot list tags with prefix %q: %w. Fetch the tags before verifying one", intent.CommandVerifyTag, cfg.TagPrefix, err)
	}
	commit, ok := tags[tag]
	if !ok {
		return fmt.Errorf("%s: tag %q is not in this checkout: no tag is listed under refs/tags/%s. Fetch the tags (actions/checkout with fetch-tags: true and fetch-depth: 0) before verifying the tag", intent.CommandVerifyTag, tag, tag)
	}
	reachable, err := runner.IsAncestor(ctx, commit, branch)
	if err != nil {
		return fmt.Errorf("%s: %w", intent.CommandVerifyTag, err)
	}
	if err := intent.VerifyTag(cfg, intent.TagCheck{Tag: tag, Branch: branch, Reachable: reachable}); err != nil {
		return rejectAs(intent.CommandVerifyTag, err)
	}
	return emit(outBool(intent.OutputOK, true))
}
