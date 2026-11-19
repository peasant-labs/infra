package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// rawTreeEntry is one line of git's raw diff between two trees.
type rawTreeEntry struct {
	Path    string
	OldMode string
	NewMode string
	Status  intent.TreeChangeStatus
}

// TreeDiffer reads the whole difference between two trees, not the
// merge-base range ChangedPaths reads: inheritance compares a candidate with
// its exact base.
type TreeDiffer interface {
	TreeChanges(ctx context.Context, base, head string) ([]rawTreeEntry, error)
}

// Regular file modes as git's raw diff prints them. Every other mode (a
// symlink is 120000, a submodule 160000, an absent side 000000) is not a
// regular file; an absent side is already an addition or deletion.
const (
	gitModeRegular    = "100644"
	gitModeExecutable = "100755"
)

// isRegularMode reports whether a raw diff mode is a regular file.
func isRegularMode(mode string) bool { return mode == gitModeRegular || mode == gitModeExecutable }

// TreeChanges diffs base against head as trees, with modes, statuses and no
// rename detection, so a rename is a deletion plus an addition.
func (g *execGit) TreeChanges(ctx context.Context, base, head string) ([]rawTreeEntry, error) {
	out, err := g.run(ctx, rawTreeDiff(base, head))
	if err != nil {
		return nil, fmt.Errorf("git runner: cannot diff the trees of %q and %q via `git diff --raw`: %w. Fetch both commits before running %s", base, head, err, intent.CommandInheritVerdict)
	}
	return parseRawDiff(string(out))
}

// parseRawDiff reads `git diff --raw -z` output: a header field
// ":<old mode> <new mode> <old object> <new object> <status>" then the path,
// each NUL-terminated.
func parseRawDiff(out string) ([]rawTreeEntry, error) {
	fields := strings.Split(out, "\x00")
	var entries []rawTreeEntry
	for i := 0; i < len(fields); i++ {
		header := fields[i]
		if header == "" && i == len(fields)-1 {
			break
		}
		parts := strings.Fields(strings.TrimPrefix(header, ":"))
		if !strings.HasPrefix(header, ":") || len(parts) != 5 || i+1 >= len(fields) {
			return nil, fmt.Errorf("git runner: unexpected raw diff record %q: expected ':<mode> <mode> <object> <object> <status>' followed by a path", header)
		}
		i++
		entries = append(entries, rawTreeEntry{Path: fields[i], OldMode: parts[0], NewMode: parts[1], Status: rawStatus(parts[4])})
	}
	return entries, nil
}

// rawStatus maps git's status letter to the closed status set. Anything git
// prints that is not an in-place modification, addition, deletion or type
// change is "other", which is never release content.
func rawStatus(letter string) intent.TreeChangeStatus {
	switch letter {
	case "M":
		return intent.TreeChangeModified
	case "A":
		return intent.TreeChangeAdded
	case "D":
		return intent.TreeChangeDeleted
	case "T":
		return intent.TreeChangeTypeChanged
	default:
		return intent.TreeChangeOther
	}
}
