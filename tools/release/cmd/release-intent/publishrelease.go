package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// publish-release creates the tag's release as a draft, uploads every local
// asset to that draft by id, verifies the draft carries exactly the local
// manifest, publishes it by id and verifies the published release again. A
// rerun that meets the published release accepts it only when it is exactly
// the manifest. Before the publish, any failure deletes this run's own
// draft; nothing the run did not create is ever changed.
//
// --rehearse runs the same create, upload and verify for a never-pushed
// rehearsal-* tag and then deletes the draft. It never publishes. It exists
// to prove GitHub's real draft semantics at full scale from a maintainer's
// machine, and is removed once schema and peasant have each published one
// release through this command. The workflow policy refuses it.

// publishOptions is one run's parsed flags.
type publishOptions struct {
	tag, assets, notes         string
	prerelease, latest         bool
	requireImmutable, rehearse bool
	repo                       string
}

func runPublishRelease(flags flagValues) error {
	opts := publishOptions{
		tag: flags.Value(intent.FlagTag), repo: flags.Value(intent.FlagRepo),
		assets: flags.Value(intent.FlagAssets), notes: flags.Value(intent.FlagNotes),
		requireImmutable: flags.Has(intent.FlagRequireImmutable), rehearse: flags.Has(intent.FlagRehearse),
	}
	for _, b := range []struct {
		flag intent.Flag
		dst  *bool
	}{{intent.FlagPrerelease, &opts.prerelease}, {intent.FlagLatest, &opts.latest}} {
		switch v := flags.Value(b.flag); v {
		case "true", "false":
			*b.dst = v == "true"
		default:
			return usageErr("%s %s: %q is neither true nor false. %s", intent.CommandPublishRelease, b.flag, v, usageOf(intent.CommandPublishRelease))
		}
	}
	if opts.prerelease && opts.latest {
		return usageErr("%s: %s true and %s true together: a prerelease is never the latest release. Pass the release-kind job's outputs", intent.CommandPublishRelease, intent.FlagPrerelease, intent.FlagLatest)
	}
	w, err := newReleaseWriter(githubAPIBase(os.Getenv), opts.repo, opts.tag, githubToken(os.Getenv), &http.Client{})
	if err != nil {
		return err
	}
	return publishRelease(w, opts)
}

func reject(err error) error { return rejectAs(intent.CommandPublishRelease, err) }

func publishRelease(w *releaseWriter, opts publishOptions) error {
	if opts.rehearse {
		// The name alone is checked before any request; CheckRehearsal
		// below repeats it with the tag's existence once that is read.
		if err := intent.CheckRehearsalTag(opts.tag); err != nil {
			return reject(err)
		}
	}
	notes, err := readNotes(opts.notes, opts.assets)
	if err != nil {
		return err
	}
	manifest, err := buildManifest(opts.assets)
	if err != nil {
		return err
	}
	var total int64
	for _, a := range manifest {
		total += a.Size
	}
	fmt.Fprintf(os.Stderr, "%s: manifest of %d files, %d bytes, read from %s\n", intent.CommandPublishRelease, len(manifest), total, opts.assets)

	releases, truncated, err := w.ListReleases()
	if err != nil {
		return err
	}
	existing, err := intent.SelectRelease(opts.repo, opts.tag, releases, truncated)
	if err != nil {
		return reject(err)
	}
	if opts.rehearse {
		exists, err := w.TagExists()
		if err != nil {
			return err
		}
		if err := intent.CheckRehearsal(opts.tag, exists || existing != nil); err != nil {
			return reject(err)
		}
	} else if existing != nil {
		return verifyExisting(w, opts, *existing, manifest)
	} else {
		exists, err := w.TagExists()
		if err != nil {
			return err
		}
		if err := intent.CheckTagExists(opts.repo, opts.tag, exists); err != nil {
			return reject(err)
		}
	}

	id, err := w.CreateDraft(notes, opts.prerelease)
	if id != 0 {
		if emitErr := emit(out(intent.OutputReleaseID, strconv.FormatInt(id, 10))); emitErr != nil && err == nil {
			err = emitErr
		}
	}
	if err != nil {
		return cleanupOwnDraft(w, opts, id, err)
	}
	for i, a := range manifest {
		if err := w.Upload(id, filepath.Join(opts.assets, a.Name), a.Name); err != nil {
			return cleanupOwnDraft(w, opts, id, err)
		}
		fmt.Fprintf(os.Stderr, "%s: uploaded %d/%d %s\n", intent.CommandPublishRelease, i+1, len(manifest), a.Name)
	}
	if err := verifyDraft(w, opts, id, manifest); err != nil {
		return cleanupOwnDraft(w, opts, id, err)
	}
	if opts.rehearse {
		if err := w.DeleteOwn(id); err != nil {
			var published *publishedError
			if errors.As(err, &published) {
				return err
			}
			return deleteFailed(opts, id, err, err)
		}
		fmt.Fprintf(os.Stderr, "%s: rehearsal draft id %d verified with %d assets and deleted\n", intent.CommandPublishRelease, id, len(manifest))
		return emit(out(intent.OutputState, intent.PublishStateRehearsed.String()))
	}
	if err := w.Publish(id, opts.prerelease, opts.latest); err != nil {
		// The publish may or may not have taken effect: read it back by id.
		rel, readErr := w.ReadRelease(id)
		if readErr != nil {
			return fmt.Errorf("%w; reading release id %d back also failed (%v): check it by hand, and if it is still a draft delete it with %s", err, id, readErr, intent.DeleteCommand(opts.repo, id))
		}
		if rel.Draft {
			return cleanupOwnDraft(w, opts, id, err)
		}
		w.forget(id)
		fmt.Fprintf(os.Stderr, "%s: the publish request failed (%v), but release id %d is published; verifying it\n", intent.CommandPublishRelease, err, id)
	}
	if err := verifyPublished(w, opts, id, manifest, false); err != nil {
		return err
	}
	return emit(out(intent.OutputState, intent.PublishStatePublished.String()))
}

