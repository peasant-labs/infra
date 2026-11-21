package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/publish-release.yaml
var publishReleaseFixtureYAML []byte

const (
	fakeReleaseRepo  = "peasant-labs/schema"
	fakeReleaseToken = "test-token"
	// fakeFirstID is the id the fake server gives the first release this run
	// creates, so the fixture can name it.
	fakeFirstID = 9001
)

type publishAssetFile struct {
	Name    string `yaml:"name"`
	Content string `yaml:"content"`
}

type publishServerRelease struct {
	ID         int64  `yaml:"id"`
	Tag        string `yaml:"tag"`
	Draft      bool   `yaml:"draft"`
	Prerelease bool   `yaml:"prerelease"`
	Immutable  bool   `yaml:"immutable"`
	// The release carries the local assets, less Drop, with Corrupt given
	// another digest.
	LocalAssets bool     `yaml:"local_assets"`
	Drop        []string `yaml:"drop"`
	Corrupt     []string `yaml:"corrupt"`
	// FillerAssets adds this many more assets, to page the asset list.
	FillerAssets int `yaml:"filler_assets"`
}

type publishFaults struct {
	UploadFail string `yaml:"upload_fail"`
	Corrupt    string `yaml:"corrupt"`
	Drop       string `yaml:"drop"`
	Extra      string `yaml:"extra"`
	// DeleteStatus answers every delete with this status instead of 204;
	// DeleteRemoves still removes the release first.
	DeleteStatus  int  `yaml:"delete_status"`
	DeleteRemoves bool `yaml:"delete_removes"`
	// DeleteTimesOutWithAssets answers 502 to the delete of a release that
	// still has assets, as GitHub did for a 352-asset draft.
	DeleteTimesOutWithAssets bool `yaml:"delete_times_out_with_assets"`
	// PublishOnUpload publishes the release, as another actor would, once
	// this asset's upload lands.
	PublishOnUpload string `yaml:"publish_on_upload"`
	// PatchLandsOnRead answers the publish request 502 without applying it,
	// then applies it just before the Nth later read of that release, as a
	// publish that timed out but landed late.
	PatchLandsOnRead int `yaml:"patch_lands_on_read"`
	// RedirectList answers the release listing with a 302 to another path.
	RedirectList bool `yaml:"redirect_list"`
	// DigestNullReads reports every asset's digest as null on the first N
	// asset listings of a release this run created.
	DigestNullReads int `yaml:"digest_null_reads"`
	// PublishAfterAssetDeletes flips the release to published after N
	// individual asset deletions.
	PublishAfterAssetDeletes int `yaml:"publish_after_asset_deletes"`
	// ReadReleaseFailFromRead answers GET /releases/<id> with
	// ReadReleaseFailStatus starting at the Nth read of that release.
	ReadReleaseFailFromRead int `yaml:"read_release_fail_from_read"`
	ReadReleaseFailStatus   int `yaml:"read_release_fail_status"`
	// CreateLost creates the draft, then answers the create with 502.
	CreateLost bool `yaml:"create_lost"`
	// RewriteLocalOnUpload rewrites a local file (same size, other bytes)
	// when another asset's upload lands: a file changing after the manifest
	// was hashed and before its own upload.
	RewriteLocalOnUpload struct {
		At   string `yaml:"at"`
		File string `yaml:"file"`
	} `yaml:"rewrite_local_on_upload"`
}

type publishServer struct {
	Tags               []string               `yaml:"tags"`
	Filler             int                    `yaml:"filler"`
	Releases           []publishServerRelease `yaml:"releases"`
	ImmutableOnPublish bool                   `yaml:"immutable_on_publish"`
	Faults             publishFaults          `yaml:"faults"`
}

type publishAfter struct {
	ID         int64  `yaml:"id"`
	Draft      bool   `yaml:"draft"`
	Prerelease bool   `yaml:"prerelease"`
	MakeLatest string `yaml:"make_latest"`
	Assets     int    `yaml:"assets"`
}

