package main

import (
	"context"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// runClassify implements the classify command: the PR kind of an author and
// head branch, judged by the release config at the given base revision.
func runClassify(flags flagValues) error {
	rev := flags.Value(intent.FlagRev)
	ctx := context.Background()
	runner, err := newExecGit()
	if err != nil {
		return err
	}
	cfgData, err := runner.ReadFile(ctx, rev, intent.DefaultConfigPath)
	if err != nil {
		return configReadError(intent.DefaultConfigPath, rev, err)
	}
	cfg, err := intent.ParseConfig(cfgData)
	if err != nil {
		return rejectAs(intent.CommandClassify, err)
	}
	kind := intent.ClassifyPR(cfg, flags.Value(intent.FlagAuthor), flags.Value(intent.FlagHeadBranch))
	return emit(out(intent.OutputKind, kind.String()))
}