// verifyExisting is the rerun path: a published release for the tag is
// accepted, unchanged, only when it is exactly this build's assets.
func verifyExisting(w *releaseWriter, opts publishOptions, rel intent.ReleaseFact, manifest []intent.LocalAsset) error {
	if err := emit(out(intent.OutputReleaseID, strconv.FormatInt(rel.ID, 10))); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s: %s already has published release id %d; checking it without changing it\n", intent.CommandPublishRelease, opts.tag, rel.ID)
	if err := verifyPublished(w, opts, rel.ID, manifest, true); err != nil {
		return err
	}
	return emit(out(intent.OutputState, intent.PublishStateAlreadyPublished.String()))
}

// digestRetrySchedule is how long the command waits before reading an
// asset list again while an uploaded asset has no digest yet.
var digestRetrySchedule = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// settledAssets lists a release's assets, reading again while an uploaded
// asset has no digest, so a digest GitHub reports late is not judged as
// missing. After the schedule the last list is judged as it is.
func settledAssets(w *releaseWriter, id int64) ([]intent.ReleaseAssetFact, error) {
	for _, d := range digestRetrySchedule {
		assets, err := w.ListAssets(id)
		if err != nil || !intent.AwaitingDigest(assets) {
			return assets, err
		}
		fmt.Fprintf(os.Stderr, "%s: GitHub has not reported a digest for every asset of release id %d yet; reading again in %s\n", intent.CommandPublishRelease, id, d)
		w.sleep(d)
	}
	return w.ListAssets(id)
}

func verifyDraft(w *releaseWriter, opts publishOptions, id int64, manifest []intent.LocalAsset) error {
	rel, err := w.ReadRelease(id)
	if err != nil {
		return err
	}
	assets, err := settledAssets(w, id)
	if err != nil {
		return err
	}
	if err := intent.JudgeDraft(opts.repo, rel, manifest, assets); err != nil {
		return reject(err)
	}
	return nil
}

func verifyPublished(w *releaseWriter, opts publishOptions, id int64, manifest []intent.LocalAsset, rerun bool) error {
	rel, err := w.ReadRelease(id)
	if err != nil {
		return err
	}
	assets, err := settledAssets(w, id)
	if err != nil {
		return err
	}
	if err := intent.JudgePublished(opts.repo, rel, manifest, assets, intent.PublishWant{Prerelease: opts.prerelease, RequireImmutable: opts.requireImmutable, Rerun: rerun}); err != nil {
		return reject(err)
	}
	return nil
}

