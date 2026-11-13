package intent

import "strconv"

// This file holds the release-tag verification a tag-triggered publish
// workflow runs before it publishes: the tag is the release prefix plus a
// version, and its commit is on the default branch. A tag push needs only a
// write to refs/tags/, so the workflow cannot trust a tag because it exists.

// TagCheck is one tag verification: the tag name, the branch it must be on,
// and whether the branch reaches the tag's commit. The command layer reads
// reachability from git; this file only judges the answer.
type TagCheck struct {
	Tag       string `yaml:"tag"`
	Branch    string `yaml:"branch"`
	Reachable bool   `yaml:"reachable"`
}

// VerifyTagName returns the version a release tag names: the release
// config's tag prefix followed by a bare X.Y.Z version, nothing before or
// after. Any other name is refused with ErrTagUnverified.
func VerifyTagName(cfg Config, tag string) (SemVer, error) {
	version, ok := splitReleaseTag(cfg.TagPrefix, tag)
	if !ok {
		return "", tagError(
			"tag "+strconv.Quote(tag)+" is not a release tag",
			"a release tag is the release config's tag prefix "+strconv.Quote(cfg.TagPrefix)+" followed by a bare X.Y.Z version, which is the only tag the release job pushes",
			"push only the tag the release job prints, or publish this tag by hand outside the release workflow",
		)
	}
	return version, nil
}

// VerifyTag accepts a release tag only when its name is a release tag and
// the branch reaches its commit. A tag whose commit is off the branch was
// never planned from the landed history, whatever its name.
func VerifyTag(cfg Config, check TagCheck) error {
	if _, err := VerifyTagName(cfg, check.Tag); err != nil {
		return err
	}
	if !check.Reachable {
		return tagError(
			"tag "+strconv.Quote(check.Tag)+" points at a commit "+strconv.Quote(check.Branch)+" does not reach",
			"the release job tags a commit that landed on the default branch through the merge queue, so a tag off that branch was not made by it",
			"delete the tag; release tags come only from the release job on landed commits",
		)
	}
	return nil
}

// tagError builds the typed tag rejection.
func tagError(what, why, fix string) *Error {
	return &Error{
		Code:  ErrTagUnverified,
		What:  what,
		Why:   why,
		Where: "the triggering tag, checked by " + ProgramName + " " + CommandVerifyTag.String(),
		When:  "verifying a pushed tag before the publish job runs",
		Means: "nothing is published for this tag: the publish job needs this check",
		Fix:   fix,
	}
}
