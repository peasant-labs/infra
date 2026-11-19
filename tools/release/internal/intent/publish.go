package intent

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	regexp "github.com/wasilibs/go-re2"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
)

// This file owns the judgments behind publish-release: which release a tag
// already has, whether the local asset directory can be published as it is,
// and whether a release on GitHub carries exactly the local assets. The
// command reads the files and talks to GitHub; every decision is made here
// over the facts it read, so the same rules hold in the tests and in a
// workflow.

// PublishState is what a publish-release run did. The zero value is nothing.
type PublishState string

const (
	// PublishStatePublished means this run created, filled, verified and
	// published the release.
	PublishStatePublished PublishState = "published"
	// PublishStateAlreadyPublished means a published release for the tag
	// already carried exactly the local assets, so the run changed nothing.
	PublishStateAlreadyPublished PublishState = "already-published"
	// PublishStateRehearsed means a rehearsal created, filled and verified a
	// draft and then deleted it.
	PublishStateRehearsed PublishState = "rehearsed"
)

// AllPublishStates is the canonical closed set of publish states.
var AllPublishStates = []PublishState{PublishStatePublished, PublishStateAlreadyPublished, PublishStateRehearsed}

func (s PublishState) String() string { return string(s) }

// IsValid reports whether the state is one of the known members.
func (s PublishState) IsValid() bool { return slices.Contains(AllPublishStates, s) }

// UnmarshalText parses a state at a decoding boundary.
func (s *PublishState) UnmarshalText(text []byte) error {
	member, err := closedset.Parse(AllPublishStates, string(text), parsing("publish state", "intent.PublishState"))
	if err != nil {
		return err
	}
	*s = member
	return nil
}

// RehearsalTagPrefix opens every tag a rehearsal may use. A rehearsal tag is
// never pushed, so no real release tag can carry the prefix by accident and
// a leftover rehearsal draft can never match a real tag.
const RehearsalTagPrefix = "rehearsal-"

// AssetDigestPrefix opens GitHub's per-asset digest: "sha256:" and the
// lowercase hex SHA-256 of the uploaded bytes.
const AssetDigestPrefix = "sha256:"

// AssetStateUploaded is the state of an asset whose upload completed.
const AssetStateUploaded = "uploaded"

// LocalEntry is one entry of the local asset directory as the command read
// it with lstat: its name and whether it is a regular file. A symlink, a
// directory or any other kind is not.
type LocalEntry struct {
	Name    string `yaml:"name"`
	Regular bool   `yaml:"regular"`
}

// LocalAsset is one file of the manifest the command builds once, before it
// talks to GitHub: its name, size and lowercase hex SHA-256.
type LocalAsset struct {
	Name   string `yaml:"name"`
	Size   int64  `yaml:"size"`
	SHA256 string `yaml:"sha256"`
}

// ReleaseFact is the part of a GitHub release the judgments read.
type ReleaseFact struct {
	ID         int64  `yaml:"id"`
	Tag        string `yaml:"tag"`
	Draft      bool   `yaml:"draft"`
	Prerelease bool   `yaml:"prerelease"`
	Immutable  bool   `yaml:"immutable"`
}

// ReleaseAssetFact is the part of a GitHub release asset the judgments read.
type ReleaseAssetFact struct {
	Name   string `yaml:"name"`
	Size   int64  `yaml:"size"`
	Digest string `yaml:"digest"`
	State  string `yaml:"state"`
}

// assetName is a file name GitHub keeps as it is. GitHub rewrites special
// characters and leading or trailing periods, and a renamed asset could not
// be compared by name.
var assetName = regexp.MustCompile(`^[A-Za-z0-9_+-](?:[A-Za-z0-9._+-]*[A-Za-z0-9_+-])?$`)

