package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	regexp "github.com/wasilibs/go-re2"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// The GitHub REST writer behind publish-release. It is the tool's only seam
// that writes to the network. Every request passes permit first, which maps
// it to one member of a closed allow-list of release calls on the --repo
// repository and the --tag tag, and refuses anything else before it is
// sent. Updating, uploading to and deleting a release are allowed only for a
// release this writer created in this run, so the command can never change a
// release it did not make. The transport rules are the reader's: https only
// (plain http only for a loopback test server), no client library, a
// response size cap, and the token only in the Authorization header.

// releaseCall is one request the writer may send. The zero value is none.
type releaseCall string

const (
	callListReleases  releaseCall = "list-releases"
	callReadTagRef    releaseCall = "read-tag-ref"
	callCreateRelease releaseCall = "create-release"
	callUploadAsset   releaseCall = "upload-asset"
	callReadRelease   releaseCall = "read-release"
	callListAssets    releaseCall = "list-assets"
	callUpdateRelease releaseCall = "update-release"
	callDeleteRelease releaseCall = "delete-release"
)

// allReleaseCalls is the closed allow-list, in the order a publish makes
// them.
var allReleaseCalls = []releaseCall{
	callListReleases, callReadTagRef, callCreateRelease, callUploadAsset,
	callReadRelease, callListAssets, callUpdateRelease, callDeleteRelease,
}

func (c releaseCall) String() string { return string(c) }

// method is the one HTTP method of the call.
func (c releaseCall) method() string {
	switch c {
	case callCreateRelease, callUploadAsset:
		return http.MethodPost
	case callUpdateRelease:
		return http.MethodPatch
	case callDeleteRelease:
		return http.MethodDelete
	}
	return http.MethodGet
}

// ownReleaseOnly reports whether the call may only name a release this run
// created.
func (c releaseCall) ownReleaseOnly() bool {
	return c == callUploadAsset || c == callUpdateRelease || c == callDeleteRelease
}

const (
	// githubUploadsAPI is where github.com takes release asset uploads.
	githubUploadsAPI = "https://uploads.github.com"
	// releaseListPageSize is smaller than the reader's page: a release
	// object embeds its assets, and one schema release with 352 assets is
	// about 580 KiB, so 100 of them would pass the response cap.
	releaseListPageSize = 30
	assetListPageSize   = 100
	// apiCallTimeout bounds one API request; uploadTimeout one upload.
	apiCallTimeout = 60 * time.Second
	uploadTimeout  = 15 * time.Minute
)

var (
	releaseIDPath = regexp.MustCompile(`^/releases/([1-9][0-9]{0,18})(/assets)?$`)
	// pageQuery is the only query a listing may carry.
	pageQuery = regexp.MustCompile(`^per_page=[0-9]{1,3}(&page=[1-9][0-9]{0,4})?$`)
)

// releaseWriter talks to the release endpoints of one repository, for one
// tag.
type releaseWriter struct {
	api, uploads string
	repo, tag    string
	token        string
	client       *http.Client
	created      map[int64]bool
}

// newReleaseWriter accepts github.com's API, or a loopback test server that
// also takes the uploads. Any other API base is refused: the uploads host of
// another installation cannot be derived from it.
func newReleaseWriter(apiBase, repo, tag, token string, client *http.Client) (*releaseWriter, error) {
	if !repoPattern.MatchString(repo) {
		return nil, usageErr("%s %s: %q is not an OWNER/NAME repository. Pass %s \"$GITHUB_REPOSITORY\"", intent.CommandPublishRelease, intent.FlagRepo, repo, intent.FlagRepo)
	}
	if !releaseTagPattern.MatchString(tag) || strings.Contains(tag, "..") {
		return nil, usageErr("%s %s: %q is not a tag name the command publishes (letters, digits, '.', '_', '+', '-' and '/', starting with a letter or digit). Pass %s \"$TAG\" mapped from github.ref_name", intent.CommandPublishRelease, intent.FlagTag, tag, intent.FlagTag)
	}
	if token == "" {
		return nil, usageErr("%s: no token in $GH_TOKEN or $GITHUB_TOKEN. The publish step needs GH_TOKEN: ${{ github.token }} with contents: write", intent.CommandPublishRelease)
	}
	base := strings.TrimSuffix(apiBase, "/")
	uploads := ""
	parsed, err := url.Parse(base)
	switch {
	case err != nil:
	case base == defaultGitHubAPI:
		uploads = githubUploadsAPI
	case parsed.Scheme == "http" && isLoopback(parsed.Hostname()) && parsed.Path == "":
		uploads = base
	}
	if uploads == "" {
		return nil, usageErr("%s: $%s %q is not %s or a plain-http loopback test server; the command publishes only to github.com, whose uploads host it knows, and never sends the token in clear text elsewhere", intent.CommandPublishRelease, githubAPIEnv, apiBase, defaultGitHubAPI)
	}
	return &releaseWriter{api: base, uploads: uploads, repo: repo, tag: tag, token: token, client: client, created: map[int64]bool{}}, nil
}

