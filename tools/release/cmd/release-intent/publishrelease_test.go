package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
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
}

type publishFaults struct {
	UploadFail string `yaml:"upload_fail"`
	Corrupt    string `yaml:"corrupt"`
	Drop       string `yaml:"drop"`
	Extra      string `yaml:"extra"`
	DeleteFail bool   `yaml:"delete_fail"`
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
		Tag              string        `yaml:"tag"`
		Prerelease       string        `yaml:"prerelease"`
		Latest           string        `yaml:"latest"`
		RequireImmutable bool          `yaml:"require_immutable"`
		Rehearse         bool          `yaml:"rehearse"`
		Server           publishServer `yaml:"server"`
	} `yaml:"input"`
	Expected struct {
		Exit   int      `yaml:"exit"`
		Lines  []string `yaml:"lines"`
		Stderr []string `yaml:"stderr"`
		// After lists every release for the tag once the run ended.
		After []publishAfter `yaml:"after"`
		// NoRequests asserts the run refused before any request.
		NoRequests bool `yaml:"no_requests"`
	} `yaml:"expected"`
	Sibling        string                  `yaml:"sibling"`
	Classification testcase.Classification `yaml:"classification"`
	Provenance     testcase.Provenance     `yaml:"provenance"`
	Mutation       testcase.Mutation       `yaml:"mutation"`
}

type permitCase struct {
	Name    string  `yaml:"name"`
	Method  string  `yaml:"method"`
	URL     string  `yaml:"url"`
	Created []int64 `yaml:"created"`
	Call    string  `yaml:"call"`
}

type publishFixture struct {
	RequiredNames       []string           `yaml:"required_names"`
	Assets              []publishAssetFile `yaml:"assets"`
	Notes               string             `yaml:"notes"`
	Cases               []publishCase      `yaml:"cases"`
	PermitRequiredNames []string           `yaml:"permit_required_names"`
	PermitCases         []permitCase       `yaml:"permit_cases"`
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
	return f
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
	requests []string
	server   *httptest.Server
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return intent.AssetDigestPrefix + hex.EncodeToString(sum[:])
}

func newFakeReleaseServer(t *testing.T, cfg publishServer, local []publishAssetFile) *fakeReleaseServer {
	t.Helper()
	s := &fakeReleaseServer{t: t, cfg: cfg, tags: map[string]bool{}, nextID: fakeFirstID}
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
				rel.Assets = append(rel.Assets, fakeAsset{Name: a.Name, Size: int64(len(a.Content)), Digest: digest, State: intent.AssetStateUploaded})
			}
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
		w.Header().Set("Link", fmt.Sprintf(`<%s%s?per_page=%d&page=%d>; rel="next"`, s.server.URL, r.URL.Path, per, pg+1))
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
		s.page(w, r, len(rel.Assets), func(lo, hi int) { s.writeJSON(w, http.StatusOK, rel.Assets[lo:hi]) })
	case r.Method == http.MethodGet:
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
		rel.Draft, rel.Prerelease, rel.MakeLatest = false, body.Prerelease, body.MakeLatest
		rel.Immutable = s.cfg.ImmutableOnPublish
		s.writeJSON(w, http.StatusOK, rel)
	case r.Method == http.MethodDelete:
		if s.cfg.Faults.DeleteFail {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Server Error"})
			return
		}
		for i, x := range s.releases {
			if x.ID == id {
				s.releases = append(s.releases[:i], s.releases[i+1:]...)
				break
			}
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
	if name == f.UploadFail {
		rel.Assets = append(rel.Assets, fakeAsset{Name: name, Size: 0, State: "starter"})
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Bad Gateway"})
		return
	}
	asset := fakeAsset{Name: name, Size: int64(len(data)), Digest: digestOf(data), State: intent.AssetStateUploaded}
	if name == f.Corrupt {
		asset.Digest = digestOf(append(data, '?'))
	}
	if name != f.Drop {
		rel.Assets = append(rel.Assets, asset)
	}
	if f.Extra != "" && len(rel.Assets) == 1 {
		rel.Assets = append(rel.Assets, fakeAsset{Name: f.Extra, Size: 1, Digest: digestOf([]byte("x")), State: intent.AssetStateUploaded})
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
			if err := os.WriteFile(filepath.Join(dir, "dist", "release-notes.md"), []byte(f.Notes), 0o644); err != nil {
				t.Fatal(err)
			}
			api := newFakeReleaseServer(t, c.Input.Server, f.Assets)
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
			api.mu.Lock()
			defer api.mu.Unlock()
			if c.Expected.NoRequests && len(api.requests) != 0 {
				t.Fatalf("the run sent %d requests before refusing: %v", len(api.requests), api.requests)
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
			for _, id := range c.Created {
				w.created[id] = true
			}
			call, err := w.permit(c.Method, c.URL)
			if c.Call == "" {
				if err == nil {
					t.Fatalf("permit(%s %s) = %s, want a refusal", c.Method, c.URL, call)
				}
				if strings.Contains(err.Error(), fakeReleaseToken) {
					t.Fatalf("the refusal carries the token")
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
