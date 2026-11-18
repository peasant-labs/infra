package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseCorrectionMutations runs the same real-CLI preservation witness
// under isolated source overlays. The subprocess selects one witness and one
// control, so neither the worktree nor unrelated test cases are mutated.
func TestReleaseCorrectionMutations(t *testing.T) {
	data, err := cliFixtureYAML.ReadFile(fixtureDir + "/release-mutations.yaml")
	if err != nil {
		t.Fatalf("read release mutation fixture: %v", err)
	}
	var fixture struct {
		RequiredNames []string `yaml:"required_names"`
		Mutations     []struct {
			Name        string `yaml:"name"`
			Source      string `yaml:"source"`
			Find        string `yaml:"find"`
			Replace     string `yaml:"replace"`
			Witness     string `yaml:"witness"`
			FailureText string `yaml:"failure_text"`
			Control     string `yaml:"control"`
		} `yaml:"mutations"`
	}
	if err := strictDecode(data, &fixture); err != nil {
		t.Fatalf("decode release mutation fixture: %v", err)
	}
	seen := map[string]struct{}{}
	for _, m := range fixture.Mutations {
		if _, duplicate := seen[m.Name]; duplicate {
			t.Fatalf("duplicate mutation %q", m.Name)
		}
		seen[m.Name] = struct{}{}
		t.Run(m.Name, func(t *testing.T) {
			source, err := filepath.Abs(m.Source)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(source)
			if err != nil {
				t.Fatalf("read mutation source: %v", err)
			}
			if count := strings.Count(string(original), m.Find); m.Find == "" || count != 1 {
				t.Fatalf("mutation anchor occurs %d times, want exactly one", count)
			}
			dir := t.TempDir()
			overlaySource := filepath.Join(dir, filepath.Base(source))
			if err := os.WriteFile(overlaySource, []byte(strings.Replace(string(original), m.Find, m.Replace, 1)), 0o600); err != nil {
				t.Fatalf("write overlay source: %v", err)
			}
			overlay, err := json.Marshal(struct {
				Replace map[string]string
			}{Replace: map[string]string{source: overlaySource}})
			if err != nil {
				t.Fatal(err)
			}
			overlayPath := filepath.Join(dir, "overlay.json")
			if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
				t.Fatalf("write overlay: %v", err)
			}
			run := func(testPattern string) ([]byte, error) {
				cmd := exec.Command("go", "test", ".", "-run", testPattern, "-count=1", "-v")
				for _, env := range os.Environ() {
					if !strings.HasPrefix(env, "GOFLAGS=") {
						cmd.Env = append(cmd.Env, env)
					}
				}
				cmd.Env = append(cmd.Env, "GOFLAGS=-mod=readonly -overlay="+overlayPath)
				return cmd.CombinedOutput()
			}
			control, err := run(m.Control)
			if err != nil || !strings.Contains(string(control), "--- PASS: TestCLIExitCodes/check-title-accept") {
				t.Fatalf("mutation control failed or did not run: %v\n%s", err, control)
			}
			witness, err := run(m.Witness)
			if err == nil || !strings.Contains(string(witness), "--- FAIL: TestCorrectionPreservation/reviewed-correction-write-and-tag") ||
				!strings.Contains(string(witness), m.FailureText) {
				t.Fatalf("mutation did not fail the intended witness for %q: %v\n%s", m.FailureText, err, witness)
			}
		})
	}
	checkRequiredNames(t, fixture.RequiredNames, seen, "release mutation fixture")
}