// CheckLocalAssets accepts the asset directory only when it holds at least
// one entry and every entry is a regular file whose name GitHub keeps.
func CheckLocalAssets(dir string, entries []LocalEntry) error {
	if len(entries) == 0 {
		return assetsError(dir, "the asset directory "+strconv.Quote(dir)+" is empty",
			"a release whose build staged nothing is not this build's release, and an empty compare proves nothing",
			"make the build job stage its release files into the directory, or check the --assets path")
	}
	for _, e := range entries {
		if !e.Regular {
			return assetsError(dir, "the asset "+strconv.Quote(e.Name)+" in "+strconv.Quote(dir)+" is not a regular file",
				"a symlink can point outside the build's output and a directory cannot be uploaded, so the manifest would not be what GitHub receives",
				"stage only regular files, flat, in the asset directory")
		}
		if !assetName.MatchString(e.Name) {
			return assetsError(dir, "the asset name "+strconv.Quote(e.Name)+" in "+strconv.Quote(dir)+" is not one GitHub keeps",
				"GitHub renames assets with characters outside letters, digits, '.', '_', '+' and '-', or with a leading or trailing period, so the release could not be compared with the manifest by name",
				"rename the file in the build job")
		}
	}
	return nil
}

func assetsError(dir, what, why, fix string) *Error {
	return &Error{
		Code:  ErrReleaseAssetsInvalid,
		What:  what,
		Why:   why,
		Where: "the --assets directory " + strconv.Quote(dir) + ", read by " + ProgramName + " " + CommandPublishRelease.String(),
		When:  "building the local manifest, before any request to GitHub",
		Means: "nothing was created or uploaded",
		Fix:   fix,
	}
}

// SelectRelease decides what already exists for the tag from the complete
// release list: nothing (nil), or the one published release a rerun checks.
// A list the command could not read to the end, any draft for the tag, or
// two published releases for it are refused: none of them was made by this
// run, and the command never deletes what it did not create.
func SelectRelease(repo, tag string, releases []ReleaseFact, truncated bool) (*ReleaseFact, error) {
	if truncated {
		return nil, selectError(ErrReleaseListTruncated, repo, tag,
			"the release list of "+repo+" did not end within the page cap",
			"a draft for the tag can sit on any page, and one not read could be filled beside this run's draft",
			"delete stale drafts and releases by id until the list fits, then rerun")
	}
	var drafts, published []ReleaseFact
	for _, r := range releases {
		if r.Tag != tag {
			continue
		}
		if r.Draft {
			drafts = append(drafts, r)
		} else {
			published = append(published, r)
		}
	}
	if len(drafts) > 0 {
		return nil, selectError(ErrDraftExists, repo, tag,
			fmt.Sprintf("%d draft release(s) already exist for %s (%s)", len(drafts), tag, releaseIDs(drafts)),
			"this run did not create them, so it cannot know what they hold, and GitHub's tag lookup returns the oldest draft",
			"inspect and delete each by id, never by tag: "+deleteCommands(repo, drafts)+"; then rerun")
	}
	if len(published) > 1 {
		return nil, selectError(ErrReleaseAmbiguous, repo, tag,
			fmt.Sprintf("%d published releases exist for %s (%s)", len(published), tag, releaseIDs(published)),
			"a tag has at most one published release; two mean the state was changed outside this command",
			"decide by hand which release is the tag's, and fix forward with a new tag")
	}
	if len(published) == 1 {
		return &published[0], nil
	}
	return nil, nil
}

func selectError(code ErrorCode, repo, tag, what, why, fix string) *Error {
	return &Error{
		Code:  code,
		What:  what,
		Why:   why,
		Where: "the releases of " + repo + ", read by " + ProgramName + " " + CommandPublishRelease.String(),
		When:  "deciding what already exists for " + tag + ", before creating anything",
		Means: "nothing was created, uploaded or deleted",
		Fix:   fix,
	}
}

func releaseIDs(rs []ReleaseFact) string {
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, "id "+strconv.FormatInt(r.ID, 10))
	}
	return strings.Join(ids, ", ")
}

