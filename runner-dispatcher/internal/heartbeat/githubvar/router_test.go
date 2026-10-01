package githubvar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/heartbeat"
)

// The router workflow lives at the repository root; this package is four
// directories below it.
var routerWorkflow = filepath.Join("..", "..", "..", "..", ".github", "workflows", "runner-router.yml")

const (
	routerFallback = `["ubuntu-24.04"]`
	routerLabels   = `["self-hosted","linux","x64","microvm"]`
)

type routerCase struct {
	Name       string  `yaml:"name"`
	AgeSeconds int     `yaml:"ageSeconds"`
	Healthy    bool    `yaml:"healthy"`
	RawValue   *string `yaml:"rawValue"`
	QueryFails bool    `yaml:"queryFails"`
	Token      *string `yaml:"token"`
	Labels     *string `yaml:"labels"`
	WantPool   string  `yaml:"wantPool"`
	WantReason string  `yaml:"wantReason"`
}

// The branches every router change must keep covered.
var requiredRouterCases = []string{
	"fresh healthy record routes to the pool",
	"record older than the freshness window falls back",
	"fresh record with an unhealthy listener falls back",
	"failed variable read falls back",
}

// TestRouterReadsPublishedRecord runs the router's real "Pick a runner" script
// against records written by this package's publisher, so the writer's format
// and the reader's freshness and health rules cannot drift apart.
func TestRouterReadsPublishedRecord(t *testing.T) {
	script := routerScript(t)
	for _, tool := range []string{"bash", "jq", "timeout"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is required to run the router script: %v", tool, err)
			}
			t.Skipf("%s not installed; the router script cannot run here", tool)
		}
	}

	data, err := os.ReadFile("testdata/router.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var cases []routerCase
	if err := yaml.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	names := map[string]bool{}
	for _, tc := range cases {
		names[tc.Name] = true
	}
	for _, name := range requiredRouterCases {
		if !names[name] {
			t.Fatalf("fixture lost required case %q", name)
		}
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()
			value := publishedValue(t, tc)
			if tc.RawValue != nil {
				value = *tc.RawValue
			}
			valueFile := filepath.Join(dir, "value")
			if err := os.WriteFile(valueFile, []byte(value), 0o600); err != nil {
				t.Fatalf("write value: %v", err)
			}
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			// Stands in for `gh api .../RUNNER_POOL_HEALTH --jq .value`.
			fakeGH := "#!/usr/bin/env bash\n" +
				"if [ -n \"${FAKE_GH_FAIL:-}\" ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi\n" +
				"cat \"$FAKE_GH_VALUE\"\n"
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
				t.Fatalf("write fake gh: %v", err)
			}
			scriptFile := filepath.Join(dir, "route.sh")
			if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
				t.Fatalf("write script: %v", err)
			}
			outputFile := filepath.Join(dir, "github_output")

			token := "test-token"
			if tc.Token != nil {
				token = *tc.Token
			}
			labels := routerLabels
			if tc.Labels != nil {
				labels = *tc.Labels
			}
			cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", scriptFile)
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GH_TOKEN="+token,
				"FALLBACK="+routerFallback,
				"LABELS="+labels,
				"GITHUB_OUTPUT="+outputFile,
				"FAKE_GH_VALUE="+valueFile,
			)
			if tc.QueryFails {
				cmd.Env = append(cmd.Env, "FAKE_GH_FAIL=1")
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("router script failed: %v\n%s", err, out)
			}

			outputs := readGitHubOutput(t, outputFile)
			if outputs["pool"] != tc.WantPool || outputs["reason"] != tc.WantReason {
				t.Fatalf("pool=%q reason=%q, want pool=%q reason=%q", outputs["pool"], outputs["reason"], tc.WantPool, tc.WantReason)
			}
			wantRunner := routerFallback
			if tc.WantPool == "true" {
				wantRunner = labels
			}
			if outputs["runner"] != wantRunner {
				t.Fatalf("runner=%q, want %q", outputs["runner"], wantRunner)
			}
		})
	}
}

// routerScript extracts the production script text from the workflow.
func routerScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(routerWorkflow)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("router workflow not in this source tree")
		}
		t.Fatalf("read workflow: %v", err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	for _, step := range wf.Jobs["pick"].Steps {
		if step.Name == "Pick a runner" {
			return step.Run
		}
	}
	t.Fatal(`workflow has no "Pick a runner" step in job "pick"`)
	return ""
}

// publishedValue publishes a record through this package's publisher and
// returns the variable value it sent.
func publishedValue(t *testing.T, tc routerCase) string {
	t.Helper()
	var sent struct {
		Value string `json:"value"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Errorf("decode publish body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	pub, err := New(Config{Repo: "o/r", Variable: "HEALTH", Tokens: tokenStub{}, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	record := heartbeat.Record{
		Timestamp:       time.Now().Add(-time.Duration(tc.AgeSeconds) * time.Second),
		ListenerHealthy: tc.Healthy,
	}
	if err := pub.Publish(context.Background(), record); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return sent.Value
}

// readGitHubOutput parses a GITHUB_OUTPUT file: key=value lines and
// key<<DELIM heredocs. The last write of a key wins, as on GitHub.
func readGitHubOutput(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read outputs: %v", err)
	}
	out := map[string]string{}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if key, delim, ok := strings.Cut(lines[i], "<<"); ok {
			var value []string
			for i++; i < len(lines) && lines[i] != delim; i++ {
				value = append(value, lines[i])
			}
			out[key] = strings.Join(value, "\n")
			continue
		}
		if key, value, ok := strings.Cut(lines[i], "="); ok {
			out[key] = value
		}
	}
	return out
}
