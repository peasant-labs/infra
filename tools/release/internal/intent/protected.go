package intent

import (
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CheckChangelogTitle prevents releasing subjects from being hidden by a
// changelog edit, which the release planner excludes from pending subjects.
func CheckChangelogTitle(title Title, kind PRKind, touchesChangelog bool) error {
	if !touchesChangelog || kind == PRKindRelease || (title.Level == LevelNone && title.Type != TypeRelease) {
		return nil
	}
	return &Error{
		Code:  ErrChangelogRelease,
		What:  "a non-release PR changes the changelog and carries a release title or level",
		Why:   "changelog-changing commits are excluded from release subjects, so this release would be lost",
		Where: "PR change, checked by release-intent check-title",
		When:  "checking the pull request or merge group",
		Means: "the title guard rejects this PR before it can be merged",
		Fix:   "retitle as docs(changelog): ... with no level, or move the changelog edit to its own PR",
	}
}

// ProtectedChange is one changed path with the comparisons applicable to a
// version file or changelog. The command layer reads both ends of the change
// from git; this predicate only judges the answer. An unset comparison is
// never proof that the release headings are unchanged.
type ProtectedChange struct {
	Path                       string `yaml:"path"`
	ProtectedFieldChanged      bool   `yaml:"protected_field_changed"`
	OtherContentChanged        bool   `yaml:"other_content_changed"`
	ChangelogHeadingsUnchanged bool   `yaml:"changelog_headings_unchanged"`
}

// CheckProtectedPaths judges a PR's changes by the kind the classifier gave
// it. The config always comes from the base revision, so a PR's own edit to
// the config cannot unprotect its path or widen its scope.
//
//   - The release PR is built by a job with no credential and lands
//     unreviewed, so it may change only what the release writes: the
//     changelog, protected paths, configured freeze outputs, and the protected
//     field of each version file with nothing else in that file. The title
//     guard cannot see the plan; commit preparation enforces the plan gate.
//   - The cut-release PR is the one empty commit the button makes, so it may
//     change no path at all.
//   - Every other PR, any other bot PR included, may edit the changelog body
//     but not release headings, a protected path or a protected version field.
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
		if change.Path == cfg.Changelog && !change.ChangelogHeadingsUnchanged {
			return protectedError(change.Path, "release section headings are written only by the release PR")
		}
		for _, glob := range cfg.ProtectedPaths {
			if change.Path == glob {
				return protectedError(change.Path, "the path is listed in protected_paths, which only the release PR may change")
			}
			if matched, err := path.Match(glob, change.Path); err == nil && matched {
				return protectedError(change.Path, "the path matches protected_paths entry "+strconv.Quote(glob)+", which only the release PR may change")
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

// checkReleaseScope holds the release PR to the changelog, protected paths,
// freeze outputs, and the protected field of each version file.
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
		case versionFile && change.OtherContentChanged:
			return scopeError(PRKindRelease, change.Path, "the release PR may change only the protected field of a version file, and this change also moves other content in it", "rebuild the release branch with "+ProgramName+" "+CommandReleasePR.String()+" "+FlagWrite.String()+" alone; edit the rest of the version file in an ordinary reviewed PR")
		case matchesProtectedPath(cfg, change.Path):
			continue
		case !versionFile && !IsFreezeOutput(cfg, change.Path):
			return scopeError(PRKindRelease, change.Path, "the release PR writes only the changelog "+strconv.Quote(cfg.Changelog)+", protected paths, freeze outputs and the protected field of each version file, and this path is none of those", "rebuild the release branch with "+ProgramName+" "+CommandReleasePR.String()+"; any other change belongs in an ordinary reviewed PR")
		}
	}
	return nil
}

// IsProtectedPath reports whether a path is listed by a configured literal or
// glob. Invalid patterns cannot match; config validation refuses them.
func IsProtectedPath(cfg Config, name string) bool { return matchesProtectedPath(cfg, name) }

func matchesProtectedPath(cfg Config, name string) bool {
	for _, glob := range cfg.ProtectedPaths {
		if matchesPathPattern(glob, name) {
			return true
		}
	}
	return false
}

func matchesPathPattern(pattern, name string) bool {
	if pattern == name {
		return true
	}
	matched, err := path.Match(pattern, name)
	return err == nil && matched
}

// SafeReleaseOutput confines a concrete output even when a glob matches
// metadata, a workflow path, or a Git pathspec. Both output classes use it.
func SafeReleaseOutput(name string) bool {
	return safeReleasePath(name, false)
}

func safeReleasePattern(pattern string) bool {
	if !safeReleasePath(pattern, true) {
		return false
	}
	_, err := path.Match(pattern, "")
	return err == nil
}

func safeReleasePath(name string, pattern bool) bool {
	forbidden := ":\\"
	if !pattern {
		forbidden += "*?[]"
	}
	if name == "" || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, forbidden) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".github") {
			return false
		}
	}
	return true
}

// IsFreezeOutput tests the same glob semantics as protected_paths, but never
// grants access to unsafe concrete paths or paths with separate release rules.
func IsFreezeOutput(cfg Config, name string) bool {
	if !SafeReleaseOutput(name) || name == cfg.Changelog {
		return false
	}
	for _, vf := range cfg.VersionFiles {
		if name == vf.Path {
			return false
		}
	}
	for _, glob := range cfg.FreezeOutputs {
		if matchesPathPattern(glob, name) {
			return true
		}
	}
	return false
}

func configVersionPaths(cfg Config) []string {
	paths := make([]string, 0, len(cfg.VersionFiles))
	for _, vf := range cfg.VersionFiles {
		paths = append(paths, vf.Path)
	}
	return paths
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
		Fix:   "drop the protected edit from the PR; only the release PR writes release headings, protected paths and protected version fields when the batch lands",
	}
}
