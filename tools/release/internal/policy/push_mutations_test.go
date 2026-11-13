package policy

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// manifestNames holds a corpus to its required-name manifest: every case
// named once and listed, and every listed name present.
func manifestNames(t *testing.T, corpus string, required, names []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			t.Fatalf("%s: case name %q is empty or repeated", corpus, name)
		}
		seen[name] = true
		if !slices.Contains(required, name) {
			t.Fatalf("%s: case %q is not in required_names", corpus, name)
		}
	}
	for _, name := range required {
		if !seen[name] {
			t.Fatalf("%s: required case %q is missing", corpus, name)
		}
	}
}

//go:embed testdata/push_mutations.yaml
var pushMutationsFixtureYAML []byte

// pushMutation is one row of the push mutation corpus. Workflow names a
// file in the workflows directory, or File a path from the fixture root
// (the release config). Rules is the exact set that must fire (empty for an
// accepted change), and ConfigError the code the release config must fail
// with instead, for a change the config loader refuses before any rule runs.
type pushMutation struct {
	Name         string           `yaml:"name"`
	Workflow     string           `yaml:"workflow"`
	File         string           `yaml:"file"`
	Find         string           `yaml:"find"`
	Replace      string           `yaml:"replace"`
	Rules        []QueueRule      `yaml:"rules"`
	ConfigError  intent.ErrorCode `yaml:"config_error"`
	WantContains []string         `yaml:"want_contains"`
}

// TestPushTargetMutations copies the realistic consumer workflow set (the
// queue corpus base: a release job, two bot jobs, a guard and CI) into a temp
// dir, proves the copy passes, then makes one change per row and runs the
// production loaders and checker. A rejected change must fire exactly its
// listed rules, carrying the listed fragments; an accepted change must fire
// none. The rows cover each push form against each job class that may hold
// a credential, every form that pushed the default branch past the
// earlier reader, and the general steps a credential job may still run.
func TestPushTargetMutations(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string       `yaml:"required_names"`
		Cases         []pushMutation `yaml:"cases"`
	}
	if err := strictUnmarshal(pushMutationsFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/push_mutations.yaml: %v", err)
	}
	var names []string
	for _, c := range fx.Cases {
		names = append(names, c.Name)
	}
	manifestNames(t, "push_mutations.yaml", fx.RequiredNames, names)

	if diags, err := checkMutationTree(t, materializeQueueFixture(t, "")); err != nil || len(diags) != 0 {
		t.Fatalf("the unmutated copy does not pass: %v %+v", err, diags)
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runWorkflowMutation(t, "", tc)
		})
	}
}

// checkMutationTree runs the production loaders and checker over one
// materialized tree. A release config the loader refuses is returned as the
// error, for the rows that expect it.
func checkMutationTree(t *testing.T, dir string) ([]Diagnostic, error) {
	t.Helper()
	policy, err := LoadQueuePolicy(filepath.Join(dir, DefaultPath))
	if err != nil {
		t.Fatalf("policy does not load: %v", err)
	}
	cfg, err := intent.LoadConfig(filepath.Join(dir, intent.DefaultConfigPath))
	if err != nil {
		return nil, err
	}
	diags, _ := CheckWorkflows(policy, cfg, filepath.Join(dir, fixtureWorkflowsDir))
	return diags, nil
}

// runWorkflowMutation materializes the base plus the overlay, makes the
// row's one change, runs the checker, and holds the result to the row: the
// exact rule set (none for an accepted change), or the release config error,
// and every fragment.
func runWorkflowMutation(t *testing.T, overlay string, tc pushMutation) {
	t.Helper()
	if (tc.Workflow == "") == (tc.File == "") {
		t.Fatal("a mutation row names neither or both of workflow and file (fixture error)")
	}
	if (len(tc.Rules) == 0 && tc.ConfigError == "") != (len(tc.WantContains) == 0) {
		t.Fatal("a rejected row names no fragment, or an accepted row names one (fixture error)")
	}
	dir := materializeQueueFixture(t, overlay)
	file := filepath.Join(dir, fixtureWorkflowsDir, tc.Workflow)
	if tc.File != "" {
		file = filepath.Join(dir, tc.File)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), tc.Find); n != 1 {
		t.Fatalf("find %q occurs %d times in %s, want exactly once", tc.Find, n, file)
	}
	mutated := strings.Replace(string(data), tc.Find, tc.Replace, 1)
	if err := os.WriteFile(file, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	diags, err := checkMutationTree(t, dir)
	if tc.ConfigError != "" {
		var typed *intent.Error
		if !errors.As(err, &typed) || typed.Code != tc.ConfigError {
			t.Fatalf("release config error = %v, want code %s", err, tc.ConfigError)
		}
		for _, want := range tc.WantContains {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("release config error does not contain %q: %v", want, err)
			}
		}
		return
	}
	if err != nil {
		t.Fatalf("release config does not load: %v", err)
	}
	fired := map[QueueRule]bool{}
	var msgs []string
	for _, d := range diags {
		fired[d.Rule] = true
		msgs = append(msgs, d.Message)
	}
	joined := strings.Join(msgs, "\n")
	for _, rule := range AllQueueRules {
		if fired[rule] != slices.Contains(tc.Rules, rule) {
			t.Fatalf("rule %s fired = %v, want the exact set %v:\n%s", rule, fired[rule], tc.Rules, joined)
		}
	}
	for _, want := range tc.WantContains {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostics do not contain %q:\n%s", want, joined)
		}
	}
}

// strictUnmarshal decodes a corpus with unknown fields refused.
func strictUnmarshal(data []byte, out any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	return dec.Decode(out)
}
