package githubvar

import (
	"context"
	"encoding/json"
	"fmt"
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

const routerFallback = `["ubuntu-24.04"]`

type routerCase struct {
	Name          string  `yaml:"name"`
	AgeSeconds    int     `yaml:"ageSeconds"`
	Healthy       bool    `yaml:"healthy"`
	AssignedJobs  int     `yaml:"assignedJobs"`
	MaxCapacity   int     `yaml:"maxCapacity"`
	RequiredSlots *string `yaml:"requiredSlots"`
	RawValue      *string `yaml:"rawValue"`
	QueryFails    bool    `yaml:"queryFails"`
	Token         *string `yaml:"token"`
	Labels        *string `yaml:"labels"`
	WantPool      string  `yaml:"wantPool"`
	WantReason    string  `yaml:"wantReason"`
	WantRunner    string  `yaml:"wantRunner"`
	WantNotice    *string `yaml:"wantNotice"`
}

// The branches every router change must keep covered.
var requiredRouterCases = []string{
	"fresh healthy record routes to the pool",
	"idle healthy pool uses the workflow microvm default",
	"explicit labels override the workflow default",
	"pool with free capacity routes to the pool",
	"full pool falls back to the fallback runner",
	"four required with two free slots falls back",
	"four required with three free slots falls back",
	"four required with four free slots routes to the pool",
	"six required with four slots idle falls back as exceeding capacity",
	"four required with no capacity routes to the pool",
	"one required with one free slot routes to the pool",
	"required_slots 0 falls back",
	"required_slots -1 falls back",
	"required_slots 4.5 falls back",
	"required_slots 04 falls back",
	"record older than the freshness window falls back",
	"fresh record with an unhealthy listener falls back",
	"failed variable read falls back",
	"missing token falls back",
	"invalid labels input falls back",
	"unparseable record falls back",
}

// TestRouterReadsPublishedRecord runs the router's real "Pick a runner" script
// against records written by this package's publisher, so the writer's format
// and the reader's freshness and health rules cannot drift apart.
func TestRouterReadsPublishedRecord(t *testing.T) {
	script, defaultLabels, requiredDefault := routerScript(t)
	for _, tool := range []string{"bash", "jq", "timeout"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s is required to run the router script: %v", tool, err)
			}
			t.Skipf("%s not installed; the router script cannot run here", tool)
		}
	}

	cases := loadRouterCases(t)
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			combined, failure := runAndCheck(script, defaultLabels, requiredDefault, tc)
			if failure != "" {
				t.Fatalf("%s\n%s", failure, combined)
			}
		})
	}
}

// runAndCheck executes one case and returns the script's output plus a
// non-empty failure description when the case does not pass.
func runAndCheck(script, defaultLabels, requiredDefault string, tc routerCase) (string, string) {
	labels := defaultLabels
	if tc.Labels != nil {
		labels = *tc.Labels
	}
	required := requiredDefault
	if tc.RequiredSlots != nil {
		required = *tc.RequiredSlots
	}
	outputs, combined, err := execRouterCase(script, labels, required, tc)
	if err != nil {
		return combined, err.Error()
	}
	if err := checkRouterOutputs(tc, labels, outputs); err != nil {
		return combined, err.Error()
	}
	if tc.WantNotice != nil && !strings.Contains(combined, *tc.WantNotice) {
		return combined, fmt.Sprintf("output lacks notice %q", *tc.WantNotice)
	}
	return combined, ""
}

// checkRouterOutputs compares parsed GITHUB_OUTPUT against a case's wants.
func checkRouterOutputs(tc routerCase, labels string, outputs map[string]string) error {
	if outputs["pool"] != tc.WantPool || outputs["reason"] != tc.WantReason {
		return fmt.Errorf("pool=%q reason=%q, want pool=%q reason=%q",
			outputs["pool"], outputs["reason"], tc.WantPool, tc.WantReason)
	}
	wantRunner := routerFallback
	if tc.WantPool == "true" {
		wantRunner = labels
	}
	if tc.WantRunner != "" {
		wantRunner = tc.WantRunner
	}
	if outputs["runner"] != wantRunner {
		return fmt.Errorf("runner=%q, want %q", outputs["runner"], wantRunner)
	}
	return nil
}