type publishCase struct {
	Name  string `yaml:"name"`
	Input struct {
		Tag string `yaml:"tag"`
		// ExtraLocalAssets stages this many more small local files.
		ExtraLocalAssets int           `yaml:"extra_local_assets"`
		Prerelease       string        `yaml:"prerelease"`
		Latest           string        `yaml:"latest"`
		RequireImmutable bool          `yaml:"require_immutable"`
		Rehearse         bool          `yaml:"rehearse"`
		Server           publishServer `yaml:"server"`
	} `yaml:"input"`
	Expected struct {
		Exit         int      `yaml:"exit"`
		Lines        []string `yaml:"lines"`
		Stderr       []string `yaml:"stderr"`
		StderrAbsent []string `yaml:"stderr_absent"`
		// After lists every release for the tag once the run ended.
		After []publishAfter `yaml:"after"`
		// NoRequests asserts the run refused before any request.
		NoRequests bool `yaml:"no_requests"`
		// NoDelete asserts the run sent no DELETE request.
		NoDelete bool `yaml:"no_delete"`
		// NoPublish asserts the run sent no PATCH (publish) request.
		NoPublish bool `yaml:"no_publish"`
		// Requests, when set, is the exact request log.
		Requests []string `yaml:"requests"`
	} `yaml:"expected"`
	Sibling        string                  `yaml:"sibling"`
	Classification testcase.Classification `yaml:"classification"`
	Provenance     testcase.Provenance     `yaml:"provenance"`
	Mutation       testcase.Mutation       `yaml:"mutation"`
}

type permitCase struct {
	Name          string  `yaml:"name"`
	Method        string  `yaml:"method"`
	URL           string  `yaml:"url"`
	Created       []int64 `yaml:"created"`
	Confirmed     []int64 `yaml:"confirmed"`
	OwnAssets     []int64 `yaml:"own_assets"`
	AllowDelete   bool    `yaml:"allow_delete"`
	Call          string  `yaml:"call"`
	ErrorContains string  `yaml:"error_contains"`
}

// deleteCase drives DeleteOwn in process with a counting sleep.
type deleteCase struct {
	Name          string        `yaml:"name"`
	Faults        publishFaults `yaml:"faults"`
	Deleted       bool          `yaml:"deleted"`
	Sleeps        int           `yaml:"sleeps"`
	Unconfirmed   bool          `yaml:"unconfirmed"`
	ErrorContains string        `yaml:"error_contains"`
}

type readStateCase struct {
	Name         string `yaml:"name"`
	Draft        bool   `yaml:"draft"`
	Retained     bool   `yaml:"retained"`
	GoneRead     bool   `yaml:"gone_read"`
	PublishError bool   `yaml:"publish_error"`
}

type publishFixture struct {
	RequiredNames       []string           `yaml:"required_names"`
	Assets              []publishAssetFile `yaml:"assets"`
	Notes               string             `yaml:"notes"`
	Cases               []publishCase      `yaml:"cases"`
	PermitRequiredNames []string           `yaml:"permit_required_names"`
	PermitCases         []permitCase       `yaml:"permit_cases"`
	DeleteRequiredNames []string           `yaml:"delete_required_names"`
	DeleteCases         []deleteCase       `yaml:"delete_cases"`
	ReadRequiredNames   []string           `yaml:"read_required_names"`
	ReadCases           []readStateCase    `yaml:"read_cases"`
}

func loadPublishFixture(t *testing.T) publishFixture {
	t.Helper()
	var f publishFixture
	if err := strictDecode(publishReleaseFixtureYAML, &f); err != nil {
		t.Fatalf("publish-release fixture: %v", err)
	}
	seen := map[string]struct{}{}
	byName := map[string]publishCase{}
	for _, c := range f.Cases {
		if _, dup := seen[c.Name]; dup {
			t.Fatalf("publish-release fixture: case %q appears twice", c.Name)
		}
		seen[c.Name] = struct{}{}
		byName[c.Name] = c
		if !c.Classification.IsValid() || !c.Provenance.Source.IsValid() || c.Provenance.Ref == "" || c.Mutation.Description == "" {
			t.Fatalf("publish-release fixture: case %q is under-populated", c.Name)
		}
	}
	for _, c := range f.Cases {
		if c.Classification != testcase.MustFail {
			continue
		}
		if sib, ok := byName[c.Sibling]; !ok || sib.Classification != testcase.MustPass {
			t.Fatalf("publish-release fixture: reject %q needs an accept sibling, got %q", c.Name, c.Sibling)
		}
	}
	checkRequiredNames(t, f.RequiredNames, seen, "publish-release fixture")
	permitSeen := map[string]struct{}{}
	for _, c := range f.PermitCases {
		permitSeen[c.Name] = struct{}{}
	}
	checkRequiredNames(t, f.PermitRequiredNames, permitSeen, "publish-release permit fixture")
	deleteSeen := map[string]struct{}{}
	for _, c := range f.DeleteCases {
		deleteSeen[c.Name] = struct{}{}
	}
	checkRequiredNames(t, f.DeleteRequiredNames, deleteSeen, "publish-release delete fixture")
	readSeen := map[string]struct{}{}
	for _, c := range f.ReadCases {
		readSeen[c.Name] = struct{}{}
	}
	checkRequiredNames(t, f.ReadRequiredNames, readSeen, "publish-release read fixture")
	return f
}

