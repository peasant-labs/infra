package main

import (
	"context"
	_ "embed"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/policy"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/inherit-verdict.yaml
var inheritVerdictFixtureYAML []byte

type inheritResponse struct {
	Status  int    `yaml:"status"`
	Body    string `yaml:"body"`
	Next    string `yaml:"next"`
	NextURL string `yaml:"next_url"`
}

type inheritRoute struct {
	Path      string            `yaml:"path"`
	Responses []inheritResponse `yaml:"responses"`
}

type inheritEdit struct {
	Op      string `yaml:"op"`
	Path    string `yaml:"path"`
	To      string `yaml:"to"`
	Value   string `yaml:"value"`
	Key     string `yaml:"key"`
	Text    string `yaml:"text"`
	Content string `yaml:"content"`
}

type inheritCase struct {
	Name  string `yaml:"name"`
	Input struct {
		Edits                []inheritEdit `yaml:"edits"`
		Routes               string        `yaml:"routes"`
		PolicyWithoutInherit bool          `yaml:"policy_without_inherit"`
	} `yaml:"input"`
	Expected struct {
		Exit   int      `yaml:"exit"`
		Lines  []string `yaml:"lines"`
		Stderr []string `yaml:"stderr"`
	} `yaml:"expected"`
	Sibling        string                  `yaml:"sibling"`
	Classification testcase.Classification `yaml:"classification"`
	Provenance     testcase.Provenance     `yaml:"provenance"`
	Mutation       testcase.Mutation       `yaml:"mutation"`
}

type inheritFixture struct {
	RequiredNames        []string                  `yaml:"required_names"`
	Repo                 string                    `yaml:"repo"`
	Policy               string                    `yaml:"policy"`
	PolicyWithoutInherit string                    `yaml:"policy_without_inherit"`
	WorkflowPath         string                    `yaml:"workflow_path"`
	Workflow             string                    `yaml:"workflow"`
	Routes               map[string][]inheritRoute `yaml:"routes"`
	Cases                []inheritCase             `yaml:"cases"`
}

func loadInheritFixture(t *testing.T) inheritFixture {
	t.Helper()
	var f inheritFixture
	if err := strictDecode(inheritVerdictFixtureYAML, &f); err != nil {
		t.Fatalf("inherit-verdict fixture: %v", err)
	}
	seen := map[string]struct{}{}
	byName := map[string]inheritCase{}
	for _, c := range f.Cases {
		if _, dup := seen[c.Name]; dup {
			t.Fatalf("inherit-verdict fixture: case %q appears twice", c.Name)
		}
		seen[c.Name] = struct{}{}
		byName[c.Name] = c
		if !c.Classification.IsValid() || !c.Provenance.Source.IsValid() || c.Provenance.Ref == "" || c.Mutation.Description == "" {
			t.Fatalf("inherit-verdict fixture: case %q is under-populated", c.Name)
		}
		if _, ok := f.Routes[c.Input.Routes]; !ok {
			t.Fatalf("inherit-verdict fixture: case %q names unknown route set %q", c.Name, c.Input.Routes)
		}
	}
	for _, c := range f.Cases {
		if c.Classification != testcase.MustFail {
			continue
		}
		if sib, ok := byName[c.Sibling]; !ok || sib.Classification != testcase.MustPass {
			t.Fatalf("inherit-verdict fixture: reject %q needs an accept sibling, got %q", c.Name, c.Sibling)
		}
	}
	checkRequiredNames(t, f.RequiredNames, seen, "inherit-verdict fixture")
	return f
}

// fakeGitHub serves one route set. Each route's responses are served in
// order and the last repeats; the base commit fills {{base}}.
type fakeGitHub struct {
	mu     sync.Mutex
	routes map[string][]inheritResponse
	served map[string]int
	base   string
	server *httptest.Server
}

func newFakeGitHub(t *testing.T, routes []inheritRoute, base string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{routes: map[string][]inheritResponse{}, served: map[string]int{}, base: base}
	for _, r := range routes {
		g.routes[g.fill(r.Path)] = r.Responses
	}
	g.server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGitHub) fill(s string) string { return strings.ReplaceAll(s, "{{base}}", g.base) }

func (g *fakeGitHub) serve(w http.ResponseWriter, req *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := req.URL.RequestURI()
	responses, ok := g.routes[key]
	if !ok || len(responses) == 0 {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	i := g.served[key]
	if i >= len(responses) {
		i = len(responses) - 1
	}
	g.served[key]++
	resp := responses[i]
	switch {
	case resp.Next != "":
		w.Header().Set("Link", "<"+g.server.URL+g.fill(resp.Next)+`>; rel="next", <`+g.server.URL+`/last>; rel="last"`)
	case resp.NextURL != "":
		w.Header().Set("Link", "<"+g.fill(resp.NextURL)+`>; rel="next"`)
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write([]byte(g.fill(resp.Body)))
}

// buildInheritRepo commits the base (config, policy, workflow, changelog,
// version file) and then the case's head edits. It returns base and head.
func buildInheritRepo(t *testing.T, f inheritFixture, c inheritCase) (*testRepo, string, string) {
	t.Helper()
	r := newTestRepo(t, repoStandard)
	r.Seed()
	pol := f.Policy
	if c.Input.PolicyWithoutInherit {
		pol = f.PolicyWithoutInherit
	}
	r.WriteFile(policy.DefaultPath, pol)
	r.WriteFile(f.WorkflowPath, f.Workflow)
	base := r.CommitRaw("chore: base")
	for _, e := range c.Input.Edits {
		switch e.Op {
		case "version":
			r.SetVersionField(0, intent.SemVer(e.Value))
		case "ordinary":
			r.SetOrdinaryField(0, e.Key, e.Value)
		case "raw-version":
			r.WriteVersionDocument(0, e.Content)
		case "append-changelog":
			r.AppendChangelog(e.Text)
		case "write":
			r.WriteFile(e.Path, e.Content)
		case "rename":
			r.Rename(e.Path, e.To)
		case "chmod-exec":
			if err := os.Chmod(filepath.Join(r.Dir(), e.Path), 0o755); err != nil {
				t.Fatalf("chmod %s: %v", e.Path, err)
			}
		default:
			t.Fatalf("case %q: unknown edit op %q", c.Name, e.Op)
		}
	}
	head := r.CommitRaw("chore: head")
	return r, base, head
}

// TestInheritVerdict_Process runs the built binary: flag parsing, the git
// tree diff, the base-revision config and policy, the HTTP reader, the
// machine lines and the exit status, against a local API.
func TestInheritVerdict_Process(t *testing.T) {
	f := loadInheritFixture(t)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			r, base, head := buildInheritRepo(t, f, c)
			api := newFakeGitHub(t, f.Routes[c.Input.Routes], base)
			outFile := filepath.Join(t.TempDir(), "github-output")
			if err := os.WriteFile(outFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			res := runBinary(t, r.Dir(), []string{intent.CommandInheritVerdict.String(),
				intent.FlagBase.String(), base, intent.FlagHead.String(), head, intent.FlagRepo.String(), f.Repo},
				"", []string{githubAPIEnv + "=" + api.server.URL, "GH_TOKEN=test-token", githubOutputEnv + "=" + outFile})
			if int(res.Exit) != c.Expected.Exit {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", res.Exit, c.Expected.Exit, res.Stdout, res.Stderr)
			}
			var want []string
			for _, line := range c.Expected.Lines {
				want = append(want, api.fill(line))
			}
			got := strings.Fields(res.Stdout)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("stdout lines = %q, want %q\nstderr: %s", got, want, res.Stderr)
			}
			written, err := os.ReadFile(outFile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(strings.Fields(string(written)), "\n") != strings.Join(want, "\n") {
				t.Fatalf("step outputs = %q, want %q", written, want)
			}
			for _, frag := range c.Expected.Stderr {
				if !strings.Contains(res.Stderr, api.fill(frag)) {
					t.Fatalf("stderr does not contain %q:\n%s", api.fill(frag), res.Stderr)
				}
			}
		})
	}
}

// TestInheritVerdict_WaitsForPendingBase drives the wait loop in process
// with a fake clock: the first read finds the base's queue run in progress,
// the loop sleeps one interval, and the second read inherits. With no wait
// the same pending read fails closed.
func TestInheritVerdict_WaitsForPendingBase(t *testing.T) {
	f := loadInheritFixture(t)
	release := f.Cases[0]
	if release.Name != "release-pr-inherits" {
		t.Fatalf("the first case must be the release accept row, got %q", release.Name)
	}
	for _, tc := range []struct {
		wait      time.Duration
		wantErr   intent.ErrorCode
		wantSleep int
	}{{wait: 20 * time.Minute, wantSleep: 1}, {wait: 0, wantErr: intent.ErrBaseVerdictPending}} {
		r, base, head := buildInheritRepo(t, f, release)
		api := newFakeGitHub(t, f.Routes["verdict-pending-then-full"], base)
		reader, err := newGitHubReader(api.server.URL, f.Repo, "test-token", api.server.Client())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
		sleeps := 0
		clock := inheritClock{now: func() time.Time { return now }, sleep: func(d time.Duration) { sleeps++; now = now.Add(d) }}
		t.Setenv(githubOutputEnv, "")
		err = inheritVerdict(context.Background(), r.Runner().(*execGit), reader, base, head, tc.wait, clock)
		if sleeps != tc.wantSleep {
			t.Fatalf("wait %s: slept %d times, want %d", tc.wait, sleeps, tc.wantSleep)
		}
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("wait %s: %v", tc.wait, err)
			}
			continue
		}
		rej, ok := asReject(err)
		if !ok || rej.Err.Code != tc.wantErr {
			t.Fatalf("wait %s: err = %v, want rejection %s", tc.wait, err, tc.wantErr)
		}
	}
}

func TestParseRawDiff(t *testing.T) {
	entries, err := parseRawDiff(":100644 100755 aaaa bbbb M\x00CHANGELOG.md\x00:000000 100644 0000 cccc A\x00new file\x00")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Status != intent.TreeChangeModified || entries[0].OldMode == entries[0].NewMode || entries[1].Path != "new file" || entries[1].Status != intent.TreeChangeAdded {
		t.Fatalf("parseRawDiff = %+v", entries)
	}
	if _, err := parseRawDiff(":100644 100644 aaaa bbbb\x00x\x00"); err == nil {
		t.Fatal("parseRawDiff accepted a record with no status")
	}
}
