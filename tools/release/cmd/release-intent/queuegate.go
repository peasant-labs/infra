package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// runQueueGate implements the queue-gate command: the hold decision for the
// release PR, from the raw merge-queue response in a file.
func runQueueGate(flags flagValues) error {
	queuePath := flags.Value(intent.FlagQueue)
	rawPR := flags.Value(intent.FlagReleasePR)
	releasePR, err := strconv.Atoi(rawPR)
	if err != nil || releasePR <= 0 {
		return usageErr("%s (bad %s %q): pass %s", usageOf(intent.CommandQueueGate), intent.FlagReleasePR, rawPR, shapeOf(intent.FlagReleasePR).Meaning)
	}
	data, err := os.ReadFile(queuePath)
	if err != nil {
		return fmt.Errorf("%s: cannot read queue file %q: %v. The workflow must write the raw queue response to this path before calling %s", intent.CommandQueueGate, queuePath, err, intent.CommandQueueGate)
	}
	entries, err := intent.DecodeQueueResponse(data)
	if err != nil {
		return rejectAs(intent.CommandQueueGate, err)
	}
	cfg, err := loadConfig(intent.CommandQueueGate, intent.DefaultConfigPath)
	if err != nil {
		return err
	}
	current, err := queueCurrent(cfg)
	if err != nil {
		return err
	}
	gate, err := intent.QueueGate(cfg, current, entries, releasePR)
	if err != nil {
		return rejectAs(intent.CommandQueueGate, err)
	}
	return emit(out(intent.OutputGate, gate.String()))
}

// queueCurrent reads the current version from git tags for the hold rule. A
// read failure exits 1 with no gate line: nothing is armed or disarmed.
func queueCurrent(cfg intent.Config) (intent.SemVer, error) {
	ctx := context.Background()
	runner, err := newExecGit()
	if err != nil {
		return "", err
	}
	tags, err := runner.Tags(ctx, cfg.TagPrefix)
	if err != nil {
		return "", fmt.Errorf("%s: cannot list tags with prefix %q: %w", intent.CommandQueueGate, cfg.TagPrefix, err)
	}
	_, version, ok := intent.HighestFinalTag(tags, cfg.TagPrefix)
	if !ok {
		return "", fmt.Errorf("%s: no final release tag with prefix %q found for the hold rule. Confirm tags are fetched", intent.CommandQueueGate, cfg.TagPrefix)
	}
	return version, nil
}