func TestPublishRelease_ReadDraftState(t *testing.T) {
	for _, c := range loadPublishFixture(t).ReadCases {
		t.Run(c.Name, func(t *testing.T) {
			faults := publishFaults{}
			if c.PublishError {
				faults.PatchLandsOnRead = 2
			}
			api := newFakeReleaseServer(t, publishServer{Faults: faults}, nil)
			w, err := newReleaseWriter(api.server.URL, fakeReleaseRepo, "v0.59.0", fakeReleaseToken, api.server.Client())
			if err != nil {
				t.Fatal(err)
			}
			id, err := w.CreateDraft("notes", false)
			if err != nil {
				t.Fatal(err)
			}
			w.confirmedDraft[id] = true
			const assetID = 1
			w.ownAssets[assetID] = id
			api.mu.Lock()
			api.find(id).Draft = c.Draft
			api.mu.Unlock()
			switch {
			case c.PublishError:
				if err := w.Publish(id, false, true); err == nil {
					t.Fatal("Publish unexpectedly succeeded")
				}
			case c.GoneRead:
				if _, err := w.releaseGone(id); err == nil {
					t.Fatal("releaseGone accepted a published release")
				}
			default:
				if _, err := w.ReadRelease(id); err != nil {
					t.Fatal(err)
				}
			}
			_, assetRetained := w.ownAssets[assetID]
			if w.created[id] != c.Retained || w.confirmedDraft[id] != c.Retained || assetRetained != c.Retained {
				t.Fatalf("draft state retained (release/confirmation/asset) = %v/%v/%v, want %v", w.created[id], w.confirmedDraft[id], assetRetained, c.Retained)
			}
		})
	}
}

// fakeRelease and fakeAsset are the fake server's state.
type fakeRelease struct {
	ID         int64       `json:"id"`
	Tag        string      `json:"tag_name"`
	Draft      bool        `json:"draft"`
	Prerelease bool        `json:"prerelease"`
	Immutable  bool        `json:"immutable"`
	Assets     []fakeAsset `json:"-"`
	MakeLatest string      `json:"-"`
	Body       string      `json:"-"`
}

type fakeAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

// fakeReleaseServer is a stateful GitHub for one repository: releases are
// listed newest first and paged, and creates, uploads, updates and deletes
// change the state. Every request is logged.
type fakeReleaseServer struct {
	mu       sync.Mutex
	t        *testing.T
	cfg      publishServer
	tags     map[string]bool
	releases []*fakeRelease
	nextID   int64
	assetID  int64
	// pendingPatch counts reads of a release until a late publish lands.
	pendingPatch map[int64]int
	// assetReads counts asset listings per release.
	assetReads map[int64]int
	// releaseReads counts GET /releases/<id> calls per release.
	releaseReads map[int64]int
	assetDeletes int
	requests     []string
	server       *httptest.Server
	// localDir is the run's asset directory, for RewriteLocalOnUpload.
	localDir string
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return intent.AssetDigestPrefix + hex.EncodeToString(sum[:])
}