// DeleteCommand is the by-id deletion a person runs for a release this tool
// will not delete itself.
func DeleteCommand(repo string, id int64) string {
	return "gh api -X DELETE repos/" + repo + "/releases/" + strconv.FormatInt(id, 10)
}

func deleteCommands(repo string, rs []ReleaseFact) string {
	cmds := make([]string, 0, len(rs))
	for _, r := range rs {
		cmds = append(cmds, DeleteCommand(repo, r.ID))
	}
	return strings.Join(cmds, " && ")
}

// CheckRehearsal accepts a rehearsal only for a rehearsal-prefixed tag that
// does not exist: GitHub creates the tag when a release is published, so a
// rehearsal may never come near a real tag.
func CheckRehearsal(tag string, tagExists bool) error {
	why := ""
	switch {
	case !strings.HasPrefix(tag, RehearsalTagPrefix) || len(tag) == len(RehearsalTagPrefix):
		why = "a rehearsal tag starts with " + strconv.Quote(RehearsalTagPrefix) + ", so no release tag can be rehearsed"
	case tagExists:
		why = "the tag exists on the remote, and a rehearsal draft must never sit beside a pushed tag"
	default:
		return nil
	}
	return &Error{
		Code:  ErrRehearsalRefused,
		What:  "the rehearsal tag " + strconv.Quote(tag) + " is refused",
		Why:   why,
		Where: "the --tag of " + ProgramName + " " + CommandPublishRelease.String() + " --rehearse",
		When:  "before the rehearsal creates its draft",
		Means: "nothing was created",
		Fix:   "pass a fresh tag such as " + RehearsalTagPrefix + "<unix-time> that was never pushed",
	}
}

// CheckTagExists refuses to publish a release for a tag the remote does not
// have: publishing a draft would create the tag at the default branch.
func CheckTagExists(repo, tag string, exists bool) error {
	if exists {
		return nil
	}
	return &Error{
		Code:  ErrTagMissing,
		What:  "the tag " + strconv.Quote(tag) + " does not exist in " + repo,
		Why:   "publishing a release whose tag is missing makes GitHub create the tag at the default branch tip, which is not the verified commit",
		Where: "refs/tags/" + tag + " of " + repo + ", read by " + ProgramName + " " + CommandPublishRelease.String(),
		When:  "before creating the draft",
		Means: "nothing was created",
		Fix:   "push the tag first; the release workflow runs on the tag push",
	}
}

// AssetDiff is how a release's assets differ from the local manifest, each
// list sorted by name.
type AssetDiff struct {
	// Missing are local assets the release lacks.
	Missing []string
	// Extra are release assets with no local file, or a second asset of one
	// name.
	Extra []string
	// Changed are assets whose size or digest is not the local file's, or
	// whose digest GitHub did not report.
	Changed []string
	// NotUploaded are assets whose upload did not complete.
	NotUploaded []string
}

// Empty reports whether the release carries exactly the manifest.
func (d AssetDiff) Empty() bool {
	return len(d.Missing)+len(d.Extra)+len(d.Changed)+len(d.NotUploaded) == 0
}

func (d AssetDiff) String() string {
	var parts []string
	for _, p := range []struct {
		label string
		names []string
	}{{"missing", d.Missing}, {"extra", d.Extra}, {"changed", d.Changed}, {"not uploaded", d.NotUploaded}} {
		if len(p.names) > 0 {
			parts = append(parts, fmt.Sprintf("%d %s (%s)", len(p.names), p.label, abbreviate(p.names)))
		}
	}
	return strings.Join(parts, "; ")
}

// abbreviate lists at most a few names, so a message about a 352-asset
// release stays readable.
func abbreviate(names []string) string {
	const shown = 5
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:shown], ", ") + fmt.Sprintf(", and %d more", len(names)-shown)
}