// cleanupOwnDraft deletes this run's draft by id after a failure before
// publish and returns the failure. A draft that cannot be deleted is named
// with the command that deletes it.
func cleanupOwnDraft(w *releaseWriter, opts publishOptions, id int64, cause error) error {
	if id == 0 || !w.created[id] {
		return cause
	}
	if err := w.DeleteOwn(id); err != nil {
		var published *publishedError
		if errors.As(err, &published) {
			fmt.Fprintf(os.Stderr, "%s: not deleting release id %d after the failure below: %v\n", intent.CommandPublishRelease, id, err)
			return cause
		}
		return deleteFailed(opts, id, cause, err)
	}
	fmt.Fprintf(os.Stderr, "%s: deleted this run's draft id %d after the failure below; rerun once it is fixed\n", intent.CommandPublishRelease, id)
	return cause
}

func deleteFailed(opts publishOptions, id int64, cause, err error) error {
	fmt.Fprintf(os.Stderr, "%s: could not delete this run's draft id %d (%v). Delete it by id before rerunning: %s\n", intent.CommandPublishRelease, id, err, intent.DeleteCommand(opts.repo, id))
	return cause
}

// readNotes reads the notes file: a non-empty regular file outside the
// asset directory, so the notes are never published as an asset too.
func readNotes(notes, assets string) (string, error) {
	info, err := os.Lstat(notes)
	if err != nil {
		return "", usageErr("%s %s: cannot read %s: %v. Pass the release notes file the build job staged", intent.CommandPublishRelease, intent.FlagNotes, notes, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", usageErr("%s %s: %s is not a non-empty regular file", intent.CommandPublishRelease, intent.FlagNotes, notes)
	}
	absNotes, err1 := filepath.Abs(notes)
	absAssets, err2 := filepath.Abs(assets)
	if err1 != nil || err2 != nil {
		return "", usageErr("%s: cannot resolve %s and %s", intent.CommandPublishRelease, notes, assets)
	}
	if rel, err := filepath.Rel(absAssets, absNotes); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", usageErr("%s %s: %s is inside the asset directory %s, so it would be published as an asset too. Stage the notes beside the directory", intent.CommandPublishRelease, intent.FlagNotes, notes, assets)
	}
	data, err := os.ReadFile(notes)
	if err != nil {
		return "", usageErr("%s %s: cannot read %s: %v", intent.CommandPublishRelease, intent.FlagNotes, notes, err)
	}
	return string(data), nil
}

// buildManifest reads the asset directory once, before any request: each
// entry's kind and name are judged, then each regular file is hashed.
func buildManifest(dir string) ([]intent.LocalAsset, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, usageErr("%s %s: %s is not a directory (%v). Pass the directory the build job staged its release files into", intent.CommandPublishRelease, intent.FlagAssets, dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, usageErr("%s %s: cannot list %s: %v", intent.CommandPublishRelease, intent.FlagAssets, dir, err)
	}
	facts := make([]intent.LocalEntry, 0, len(entries))
	for _, e := range entries {
		facts = append(facts, intent.LocalEntry{Name: e.Name(), Regular: e.Type().IsRegular()})
	}
	if err := intent.CheckLocalAssets(dir, facts); err != nil {
		return nil, reject(err)
	}
	manifest := make([]intent.LocalAsset, 0, len(entries))
	for _, e := range entries {
		asset, err := hashAsset(filepath.Join(dir, e.Name()), e.Name())
		if err != nil {
			return nil, err
		}
		manifest = append(manifest, asset)
	}
	slices.SortFunc(manifest, func(a, b intent.LocalAsset) int { return strings.Compare(a.Name, b.Name) })
	return manifest, nil
}

func hashAsset(path, name string) (intent.LocalAsset, error) {
	f, err := os.Open(path)
	if err != nil {
		return intent.LocalAsset{}, fmt.Errorf("%s: cannot open asset %s: %w", intent.CommandPublishRelease, path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return intent.LocalAsset{}, errors.Join(fmt.Errorf("%s: asset %s is not a regular file", intent.CommandPublishRelease, path), err)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return intent.LocalAsset{}, fmt.Errorf("%s: cannot read asset %s: %w", intent.CommandPublishRelease, path, err)
	}
	return intent.LocalAsset{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
