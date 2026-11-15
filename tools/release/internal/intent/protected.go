package intent

import (
	"path"
	"strconv"
)

// ProtectedChange is one changed path and, for a version file, whether the
// protected field moved and whether anything else in the file moved. The
// command layer reads both ends of the change from git; this predicate only
// judges the answer.
type ProtectedChange struct {
	Path                  string `yaml:"path"`
	ProtectedFieldChanged bool   `yaml:"protected_field_changed"`
	OtherContentChanged   bool   `yaml:"other_content_changed"`
}

// CheckProtectedPaths judges a PR's changes by the kind the classifier gave
// it. The config always comes from the base revision, so a PR's own edit to
// the config cannot unprotect its path or widen its scope.
//
//   - The release PR is built by a job with no credential and lands
//     unreviewed, so it may change only what the release writes: the
//     changelog, and the protected field of each version file with nothing
//     else in that file.
//   - The cut-release PR is the one empty commit the button makes, so it may
//     change no path at all.
//   - Every other PR, any other bot PR included, may not change a protected
//     path or a protected version field.
func CheckProtectedPaths(cfg Config, changes []ProtectedChange, kind PRKind) error {
	switch kind {
	case PRKindRelease:
		return checkReleaseScope(cfg, changes)
	case PRKindCutRelease:
		if len(changes) > 0 {
			return scopeError(kind, changes[0].Path, "the cut-release PR is one empty commit whose title carries the level, so any changed path is content no one reviewed", "rebuild the cut-release branch as one empty commit on the default branch; the cut-release button does this")
		}
		return nil
	}
	for _, change := range changes {
		if change.Path == cfg.Changelog {
			return protectedError(change.Path, "the changelog is written only by the release PR")
		}
		for _, glob := range cfg.ProtectedPaths {
			if change.Path == glob {
				return protectedError(change.Path, "the path is listed in protected_paths, which no PR may change while it is listed (the release PR writes only the changelog and the version fields)")
			}
			if matched, err := path.Match(glob, change.Path); err == nil && matched {
				return protectedError(change.Path, "the path matches protected_paths entry "+strconv.Quote(glob)+", which no PR may change while it is listed (the release PR writes only the changelog and the version fields)")
			}
		}
		for _, vf := range cfg.VersionFiles {
			if change.Path == vf.Path && change.ProtectedFieldChanged {
				return protectedError(change.Path, "the protected field "+strconv.Quote(vf.Field)+" is written only by the release PR")
			}
		}
	}
	return nil
}

// checkReleaseScope holds the release PR to the changelog and the protected
// field of each version file.
func checkReleaseScope(cfg Config, changes []ProtectedChange) error {
	for _, change := range changes {
		if change.Path == cfg.Changelog {
			continue
		}
		versionFile := false
		for _, vf := range cfg.VersionFiles {
			if change.Path == vf.Path {
				versionFile = true
			}
		}
		switch {
		case !versionFile:
			return scopeError(PRKindRelease, change.Path, "the release PR writes only the changelog "+strconv.Quote(cfg.Changelog)+" and the protected field of each version file, and this path is neither", "rebuild the release branch with "+ProgramName+" "+CommandReleasePR.String()+" "+FlagWrite.String()+" alone; any other change belongs in an ordinary reviewed PR")
		case change.OtherContentChanged:
			return scopeError(PRKindRelease, change.Path, "the release PR may change only the protected field of a version file, and this change also moves other content in it", "rebuild the release branch with "+ProgramName+" "+CommandReleasePR.String()+" "+FlagWrite.String()+" alone; edit the rest of the version file in an ordinary reviewed PR")
		}
	}
	return nil
}

// scopeError builds the typed rejection of a bot PR change outside what that
// bot writes.
func scopeError(kind PRKind, changePath, why, fix string) *Error {
	return &Error{
		Code:  ErrReleaseContentScope,
		What:  "the " + kind.String() + " PR changes " + strconv.Quote(changePath) + ", which it may not change",
		Why:   why,
		Where: "PR change, judged by intent.CheckProtectedPaths against the base release config",
		When:  "checking a PR before it can be enqueued",
		Means: "the title-guard check is red, so the merge queue will not accept this unreviewed bot PR",
		Fix:   fix,
	}
}

// protectedError builds the typed protected-path rejection for one path.
func protectedError(changePath, why string) *Error {
	return &Error{
		Code:  ErrProtectedPath,
		What:  "protected path " + strconv.Quote(changePath) + " changed by a PR that may not change it",
		Why:   why,
		Where: "PR change, judged by intent.CheckProtectedPaths against the base release config",
		When:  "checking a PR before it can be enqueued",
		Means: "the title-guard check is red, so the merge queue will not accept this PR",
		Fix:   "drop the protected edit from the PR; the release PR writes the changelog and the version fields when the batch lands, and a path listed in protected_paths changes only after a reviewed config change unlists it",
	}
}