// DiffAssets compares a release's assets with the local manifest: by name,
// size, GitHub's sha256 digest and upload state. Nothing missing, nothing
// extra.
func DiffAssets(local []LocalAsset, remote []ReleaseAssetFact) AssetDiff {
	want := make(map[string]LocalAsset, len(local))
	for _, l := range local {
		want[l.Name] = l
	}
	var d AssetDiff
	seen := map[string]bool{}
	for _, r := range remote {
		l, ok := want[r.Name]
		switch {
		case !ok || seen[r.Name]:
			d.Extra = append(d.Extra, r.Name)
			continue
		case r.State != AssetStateUploaded:
			d.NotUploaded = append(d.NotUploaded, r.Name)
		case r.Size != l.Size || r.Digest != AssetDigestPrefix+l.SHA256:
			d.Changed = append(d.Changed, r.Name)
		}
		seen[r.Name] = true
	}
	for _, l := range local {
		if !seen[l.Name] {
			d.Missing = append(d.Missing, l.Name)
		}
	}
	for _, list := range [][]string{d.Missing, d.Extra, d.Changed, d.NotUploaded} {
		slices.Sort(list)
	}
	return d
}

// JudgeDraft accepts this run's draft only when it is still a draft and
// carries exactly the local manifest.
func JudgeDraft(repo string, rel ReleaseFact, local []LocalAsset, remote []ReleaseAssetFact) error {
	d := DiffAssets(local, remote)
	if rel.Draft && d.Empty() {
		return nil
	}
	what := "draft id " + strconv.FormatInt(rel.ID, 10) + " for " + rel.Tag + " does not carry exactly the local assets: " + d.String()
	if !rel.Draft {
		what = "release id " + strconv.FormatInt(rel.ID, 10) + " for " + rel.Tag + " is no longer a draft before this run published it"
	}
	return &Error{
		Code:  ErrDraftIncomplete,
		What:  what,
		Why:   "a release is published only when GitHub reports every local file, by name, size and sha256 digest, and nothing else",
		Where: "the draft's assets in " + repo + ", compared by " + ProgramName + " " + CommandPublishRelease.String(),
		When:  "verifying the draft before publishing it",
		Means: "the draft is not published; the command deletes its own draft",
		Fix:   "rerun the publish job; if the compare fails again, check what the build staged and whether the upload was interrupted",
	}
}

// JudgePublished accepts a published release only when it carries exactly
// the local manifest and, when required, GitHub reports it immutable. After
// publish and on a rerun alike, a published release is never changed: a
// failure is fixed forward with a new tag.
func JudgePublished(repo string, rel ReleaseFact, local []LocalAsset, remote []ReleaseAssetFact, requireImmutable, rerun bool) error {
	when := "verifying the release this run just published"
	if rerun {
		when = "checking the release a previous run published for this tag"
	}
	d := DiffAssets(local, remote)
	fail := func(code ErrorCode, what, why string) *Error {
		return &Error{
			Code:  code,
			What:  "release id " + strconv.FormatInt(rel.ID, 10) + " for " + rel.Tag + " " + what,
			Why:   why,
			Where: "the release's assets in " + repo + ", compared by " + ProgramName + " " + CommandPublishRelease.String(),
			When:  when,
			Means: "the tag's release is published and is not this build's exact release; the command changes nothing on a published release",
			Fix:   "do not edit or delete the published release; fix forward with a new tag",
		}
	}
	switch {
	case rel.Draft:
		return fail(ErrPublishedReleaseIncomplete, "is still a draft", "a published release is what the tag's users download")
	case len(d.Missing)+len(d.Extra)+len(d.NotUploaded) > 0:
		return fail(ErrPublishedReleaseIncomplete, "does not carry exactly the local assets: "+d.String(),
			"a release is complete only when it carries every local file and nothing else")
	case len(d.Changed) > 0:
		return fail(ErrPublishedReleaseDiffers, "carries assets whose bytes differ from the local files: "+d.String(),
			"the same name with another size or sha256 digest is another build's file")
	case requireImmutable && !rel.Immutable:
		return fail(ErrReleaseNotImmutable, "is not immutable",
			"this repository requires immutable releases, so its assets and tag cannot change after publication")
	}
	return nil
}