// releaseTagPattern is a tag the command can name in a URL path unescaped.
var releaseTagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/-]*$`)

// permit maps one request to the allow-listed call it is, or refuses it.
// Only this function decides what the writer may send.
func (w *releaseWriter) permit(method, target string) (releaseCall, error) {
	refuse := func(why string) (releaseCall, error) {
		return "", fmt.Errorf("%s: refusing %s %s: %s. This is a programming error in %s: the writer sends only the release calls %v of %s", intent.CommandPublishRelease, method, redactQuery(target), why, intent.ProgramName, allReleaseCalls, w.repo)
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Fragment != "" {
		return refuse("the URL does not parse as a plain URL")
	}
	origin := u.Scheme + "://" + u.Host
	repoPrefix := "/repos/" + w.repo
	if !strings.HasPrefix(u.Path, repoPrefix+"/") {
		return refuse("the path is not under " + repoPrefix)
	}
	rest := strings.TrimPrefix(u.Path, repoPrefix)
	var call releaseCall
	var id int64
	m := releaseIDPath.FindStringSubmatch(rest)
	switch {
	case origin == w.uploads && method == http.MethodPost && m != nil && m[2] != "":
		name := u.Query().Get("name")
		if name == "" || u.RawQuery != "name="+url.QueryEscape(name) {
			return refuse("an upload carries exactly one name query")
		}
		call, id = callUploadAsset, mustID(m[1])
	case origin != w.api:
		return refuse("the host is not " + w.api + ", and " + w.uploads + " takes only POST /releases/<id>/assets")
	case rest == "/releases" && method == http.MethodGet:
		call = callListReleases
	case rest == "/releases" && method == http.MethodPost:
		call = callCreateRelease
	case rest == "/git/ref/tags/"+w.tag:
		call = callReadTagRef
	case m == nil:
		return refuse("the path is not a release endpoint")
	default:
		id = mustID(m[1])
		switch {
		case m[2] != "":
			call = callListAssets
		case method == http.MethodGet:
			call = callReadRelease
		case method == http.MethodPatch:
			call = callUpdateRelease
		case method == http.MethodDelete:
			call = callDeleteRelease
		}
	}
	if call == "" || call.method() != method {
		return refuse("the method does not belong to the endpoint")
	}
	if (call == callListReleases || call == callListAssets) && !pageQuery.MatchString(u.RawQuery) {
		return refuse("a listing carries only per_page and page")
	}
	if call != callListReleases && call != callListAssets && call != callUploadAsset && u.RawQuery != "" {
		return refuse("the call carries no query")
	}
	if call.ownReleaseOnly() && !w.created[id] {
		return refuse("release id " + strconv.FormatInt(id, 10) + " was not created by this run")
	}
	return call, nil
}

func mustID(s string) int64 {
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// redactQuery drops the query from a URL for a message.
func redactQuery(target string) string {
	if i := strings.IndexByte(target, '?'); i >= 0 {
		return target[:i]
	}
	return target
}

// apiResponse is one answer: status, body and the Link header.
type apiResponse struct {
	status int
	body   []byte
	link   []string
}

// send permits, then sends one request. The token goes only into the
// Authorization header; no message carries it.
func (w *releaseWriter) send(method, target string, body io.Reader, size int64, contentType string) (releaseCall, apiResponse, error) {
	call, err := w.permit(method, target)
	if err != nil {
		return "", apiResponse{}, err
	}
	timeout := apiCallTimeout
	if call == callUploadAsset {
		timeout = uploadTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return call, apiResponse{}, err
	}
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", githubAcceptHeader)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("Authorization", "Bearer "+w.token)
	resp, err := w.client.Do(req)
	if err != nil {
		return call, apiResponse{}, fmt.Errorf("GitHub API %s %s (%s): %w. Check the network and $%s", method, req.URL.Path, call, err, githubAPIEnv)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxResponse+1))
	if err != nil {
		return call, apiResponse{}, fmt.Errorf("GitHub API %s %s (%s): cannot read the response: %w", method, req.URL.Path, call, err)
	}
	if len(data) > githubMaxResponse {
		return call, apiResponse{}, fmt.Errorf("GitHub API %s %s (%s): the response exceeds the %d MiB cap; refusing to read a truncated answer", method, req.URL.Path, call, githubMaxResponse>>20)
	}
	return call, apiResponse{status: resp.StatusCode, body: data, link: resp.Header.Values("Link")}, nil
}

// expect sends a request and requires one status.
func (w *releaseWriter) expect(want int, method, target string, payload any) (apiResponse, error) {
	var body io.Reader
	var size int64
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return apiResponse{}, err
		}
		body, size = bytes.NewReader(data), int64(len(data))
	}
	call, resp, err := w.send(method, target, body, size, "application/json")
	if err != nil {
		return resp, err
	}
	if resp.status != want {
		return resp, w.statusError(call, method, target, resp)
	}
	return resp, nil
}

func (w *releaseWriter) statusError(call releaseCall, method, target string, resp apiResponse) error {
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(resp.body, &msg)
	detail := ""
	if msg.Message != "" {
		detail = " (" + strconv.Quote(truncateText(msg.Message, 200)) + ")"
	}
	return fmt.Errorf("GitHub API %s %s (%s): status %d%s. The publish job needs GH_TOKEN: ${{ github.token }} with contents: write on %s", method, redactQuery(target), call, resp.status, detail, w.repo)
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (w *releaseWriter) repoURL(path string) string { return w.api + "/repos/" + w.repo + path }

// apiRelease is the part of a release object the command reads.
type apiRelease struct {
	ID         int64  `json:"id"`
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Immutable  bool   `json:"immutable"`
}

func (r apiRelease) fact() intent.ReleaseFact {
	return intent.ReleaseFact{ID: r.ID, Tag: r.TagName, Draft: r.Draft, Prerelease: r.Prerelease, Immutable: r.Immutable}
}

// apiAsset is the part of a release asset the command reads.
type apiAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

// pages reads a listing page by page, building each page URL itself from
// the listing path, so every request stays inside the allow-list. GitHub's
// rel="next" link names the repository by its numeric id
// (/repositories/<id>/...), so it is read only as "another page follows"
// and never requested. At most githubMaxPages pages are read; truncated is
// true when another page remained at the cap.
func (w *releaseWriter) pages(listing string, perPage int, each func([]byte) error) (bool, error) {
	for page := 1; ; page++ {
		if page > githubMaxPages {
			return true, nil
		}
		target := listing + "?per_page=" + strconv.Itoa(perPage)
		if page > 1 {
			target += "&page=" + strconv.Itoa(page)
		}
		resp, err := w.expect(http.StatusOK, http.MethodGet, target, nil)
		if err != nil {
			return false, err
		}
		if err := each(resp.body); err != nil {
			return false, fmt.Errorf("GitHub API GET %s: cannot decode page %d: %w", listing, page, err)
		}
		if nextLink(resp.link) == "" {
			return false, nil
		}
	}
}

// ListReleases reads every release of the repository, drafts included.
func (w *releaseWriter) ListReleases() ([]intent.ReleaseFact, bool, error) {
	var out []intent.ReleaseFact
	truncated, err := w.pages(w.repoURL("/releases"), releaseListPageSize, func(body []byte) error {
		var page []apiRelease
		if err := json.Unmarshal(body, &page); err != nil {
			return err
		}
		for _, r := range page {
			out = append(out, r.fact())
		}
		return nil
	})
	return out, truncated, err
}

// ListAssets reads every asset of one release by id. A listing that would
// run past the page cap is an error: no release carries that many assets.
func (w *releaseWriter) ListAssets(id int64) ([]intent.ReleaseAssetFact, error) {
	var out []intent.ReleaseAssetFact
	truncated, err := w.pages(w.repoURL("/releases/"+strconv.FormatInt(id, 10)+"/assets"), assetListPageSize, func(body []byte) error {
		var page []apiAsset
		if err := json.Unmarshal(body, &page); err != nil {
			return err
		}
		for _, a := range page {
			out = append(out, intent.ReleaseAssetFact(a))
		}
		return nil
	})
	if err == nil && truncated {
		err = fmt.Errorf("GitHub API GET releases/%d/assets: more than %d pages of assets; refusing to judge a partial list", id, githubMaxPages)
	}
	return out, err
}

// TagExists reads refs/tags/<tag>: 200 means it exists, 404 that it does
// not.
func (w *releaseWriter) TagExists() (bool, error) {
	target := w.repoURL("/git/ref/tags/" + w.tag)
	call, resp, err := w.send(http.MethodGet, target, nil, 0, "")
	if err != nil {
		return false, err
	}
	switch resp.status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, w.statusError(call, http.MethodGet, target, resp)
}

// CreateDraft creates the tag's draft release and remembers its id as this
// run's own.
func (w *releaseWriter) CreateDraft(notes string, prerelease bool) (int64, error) {
	resp, err := w.expect(http.StatusCreated, http.MethodPost, w.repoURL("/releases"), map[string]any{
		"tag_name": w.tag, "name": w.tag, "body": notes, "draft": true, "prerelease": prerelease,
	})
	if err != nil {
		return 0, err
	}
	var r apiRelease
	if err := json.Unmarshal(resp.body, &r); err != nil || r.ID <= 0 {
		return 0, fmt.Errorf("GitHub API POST releases: the response names no release id; a draft for %s may exist: list the releases and delete it by id", w.tag)
	}
	if r.TagName != w.tag || !r.Draft {
		w.created[r.ID] = true
		return r.ID, fmt.Errorf("GitHub API POST releases: release id %d is for tag %q with draft=%t, not a draft for %q", r.ID, r.TagName, r.Draft, w.tag)
	}
	w.created[r.ID] = true
	return r.ID, nil
}

// Upload sends one file to this run's draft. The bytes are read from the
// file at upload time; the draft check compares what GitHub stored with the
// manifest built at the start.
func (w *releaseWriter) Upload(id int64, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: cannot open asset %s: %w", intent.CommandPublishRelease, path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	target := w.uploads + "/repos/" + w.repo + "/releases/" + strconv.FormatInt(id, 10) + "/assets?name=" + url.QueryEscape(name)
	call, resp, err := w.send(http.MethodPost, target, f, info.Size(), "application/octet-stream")
	if err != nil {
		return err
	}
	if resp.status != http.StatusCreated {
		return w.statusError(call, http.MethodPost, target, resp)
	}
	return nil
}

// ReadRelease reads one release by id.
func (w *releaseWriter) ReadRelease(id int64) (intent.ReleaseFact, error) {
	resp, err := w.expect(http.StatusOK, http.MethodGet, w.repoURL("/releases/"+strconv.FormatInt(id, 10)), nil)
	if err != nil {
		return intent.ReleaseFact{}, err
	}
	var r apiRelease
	if err := json.Unmarshal(resp.body, &r); err != nil {
		return intent.ReleaseFact{}, fmt.Errorf("GitHub API GET releases/%d: cannot decode the response: %w", id, err)
	}
	if r.ID != id {
		return intent.ReleaseFact{}, fmt.Errorf("GitHub API GET releases/%d: the response is release %d", id, r.ID)
	}
	return r.fact(), nil
}

// Publish turns this run's draft into the published release.
func (w *releaseWriter) Publish(id int64, prerelease, latest bool) error {
	_, err := w.expect(http.StatusOK, http.MethodPatch, w.repoURL("/releases/"+strconv.FormatInt(id, 10)), map[string]any{
		"draft": false, "prerelease": prerelease, "make_latest": strconv.FormatBool(latest),
	})
	return err
}

// DeleteOwn deletes a release this run created, by id.
func (w *releaseWriter) DeleteOwn(id int64) error {
	_, err := w.expect(http.StatusNoContent, http.MethodDelete, w.repoURL("/releases/"+strconv.FormatInt(id, 10)), nil)
	if err == nil {
		delete(w.created, id)
	}
	return err
}
