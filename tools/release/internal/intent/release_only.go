package intent

import (
	"slices"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
)

// This file owns the first half of verdict inheritance: whether a
// candidate's whole tree change is release content only. base_verdict.go
// owns the second half, whether the base commit's merge-queue verdict may be
// inherited.
//
// Soundness is by induction over the default branch. A release-only candidate
// differs from its base only in the changelog and the protected version
// fields, which no gate reads as code, so the base's verdict covers it.

// TreeChangeStatus is the closed set of ways one path differs between two
// trees, as git's raw diff reports it with rename detection off.
type TreeChangeStatus string

const (
	TreeChangeModified    TreeChangeStatus = "modified"
	TreeChangeAdded       TreeChangeStatus = "added"
	TreeChangeDeleted     TreeChangeStatus = "deleted"
	TreeChangeTypeChanged TreeChangeStatus = "type-changed"
	// TreeChangeOther is any status git may print that is none of the
	// above (unmerged, unknown). It is never release content.
	TreeChangeOther TreeChangeStatus = "other"
)

// AllTreeChangeStatuses is the canonical closed set of change statuses.
var AllTreeChangeStatuses = []TreeChangeStatus{
	TreeChangeModified, TreeChangeAdded, TreeChangeDeleted, TreeChangeTypeChanged, TreeChangeOther,
}

func (s TreeChangeStatus) String() string { return string(s) }

// IsValid reports whether the status is one of the known members.
func (s TreeChangeStatus) IsValid() bool { return slices.Contains(AllTreeChangeStatuses, s) }

// UnmarshalText parses a status at a decoding boundary.
func (s *TreeChangeStatus) UnmarshalText(text []byte) error {
	member, err := closedset.Parse(AllTreeChangeStatuses, string(text), parsing("tree change status", "intent.TreeChangeStatus"))
	if err != nil {
		return err
	}
	*s = member
	return nil
}

// TreeChange is one path that differs between the base and head trees. The
// command layer reads it from git; OtherContentChanged is its comparison of a
// version file's bytes outside the protected field, true when either side
// could not be read.
//
// NotRegularFile is true when either side is not a regular file (a symlink,
// mode 120000, or a submodule): its bytes are a link target or a commit, not
// release content.
type TreeChange struct {
	Path                string           `yaml:"path"`
	Status              TreeChangeStatus `yaml:"status"`
	ModeChanged         bool             `yaml:"mode_changed"`
	NotRegularFile      bool             `yaml:"not_regular_file"`
	OtherContentChanged bool             `yaml:"other_content_changed"`
}

// ReleaseOnlyReason is the closed set of answers to "is this change release
// content only", printed as the reason step output.
type ReleaseOnlyReason string

const (
	ReasonReleaseOnly ReleaseOnlyReason = "release-only"
	// ReasonNoChange is an identical tree: nothing to inherit for, so the
	// candidate runs the full suite like any other change.
	ReasonNoChange ReleaseOnlyReason = "no-change"
	// ReasonChangelogUnchanged is a change that does not write the
	// changelog, which every release does.
	ReasonChangelogUnchanged ReleaseOnlyReason = "changelog-unchanged"
	// ReasonNotInPlace is an added, deleted, type-changed or otherwise not
	// in-place modified path.
	ReasonNotInPlace  ReleaseOnlyReason = "path-not-modified-in-place"
	ReasonModeChanged ReleaseOnlyReason = "mode-changed"
	// ReasonNotRegularFile is a symlink or submodule on either side.
	ReasonNotRegularFile ReleaseOnlyReason = "not-a-regular-file"
	// ReasonOutsideScope is a path that is neither the changelog nor a
	// version file.
	ReasonOutsideScope ReleaseOnlyReason = "path-outside-release-content"
	// ReasonVersionFileContent is a version file that changes more than its
	// protected field, or could not be compared.
	ReasonVersionFileContent ReleaseOnlyReason = "version-file-other-content"
)

// AllReleaseOnlyReasons is the canonical closed set of reasons.
var AllReleaseOnlyReasons = []ReleaseOnlyReason{
	ReasonReleaseOnly, ReasonNoChange, ReasonChangelogUnchanged, ReasonNotInPlace,
	ReasonModeChanged, ReasonNotRegularFile, ReasonOutsideScope, ReasonVersionFileContent,
}

func (r ReleaseOnlyReason) String() string { return string(r) }

// IsValid reports whether the reason is one of the known members.
func (r ReleaseOnlyReason) IsValid() bool { return slices.Contains(AllReleaseOnlyReasons, r) }

// UnmarshalText parses a reason at a decoding boundary.
func (r *ReleaseOnlyReason) UnmarshalText(text []byte) error {
	member, err := closedset.Parse(AllReleaseOnlyReasons, string(text), parsing("release-only reason", "intent.ReleaseOnlyReason"))
	if err != nil {
		return err
	}
	*r = member
	return nil
}

// ReleaseOnlyResult is the classification of a whole tree change. Path names
// the first path that decided a negative answer.
type ReleaseOnlyResult struct {
	ReleaseOnly bool              `yaml:"release_only"`
	Reason      ReleaseOnlyReason `yaml:"reason"`
	Path        string            `yaml:"path"`
}

// ClassifyReleaseOnly decides whether a tree change is release content only:
// in-place modifications of the changelog and of version files whose only
// difference is the protected field, with the changelog among them.
//
// It reuses the release PR's scope rule with protected_paths and
// freeze_outputs removed. Those sets authorize what the release bot may
// write, including spec freeze outputs the full suite must judge; they never
// widen what may skip it.
func ClassifyReleaseOnly(cfg Config, changes []TreeChange) ReleaseOnlyResult {
	if len(changes) == 0 {
		return ReleaseOnlyResult{Reason: ReasonNoChange}
	}
	content := cfg
	content.ProtectedPaths = nil
	content.FreezeOutputs = nil
	changelog := false
	for _, change := range changes {
		switch {
		case change.Status != TreeChangeModified:
			return ReleaseOnlyResult{Reason: ReasonNotInPlace, Path: change.Path}
		case change.NotRegularFile:
			return ReleaseOnlyResult{Reason: ReasonNotRegularFile, Path: change.Path}
		case change.ModeChanged:
			return ReleaseOnlyResult{Reason: ReasonModeChanged, Path: change.Path}
		}
		if change.Path == cfg.Changelog {
			changelog = true
		}
		scoped := ProtectedChange{Path: change.Path, OtherContentChanged: change.OtherContentChanged}
		if err := checkReleaseScope(content, []ProtectedChange{scoped}); err != nil {
			if slices.Contains(configVersionPaths(cfg), change.Path) {
				return ReleaseOnlyResult{Reason: ReasonVersionFileContent, Path: change.Path}
			}
			return ReleaseOnlyResult{Reason: ReasonOutsideScope, Path: change.Path}
		}
	}
	if !changelog {
		return ReleaseOnlyResult{Reason: ReasonChangelogUnchanged, Path: cfg.Changelog}
	}
	return ReleaseOnlyResult{ReleaseOnly: true, Reason: ReasonReleaseOnly}
}
