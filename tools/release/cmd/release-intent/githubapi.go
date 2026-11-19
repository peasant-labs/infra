package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// The GitHub REST reader behind inherit-verdict. It is the tool's only
// network seam: read-only GETs over the standard library, no client library,
// and no other command uses it. Every failure is returned as an error, which
// the command turns into exit 1, so an API fault is never a verdict.

const (
	defaultGitHubAPI   = "https://api.github.com"
	githubAPIEnv       = "GITHUB_API_URL"
	githubPageSize     = "100"
	githubHTTPTimeout  = 30 * time.Second
	githubMaxPages     = 50
	githubAcceptHeader = "application/vnd.github+json"
	githubAPIVersion   = "2022-11-28"
	// githubMaxResponse caps one response body; a larger one is an error.
	githubMaxResponse = 32 << 20
)

// githubTokenEnvs are read in order; the first set one authenticates.
var githubTokenEnvs = []string{"GH_TOKEN", "GITHUB_TOKEN"}

// repoPattern is an OWNER/NAME repository as GitHub spells one.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$`)

// githubReader implements intent.BaseVerdictReader over the REST API. It
// caches each object so a run read for one check run is not read again.
type githubReader struct {
	base   string
	repo   string
	token  string
	client *http.Client
	jobs   map[int64]intent.JobFact
	runs   map[int64]intent.RunFact
}

func newGitHubReader(apiBase, repo, token string, client *http.Client) (*githubReader, error) {
	if !repoPattern.MatchString(repo) {
		return nil, usageErr("%s %s: %q is not an OWNER/NAME repository. Pass %s \"$GITHUB_REPOSITORY\"", intent.CommandInheritVerdict, intent.FlagRepo, repo, intent.FlagRepo)
	}
	parsed, err := url.Parse(apiBase)
	if err != nil || parsed.Host == "" || !(parsed.Scheme == "https" || (parsed.Scheme == "http" && isLoopback(parsed.Hostname()))) {
		return nil, usageErr("%s: $%s %q is not an https URL (plain http is allowed only for a loopback test server); the token must not travel in clear text. Unset it to use %s", intent.CommandInheritVerdict, githubAPIEnv, apiBase, defaultGitHubAPI)
	}
	return &githubReader{
		base: strings.TrimSuffix(apiBase, "/"), repo: repo, token: token, client: client,
		jobs: map[int64]intent.JobFact{}, runs: map[int64]intent.RunFact{},
	}, nil
}

type apiCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	App        *struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

type apiStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type apiJob struct {
	ID         int64     `json:"id"`
	RunID      int64     `json:"run_id"`
	RunAttempt int       `json:"run_attempt"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Steps      []apiStep `json:"steps"`
}

type apiRun struct {
	ID         int64  `json:"id"`
	Event      string `json:"event"`
	Path       string `json:"path"`
	HeadSHA    string `json:"head_sha"`
	RunAttempt int    `json:"run_attempt"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func (j apiJob) fact() intent.JobFact {
	steps := make([]intent.StepFact, 0, len(j.Steps))
	for _, s := range j.Steps {
		steps = append(steps, intent.StepFact(s))
	}
	return intent.JobFact{ID: j.ID, RunID: j.RunID, RunAttempt: j.RunAttempt, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, Steps: steps}
}

// CheckRuns lists every check run on the commit, every attempt included.
func (g *githubReader) CheckRuns(sha string) ([]intent.CheckRunFact, error) {
	var out []intent.CheckRunFact
	err := g.pages(fmt.Sprintf("/repos/%s/commits/%s/check-runs?filter=all&per_page=%s", g.repo, url.PathEscape(sha), githubPageSize), func(body []byte) error {
		var page struct {
			CheckRuns []apiCheckRun `json:"check_runs"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return err
		}
		for _, r := range page.CheckRuns {
			app := ""
			if r.App != nil {
				app = r.App.Slug
			}
			out = append(out, intent.CheckRunFact{ID: r.ID, Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, App: app})
		}
		return nil
	})
	return out, err
}

// Job reads one Actions job by its check run ID.
func (g *githubReader) Job(id int64) (intent.JobFact, error) {
	if job, ok := g.jobs[id]; ok {
		return job, nil
	}
	var job apiJob
	if err := g.getJSON(fmt.Sprintf("/repos/%s/actions/jobs/%d", g.repo, id), &job); err != nil {
		return intent.JobFact{}, err
	}
	fact := job.fact()
	g.jobs[id] = fact
	return fact, nil
}

// Run reads one workflow run as of its latest attempt.
func (g *githubReader) Run(id int64) (intent.RunFact, error) {
	if run, ok := g.runs[id]; ok {
		return run, nil
	}
	var run apiRun
	if err := g.getJSON(fmt.Sprintf("/repos/%s/actions/runs/%d", g.repo, id), &run); err != nil {
		return intent.RunFact{}, err
	}
	fact := intent.RunFact(run)
	g.runs[id] = fact
	return fact, nil
}

// RunJobs lists the jobs of every attempt of the run.
func (g *githubReader) RunJobs(id int64) ([]intent.JobFact, error) {
	var out []intent.JobFact
	err := g.pages(fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?filter=all&per_page=%s", g.repo, id, githubPageSize), func(body []byte) error {
		var page struct {
			Jobs []apiJob `json:"jobs"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return err
		}
		for _, j := range page.Jobs {
			out = append(out, j.fact())
		}
		return nil
	})
	return out, err
}

// forget drops cached objects so a wait loop reads fresh state.
func (g *githubReader) forget() {
	g.jobs = map[int64]intent.JobFact{}
	g.runs = map[int64]intent.RunFact{}
}

func (g *githubReader) getJSON(path string, out any) error {
	body, _, err := g.get(g.base + path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GitHub API GET %s: cannot decode the response: %w", path, err)
	}
	return nil
}

// pages follows the Link header's next relation from the first page. A next
// URL off the API host is refused, so the token never leaves it.
func (g *githubReader) pages(path string, each func([]byte) error) error {
	next := g.base + path
	for page := 0; next != ""; page++ {
		if page == githubMaxPages {
			return fmt.Errorf("GitHub API GET %s: more than %d pages; refusing to read further. A commit with that many check runs needs a manual look", path, githubMaxPages)
		}
		if !strings.HasPrefix(next, g.base+"/") {
			return fmt.Errorf("GitHub API GET %s: the next page %q is not under %s; refusing to follow it", path, next, g.base)
		}
		body, link, err := g.get(next)
		if err != nil {
			return err
		}
		if err := each(body); err != nil {
			return fmt.Errorf("GitHub API GET %s: cannot decode page %d: %w", path, page+1, err)
		}
		next = nextLink(link)
	}
	return nil
}

func (g *githubReader) get(target string) ([]byte, []string, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", githubAcceptHeader)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("GitHub API GET %s: %w. Check the network and $%s", req.URL.Path, err, githubAPIEnv)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxResponse+1))
	if err != nil {
		return nil, nil, fmt.Errorf("GitHub API GET %s: cannot read the response: %w", req.URL.Path, err)
	}
	if len(body) > githubMaxResponse {
		return nil, nil, fmt.Errorf("GitHub API GET %s: the response exceeds the %d MiB cap; refusing to read a truncated answer. A response that large is not a check-run or job page; check $%s", req.URL.Path, githubMaxResponse>>20, githubAPIEnv)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GitHub API GET %s: %s. The job needs a token (GH_TOKEN) with checks: read and actions: read on %s", req.URL.Path, resp.Status, g.repo)
	}
	return body, resp.Header.Values("Link"), nil
}

// isLoopback reports whether a host is the local machine, where a test
// server may listen on plain http.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// nextLink returns the rel="next" URL of a Link header, or "".
func nextLink(headers []string) string {
	for _, header := range headers {
		for _, link := range strings.Split(header, ",") {
			sections := strings.Split(link, ";")
			if len(sections) < 2 {
				continue
			}
			for _, param := range sections[1:] {
				if strings.TrimSpace(param) == `rel="next"` {
					return strings.Trim(strings.TrimSpace(sections[0]), "<>")
				}
			}
		}
	}
	return ""
}

func githubToken(getenv func(string) string) string {
	for _, name := range githubTokenEnvs {
		if v := getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func githubAPIBase(getenv func(string) string) string {
	if v := getenv(githubAPIEnv); v != "" {
		return v
	}
	return defaultGitHubAPI
}