func newFakeReleaseServer(t *testing.T, cfg publishServer, local []publishAssetFile) *fakeReleaseServer {
	t.Helper()
	s := &fakeReleaseServer{t: t, cfg: cfg, tags: map[string]bool{}, nextID: fakeFirstID, assetID: 765391956}
	for _, tag := range cfg.Tags {
		s.tags[tag] = true
	}
	for _, r := range cfg.Releases {
		rel := &fakeRelease{ID: r.ID, Tag: r.Tag, Draft: r.Draft, Prerelease: r.Prerelease, Immutable: r.Immutable}
		if r.LocalAssets {
			for _, a := range local {
				if contains(r.Drop, a.Name) {
					continue
				}
				digest := digestOf([]byte(a.Content))
				if contains(r.Corrupt, a.Name) {
					digest = digestOf([]byte(a.Content + "?"))
				}
				s.assetID++
				rel.Assets = append(rel.Assets, fakeAsset{ID: s.assetID, Name: a.Name, Size: int64(len(a.Content)), Digest: digest, State: intent.AssetStateUploaded})
			}
		}
		for i := 0; i < r.FillerAssets; i++ {
			s.assetID++
			rel.Assets = append(rel.Assets, fakeAsset{ID: s.assetID, Name: fmt.Sprintf("filler-%05d.json", i), Size: 2, Digest: digestOf([]byte("{}")), State: intent.AssetStateUploaded})
		}
		s.releases = append(s.releases, rel)
	}
	// Filler releases are newer than every listed one, so a listed release
	// lands past the first page once there are enough of them.
	var filler []*fakeRelease
	for i := 0; i < cfg.Filler; i++ {
		filler = append(filler, &fakeRelease{ID: int64(100000 + i), Tag: "v0.0." + strconv.Itoa(i)})
	}
	s.releases = append(filler, s.releases...)
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var (
	fakeReleasePath = regexp.MustCompile(`^/repos/` + regexp.QuoteMeta(fakeReleaseRepo) + `/releases/([0-9]+)(/assets)?$`)
	fakeAssetPath   = regexp.MustCompile(`^/repos/` + regexp.QuoteMeta(fakeReleaseRepo) + `/releases/assets/([0-9]+)$`)
	fakeTagRefPath  = regexp.MustCompile(`^/repos/` + regexp.QuoteMeta(fakeReleaseRepo) + `/git/ref/tags/(.+)$`)
)

func (s *fakeReleaseServer) find(id int64) *fakeRelease {
	for _, r := range s.releases {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (s *fakeReleaseServer) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *fakeReleaseServer) page(w http.ResponseWriter, r *http.Request, n int, write func(lo, hi int)) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if per <= 0 {
		per = 30
	}
	if pg <= 0 {
		pg = 1
	}
	lo, hi := (pg-1)*per, pg*per
	if lo > n {
		lo = n
	}
	if hi > n {
		hi = n
	}
	if hi < n {
		// GitHub names the repository by its numeric id in the next link
		// (recorded on the first real rehearsal); the writer must not follow it.
		rest := strings.TrimPrefix(r.URL.Path, "/repos/"+fakeReleaseRepo)
		w.Header().Set("Link", fmt.Sprintf(`<%s/repositories/1273930850%s?per_page=%d&page=%d>; rel="next"`, s.server.URL, rest, per, pg+1))
	}
	write(lo, hi)
}

func (s *fakeReleaseServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+fakeReleaseToken {
		s.writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	path := r.URL.Path
	switch {
	case path == "/repos/"+fakeReleaseRepo+"/releases" && r.Method == http.MethodGet && s.cfg.Faults.RedirectList:
		http.Redirect(w, r, s.server.URL+"/redirected/releases", http.StatusFound)
		return
	case path == "/repos/"+fakeReleaseRepo+"/releases" && r.Method == http.MethodGet:
		s.page(w, r, len(s.releases), func(lo, hi int) { s.writeJSON(w, http.StatusOK, s.releases[lo:hi]) })
		return
	case path == "/repos/"+fakeReleaseRepo+"/releases" && r.Method == http.MethodPost:
		var body struct {
			Tag        string `json:"tag_name"`
			Body       string `json:"body"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Draft || body.Body == "" {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "bad create"})
			return
		}
		rel := &fakeRelease{ID: s.nextID, Tag: body.Tag, Draft: true, Prerelease: body.Prerelease, Body: body.Body}
		s.nextID++
		s.releases = append([]*fakeRelease{rel}, s.releases...)
		if s.cfg.Faults.CreateLost {
			s.writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Server Error"})
			return
		}
		s.writeJSON(w, http.StatusCreated, rel)
		return
	}
	if m := fakeTagRefPath.FindStringSubmatch(path); m != nil && r.Method == http.MethodGet {
		if s.tags[m[1]] {
			s.writeJSON(w, http.StatusOK, map[string]string{"ref": "refs/tags/" + m[1]})
		} else {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		}
		return
	}
	if m := fakeAssetPath.FindStringSubmatch(path); m != nil && r.Method == http.MethodDelete {
		aid, _ := strconv.ParseInt(m[1], 10, 64)
		for _, rel := range s.releases {
			for i, a := range rel.Assets {
				if a.ID == aid {
					rel.Assets = append(rel.Assets[:i], rel.Assets[i+1:]...)
					s.assetDeletes++
					if s.cfg.Faults.PublishAfterAssetDeletes == s.assetDeletes {
						rel.Draft = false
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}
		s.writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	m := fakeReleasePath.FindStringSubmatch(path)
	if m == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	rel := s.find(id)
	if rel == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	switch {
	case m[2] != "" && r.Method == http.MethodPost:
		s.upload(w, r, rel)
	case m[2] != "" && r.Method == http.MethodGet:
		if s.assetReads == nil {
			s.assetReads = map[int64]int{}
		}
		if r.URL.Query().Get("page") == "" {
			s.assetReads[id]++
		}
		if id >= fakeFirstID && s.assetReads[id] <= s.cfg.Faults.DigestNullReads {
			type nullDigest struct {
				ID     int64   `json:"id"`
				Name   string  `json:"name"`
				Size   int64   `json:"size"`
				Digest *string `json:"digest"`
				State  string  `json:"state"`
			}
			s.page(w, r, len(rel.Assets), func(lo, hi int) {
				var out []nullDigest
				for _, a := range rel.Assets[lo:hi] {
					out = append(out, nullDigest{ID: a.ID, Name: a.Name, Size: a.Size, State: a.State})
				}
				s.writeJSON(w, http.StatusOK, out)
			})
			return
		}
		s.page(w, r, len(rel.Assets), func(lo, hi int) { s.writeJSON(w, http.StatusOK, rel.Assets[lo:hi]) })
	case r.Method == http.MethodGet:
		if s.releaseReads == nil {
			s.releaseReads = map[int64]int{}
		}
		s.releaseReads[id]++
		if s.cfg.Faults.ReadReleaseFailFromRead > 0 && s.releaseReads[id] >= s.cfg.Faults.ReadReleaseFailFromRead {
			status := s.cfg.Faults.ReadReleaseFailStatus
			if status == 0 {
				status = http.StatusServiceUnavailable
			}
			s.writeJSON(w, status, map[string]string{"message": http.StatusText(status)})
			return
		}
		if n, ok := s.pendingPatch[id]; ok {
			if n <= 1 {
				delete(s.pendingPatch, id)
				rel.Draft, rel.Immutable = false, s.cfg.ImmutableOnPublish
			} else {
				s.pendingPatch[id] = n - 1
			}
		}
		s.writeJSON(w, http.StatusOK, rel)
	case r.Method == http.MethodPatch:
		var body struct {
			Draft      *bool  `json:"draft"`
			Prerelease bool   `json:"prerelease"`
			MakeLatest string `json:"make_latest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Draft == nil || *body.Draft {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "bad update"})
			return
		}
		if s.cfg.Faults.PatchLandsOnRead > 0 {
			if s.pendingPatch == nil {
				s.pendingPatch = map[int64]int{}
			}
			s.pendingPatch[id] = s.cfg.Faults.PatchLandsOnRead
			rel.Prerelease, rel.MakeLatest = body.Prerelease, body.MakeLatest
			s.writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Server Error"})
			return
		}
		rel.Draft, rel.Prerelease, rel.MakeLatest = false, body.Prerelease, body.MakeLatest
		rel.Immutable = s.cfg.ImmutableOnPublish
		s.writeJSON(w, http.StatusOK, rel)
	case r.Method == http.MethodDelete:
		f := s.cfg.Faults
		if f.DeleteTimesOutWithAssets && len(rel.Assets) > 0 {
			s.writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Server Error"})
			return
		}
		if f.DeleteStatus == 0 || f.DeleteRemoves {
			for i, x := range s.releases {
				if x.ID == id {
					s.releases = append(s.releases[:i], s.releases[i+1:]...)
					break
				}
			}
		}
		if f.DeleteStatus != 0 {
			s.writeJSON(w, f.DeleteStatus, map[string]string{"message": http.StatusText(f.DeleteStatus)})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		s.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "no"})
	}
}

func (s *fakeReleaseServer) upload(w http.ResponseWriter, r *http.Request, rel *fakeRelease) {
	name := r.URL.Query().Get("name")
	data, err := io.ReadAll(r.Body)
	if err != nil || int64(len(data)) != r.ContentLength || r.Header.Get("Content-Type") != "application/octet-stream" {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad upload"})
		return
	}
	f := s.cfg.Faults
	if !rel.Draft {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Cannot upload assets to an immutable release."})
		return
	}
	if name == f.UploadFail {
		s.assetID++
		rel.Assets = append(rel.Assets, fakeAsset{ID: s.assetID, Name: name, Size: 0, State: "starter"})
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Bad Gateway"})
		return
	}
	s.assetID++
	asset := fakeAsset{ID: s.assetID, Name: name, Size: int64(len(data)), Digest: digestOf(data), State: intent.AssetStateUploaded}
	if name == f.Corrupt {
		asset.Digest = digestOf(append(data, '?'))
	}
	if name != f.Drop {
		rel.Assets = append(rel.Assets, asset)
	}
	if rw := f.RewriteLocalOnUpload; rw.At == name && s.localDir != "" {
		target := filepath.Join(s.localDir, rw.File)
		content, err := os.ReadFile(target)
		if err == nil && len(content) > 0 {
			content[0] ^= 0x01
			err = os.WriteFile(target, content, 0o644)
		}
		if err != nil {
			s.t.Errorf("rewrite %s: %v", target, err)
		}
	}
	if name == f.PublishOnUpload {
		rel.Draft, rel.Immutable = false, true
	}
	if f.Extra != "" && len(rel.Assets) == 1 {
		s.assetID++
		rel.Assets = append(rel.Assets, fakeAsset{ID: s.assetID, Name: f.Extra, Size: 1, Digest: digestOf([]byte("x")), State: intent.AssetStateUploaded})
	}
	s.writeJSON(w, http.StatusCreated, asset)
}

// TestPublishRelease_Process runs the built binary against the fake
// GitHub: flag parsing, the manifest, every request through the writer's
// allow-list, the machine lines, the exit status and the releases left
// behind.
func TestPublishRelease_Process(t *testing.T) {
	f := loadPublishFixture(t)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			assets := filepath.Join(dir, "dist", "assets")
			if err := os.MkdirAll(assets, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, a := range f.Assets {
				if err := os.WriteFile(filepath.Join(assets, a.Name), []byte(a.Content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < c.Input.ExtraLocalAssets; i++ {
				if err := os.WriteFile(filepath.Join(assets, fmt.Sprintf("bulk-%03d.json", i)), []byte(fmt.Sprintf(`{"n":%d}`, i)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "dist", "release-notes.md"), []byte(f.Notes), 0o644); err != nil {
				t.Fatal(err)
			}
			api := newFakeReleaseServer(t, c.Input.Server, f.Assets)
			api.mu.Lock()
			api.localDir = assets
			api.mu.Unlock()
			outFile := filepath.Join(t.TempDir(), "github-output")
			if err := os.WriteFile(outFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			argv := []string{intent.CommandPublishRelease.String(),
				intent.FlagTag.String(), c.Input.Tag, intent.FlagRepo.String(), fakeReleaseRepo,
				intent.FlagAssets.String(), "dist/assets", intent.FlagNotes.String(), "dist/release-notes.md",
				intent.FlagPrerelease.String(), c.Input.Prerelease, intent.FlagLatest.String(), c.Input.Latest}
			if c.Input.RequireImmutable {
				argv = append(argv, intent.FlagRequireImmutable.String())
			}
			if c.Input.Rehearse {
				argv = append(argv, intent.FlagRehearse.String())
			}
			res := runBinary(t, dir, argv, "", []string{githubAPIEnv + "=" + api.server.URL, "GH_TOKEN=" + fakeReleaseToken, githubOutputEnv + "=" + outFile})
			if int(res.Exit) != c.Expected.Exit {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.Exit, c.Expected.Exit, res.Stdout, res.Stderr)
			}
			got := strings.Fields(res.Stdout)
			if strings.Join(got, "\n") != strings.Join(c.Expected.Lines, "\n") {
				t.Fatalf("stdout lines = %q, want %q\nstderr: %s", got, c.Expected.Lines, res.Stderr)
			}
			written, err := os.ReadFile(outFile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(strings.Fields(string(written)), "\n") != strings.Join(c.Expected.Lines, "\n") {
				t.Fatalf("step outputs = %q, want %q", written, c.Expected.Lines)
			}
			for _, frag := range c.Expected.Stderr {
				if !strings.Contains(res.Stderr, frag) {
					t.Fatalf("stderr does not contain %q:\n%s", frag, res.Stderr)
				}
			}
			if strings.Contains(res.Stderr+res.Stdout, fakeReleaseToken) {
				t.Fatalf("the output carries the token")
			}
			for _, frag := range c.Expected.StderrAbsent {
				if strings.Contains(res.Stderr, frag) {
					t.Fatalf("stderr contains %q:\n%s", frag, res.Stderr)
				}
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if c.Expected.Requests != nil && strings.Join(api.requests, "\n") != strings.Join(c.Expected.Requests, "\n") {
				t.Fatalf("requests = %q, want exactly %q", api.requests, c.Expected.Requests)
			}
			if c.Expected.NoRequests && len(api.requests) != 0 {
				t.Fatalf("the run sent %d requests before refusing: %v", len(api.requests), api.requests)
			}
			if c.Expected.NoDelete {
				for _, req := range api.requests {
					if strings.HasPrefix(req, http.MethodDelete+" ") {
						t.Fatalf("the run sent unexpected DELETE request %q; all requests: %v", req, api.requests)
					}
				}
			}
			if c.Expected.NoPublish {
				for _, req := range api.requests {
					if strings.HasPrefix(req, http.MethodPatch+" ") {
						t.Fatalf("the run sent unexpected PATCH (publish) request %q; all requests: %v", req, api.requests)
					}
				}
			}
			var after []publishAfter
			for _, r := range api.releases {
				if r.Tag == c.Input.Tag {
					after = append(after, publishAfter{ID: r.ID, Draft: r.Draft, Prerelease: r.Prerelease, MakeLatest: r.MakeLatest, Assets: len(r.Assets)})
				}
			}
			if fmt.Sprint(after) != fmt.Sprint(c.Expected.After) {
				t.Fatalf("releases for %s after the run = %+v, want %+v\nrequests: %v", c.Input.Tag, after, c.Expected.After, api.requests)
			}
		})
	}
}

// TestPublishRelease_Permit drives the writer's allow-list in process: each
// row is one request, the call it must map to, or "" when it is refused
// before anything is sent.
func TestPublishRelease_Permit(t *testing.T) {
	f := loadPublishFixture(t)
	for _, c := range f.PermitCases {
		t.Run(c.Name, func(t *testing.T) {
			w, err := newReleaseWriter(defaultGitHubAPI, fakeReleaseRepo, "v0.58.0", fakeReleaseToken, http.DefaultClient)
			if err != nil {
				t.Fatal(err)
			}
			w.allowDelete = c.AllowDelete
			for _, id := range c.Created {
				w.created[id] = true
			}
			for _, id := range c.Confirmed {
				w.confirmedDraft[id] = true
			}
			for _, id := range c.OwnAssets {
				w.ownAssets[id] = 9001
			}
			call, err := w.permit(c.Method, c.URL)
			if c.Call == "" {
				if err == nil {
					t.Fatalf("permit(%s %s) = %s, want a refusal", c.Method, c.URL, call)
				}
				if strings.Contains(err.Error(), fakeReleaseToken) {
					t.Fatalf("the refusal carries the token")
				}
				if c.ErrorContains == "" || !strings.Contains(err.Error(), c.ErrorContains) {
					t.Fatalf("permit(%s %s) refused with %q, want it to name %q", c.Method, c.URL, err, c.ErrorContains)
				}
				return
			}
			if err != nil || call.String() != c.Call {
				t.Fatalf("permit(%s %s) = %q, %v; want %s", c.Method, c.URL, call, err, c.Call)
			}
		})
	}
	// Every allowed call is sampled by an accepted row.
	sampled := map[string]bool{}
	for _, c := range f.PermitCases {
		sampled[c.Call] = true
	}
	for _, call := range allReleaseCalls {
		if !sampled[call.String()] {
			t.Fatalf("no permit row accepts %s", call)
		}
	}
}

// TestPublishRelease_RefusesForeignAPI proves the writer sends the token only
// to github.com or a loopback test server.
func TestPublishRelease_RefusesForeignAPI(t *testing.T) {
	for _, base := range []string{"http://api.github.com", "https://ghe.example.com/api/v3", "http://10.0.0.1:8080", "https://api.github.com.evil.example"} {
		if _, err := newReleaseWriter(base, fakeReleaseRepo, "v0.58.0", fakeReleaseToken, http.DefaultClient); err == nil {
			t.Fatalf("newReleaseWriter accepted API base %q", base)
		}
	}
	if _, err := newReleaseWriter("http://127.0.0.1:1234", fakeReleaseRepo, "v0.58.0", fakeReleaseToken, http.DefaultClient); err != nil {
		t.Fatalf("newReleaseWriter refused a loopback test server: %v", err)
	}
}

// TestPublishRelease_DeleteConfirm drives DeleteOwn in process: a delete
// that answers with a server error is read back until the release is gone
// or the schedule ends; a client error is final at once.
func TestPublishRelease_DeleteConfirm(t *testing.T) {
	f := loadPublishFixture(t)
	for _, c := range f.DeleteCases {
		t.Run(c.Name, func(t *testing.T) {
			api := newFakeReleaseServer(t, publishServer{Faults: c.Faults}, nil)
			w, err := newReleaseWriter(api.server.URL, fakeReleaseRepo, "rehearsal-1763500000", fakeReleaseToken, api.server.Client())
			if err != nil {
				t.Fatal(err)
			}
			w.allowDelete = true
			sleeps := 0
			w.sleep = func(time.Duration) { sleeps++ }
			id, err := w.CreateDraft("notes", false)
			if err != nil {
				t.Fatal(err)
			}
			asset := filepath.Join(t.TempDir(), "a.json")
			if err := os.WriteFile(asset, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := w.Upload(id, asset, "a.json"); err != nil {
				t.Fatal(err)
			}
			err = w.DeleteOwn(id)
			if (err == nil) != c.Deleted {
				t.Fatalf("DeleteOwn = %v, want deleted=%t", err, c.Deleted)
			}
			var unconfirmed *unconfirmedStatusError
			if errors.As(err, &unconfirmed) != c.Unconfirmed {
				t.Fatalf("DeleteOwn unconfirmed = %t (%v), want %t", errors.As(err, &unconfirmed), err, c.Unconfirmed)
			}
			if c.ErrorContains != "" && (err == nil || !strings.Contains(err.Error(), c.ErrorContains)) {
				t.Fatalf("DeleteOwn error = %v, want fragment %q", err, c.ErrorContains)
			}
			if sleeps != c.Sleeps {
				t.Fatalf("slept %d times, want %d", sleeps, c.Sleeps)
			}
			if c.Deleted && w.created[id] {
				t.Fatalf("a deleted release is still recorded as this run's")
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if c.Deleted != (api.find(id) == nil) {
				t.Fatalf("the fake still holds release %d: %t, want deleted=%t", id, api.find(id) != nil, c.Deleted)
			}
		})
	}
}

// TestGitHubReader_RefusesRedirect proves inherit-verdict's reader returns a
// redirect as a status error and never follows it.
func TestGitHubReader_RefusesRedirect(t *testing.T) {
	var followed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/redirected") {
			followed = true
		}
		http.Redirect(w, r, "/redirected"+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)
	reader, err := newGitHubReader(srv.URL, fakeReleaseRepo, fakeReleaseToken, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.CheckRuns("adf33e168bd7f57f5a3d0ef9786366c554ec1662")
	if err == nil || !strings.Contains(err.Error(), "301") || followed {
		t.Fatalf("CheckRuns = %v, followed = %t; want a 301 status error and no request to the target", err, followed)
	}
}