// execRouterCase runs the extracted script against one fixture case with a
// stub gh and returns the parsed outputs plus the script's combined output.
func execRouterCase(script, labels, required string, tc routerCase) (map[string]string, string, error) {
	dir, err := os.MkdirTemp("", "router-case-")
	if err != nil {
		return nil, "", fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	value, err := publishedValue(tc)
	if err != nil {
		return nil, "", err
	}
	if tc.RawValue != nil {
		value = *tc.RawValue
	}
	valueFile := filepath.Join(dir, "value")
	if err := os.WriteFile(valueFile, []byte(value), 0o600); err != nil {
		return nil, "", fmt.Errorf("write value: %w", err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		return nil, "", fmt.Errorf("mkdir: %w", err)
	}
	// Stands in for `gh api .../RUNNER_POOL_HEALTH --jq .value`.
	fakeGH := "#!/usr/bin/env bash\n" +
		"if [ -n \"${FAKE_GH_FAIL:-}\" ]; then echo 'gh: Not Found (HTTP 404)' >&2; exit 1; fi\n" +
		"cat \"$FAKE_GH_VALUE\"\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		return nil, "", fmt.Errorf("write fake gh: %w", err)
	}
	scriptFile := filepath.Join(dir, "route.sh")
	if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
		return nil, "", fmt.Errorf("write script: %w", err)
	}
	outputFile := filepath.Join(dir, "github_output")

	token := "test-token"
	if tc.Token != nil {
		token = *tc.Token
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", scriptFile)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_TOKEN="+token,
		"FALLBACK="+routerFallback,
		"LABELS="+labels,
		"REQUIRED_SLOTS="+required,
		"GITHUB_OUTPUT="+outputFile,
		"FAKE_GH_VALUE="+valueFile,
	)
	if tc.QueryFails {
		cmd.Env = append(cmd.Env, "FAKE_GH_FAIL=1")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(out), fmt.Errorf("router script failed: %w\n%s", err, out)
	}
	outputs, err := readGitHubOutput(outputFile)
	if err != nil {
		return nil, string(out), err
	}
	return outputs, string(out), nil
}

// loadRouterCases reads the case fixture and requires every branch in
// requiredRouterCases to be present by name.
func loadRouterCases(t *testing.T) []routerCase {
	t.Helper()
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
	return cases
}

// routerDecl is the router workflow's contract surface: the extracted
// script plus the input declaration the script depends on.
type routerDecl struct {
	text                 string
	script               string
	defaultLabels        string
	requiredSlotsType    string
	requiredSlotsDefault string
	routeEnv             map[string]string
}

// routerScript extracts the production script text from the workflow and
// asserts the input declaration the script depends on.
func routerScript(t *testing.T) (script, defaultLabels, requiredDefault string) {
	t.Helper()
	data, err := os.ReadFile(routerWorkflow)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("router workflow not in this source tree")
		}
		t.Fatalf("read workflow: %v", err)
	}
	decl := loadRouterDecl(t, string(data))
	if err := checkRouterDecl(decl); err != nil {
		t.Fatalf("router declaration: %v", err)
	}
	return decl.script, decl.defaultLabels, decl.requiredSlotsDefault
}

