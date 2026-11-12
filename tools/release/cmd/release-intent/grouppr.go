package main

import (
	"strconv"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// groupPRNumber extracts the PR number from a merge-group ref through the
// contract's ref parser. Any other text yields "" with no error: the PR-time
// check stays authoritative and a ref-format change must not block the queue.
func groupPRNumber(raw string) string {
	ref, err := intent.ParseGitRef(raw)
	if err != nil {
		return ""
	}
	group, ok := intent.ParseMergeQueueRef(ref)
	if !ok {
		return ""
	}
	return strconv.Itoa(group.PR)
}

// runGroupPR implements the group-pr command: the PR number a merge-group
// ref names, or an empty value for any other ref.
func runGroupPR(flags flagValues) error {
	return emit(out(intent.OutputPR, groupPRNumber(flags.Value(intent.FlagRef))))
}