// loadRouterDecl parses workflow text and extracts the "Pick a runner"
// script. Structural problems are fatal: without the real step the test
// cannot run.
func loadRouterDecl(t *testing.T, text string) routerDecl {
	t.Helper()
	var wf struct {
		On struct {
			WorkflowCall struct {
				Inputs struct {
					Labels struct {
						Default string `yaml:"default"`
					} `yaml:"labels"`
					RequiredSlots struct {
						Type    string `yaml:"type"`
						Default any    `yaml:"default"`
					} `yaml:"required_slots"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(text), &wf); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	in := wf.On.WorkflowCall.Inputs
	def, ok := in.RequiredSlots.Default.(int)
	if !ok {
		t.Fatalf("workflow required_slots default is %v, want the number 1", in.RequiredSlots.Default)
	}
	for _, step := range wf.Jobs["pick"].Steps {
		if step.Name == "Pick a runner" {
			return routerDecl{
				text:                 text,
				script:               step.Run,
				defaultLabels:        in.Labels.Default,
				requiredSlotsType:    in.RequiredSlots.Type,
				requiredSlotsDefault: fmt.Sprintf("%d", def),
				routeEnv:             step.Env,
			}
		}
	}
	t.Fatal(`workflow has no "Pick a runner" step in job "pick"`)
	return routerDecl{}
}

// checkRouterDecl asserts the input declaration the route script depends
// on: required_slots is a number defaulting to 1, and the route step maps
// REQUIRED_SLOTS to exactly "${{ inputs.required_slots }}". Without that
// mapping every caller would fall back with reason query-failed.
func checkRouterDecl(d routerDecl) error {
	if d.defaultLabels == "" {
		return fmt.Errorf("workflow labels default is missing")
	}
	if d.requiredSlotsType != "number" {
		return fmt.Errorf("required_slots type is %q, want number", d.requiredSlotsType)
	}
	if d.requiredSlotsDefault != "1" {
		return fmt.Errorf("required_slots default is %q, want 1", d.requiredSlotsDefault)
	}
	if got := d.routeEnv["REQUIRED_SLOTS"]; got != "${{ inputs.required_slots }}" {
		return fmt.Errorf("route step REQUIRED_SLOTS env is %q, want ${{ inputs.required_slots }}", got)
	}
	return nil
}

// publishedValue publishes a record through this package's publisher and
// returns the variable value it sent.
func publishedValue(tc routerCase) (string, error) {
	var sent struct {
		Value string `json:"value"`
	}
	var pubErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &sent); err != nil {
			pubErr = fmt.Errorf("decode publish body: %w", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	pub, err := New(Config{Repo: "o/r", Variable: "HEALTH", Tokens: tokenStub{}, BaseURL: server.URL})
	if err != nil {
		return "", fmt.Errorf("New: %w", err)
	}
	record := heartbeat.Record{
		Timestamp:       time.Now().Add(-time.Duration(tc.AgeSeconds) * time.Second),
		ListenerHealthy: tc.Healthy,
		AssignedJobs:    tc.AssignedJobs,
		MaxCapacity:     tc.MaxCapacity,
	}
	if err := pub.Publish(context.Background(), record); err != nil {
		return "", fmt.Errorf("Publish: %w", err)
	}
	if pubErr != nil {
		return "", pubErr
	}
	return sent.Value, nil
}

// readGitHubOutput parses a GITHUB_OUTPUT file: key=value lines and
// key<<DELIM heredocs. The last write of a key wins, as on GitHub.
func readGitHubOutput(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read outputs: %w", err)
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
	return out, nil
}

// routerMutation is one named sabotage of the workflow file text. killedBy
// names the router.yaml case that must fail under the mutation, or the
// special value "declaration" when the mutation is killed by the input
// declaration assertion instead of by a case.
type routerMutation struct {
	Name     string `yaml:"name"`
	Old      string `yaml:"old"`
	New      string `yaml:"new"`
	KilledBy string `yaml:"killedBy"`
}

// TestRouterMutationsAreKilled applies each named mutation in
// testdata/router-mutations.yaml to the workflow file text, re-extracts
// the route script from the mutated text (and re-reads the declaration and
// env), runs every case, and requires the mutation's witness to fail.
func TestRouterMutationsAreKilled(t *testing.T) {
	data, err := os.ReadFile(routerWorkflow)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("router workflow not in this source tree")
		}
		t.Fatalf("read workflow: %v", err)
	}
	text := string(data)

	mutData, err := os.ReadFile("testdata/router-mutations.yaml")
	if err != nil {
		t.Fatalf("read mutations fixture: %v", err)
	}
	var mutDoc struct {
		Mutations             []routerMutation `yaml:"mutations"`
		ExpectedMutationNames []string         `yaml:"expectedMutationNames"`
	}
	if err := yaml.Unmarshal(mutData, &mutDoc); err != nil {
		t.Fatalf("parse mutations fixture: %v", err)
	}
	names := map[string]bool{}
	for _, m := range mutDoc.Mutations {
		names[m.Name] = true
	}
	for _, name := range mutDoc.ExpectedMutationNames {
		if !names[name] {
			t.Fatalf("fixture lost required mutation %q", name)
		}
	}
	if len(mutDoc.Mutations) != len(mutDoc.ExpectedMutationNames) {
		t.Fatalf("got %d mutations, manifest names %d",
			len(mutDoc.Mutations), len(mutDoc.ExpectedMutationNames))
	}

	cases := loadRouterCases(t)
	pristine := loadRouterDecl(t, text)
	if err := checkRouterDecl(pristine); err != nil {
		t.Fatalf("router declaration: %v", err)
	}

	for _, m := range mutDoc.Mutations {
		t.Run(m.Name, func(t *testing.T) {
			if n := strings.Count(text, m.Old); n != 1 {
				t.Fatalf("mutation pattern found %d times, want exactly 1: %q", n, m.Old)
			}
			decl := loadRouterDecl(t, strings.Replace(text, m.Old, m.New, 1))
			if m.KilledBy == "declaration" {
				if err := checkRouterDecl(decl); err == nil {
					t.Fatalf("mutation survived: declaration still valid")
				} else {
					t.Logf("killed by declaration: %v", err)
				}
				return
			}
			if err := checkRouterDecl(decl); err != nil {
				t.Fatalf("mutation broke the declaration instead of %q: %v", m.KilledBy, err)
			}
			found := false
			for _, tc := range cases {
				if tc.Name == m.KilledBy {
					found = true
				}
				_, failure := runAndCheck(decl.script, decl.defaultLabels, decl.requiredSlotsDefault, tc)
				if tc.Name == m.KilledBy {
					if failure == "" {
						t.Fatalf("mutation survived: %q passed", m.KilledBy)
					}
					t.Logf("killed by %q: %s", m.KilledBy, failure)
				}
			}
			if !found {
				t.Fatalf("mutation names unknown case %q", m.KilledBy)
			}
		})
	}
}
