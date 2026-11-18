package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestChangelogGuardMutations compiles the real command with isolated Go
// overlays, then runs the same fixture repositories against each binary. No
// source file or shared worktree is changed by a mutation.
func TestChangelogGuardMutations(t *testing.T) {
	data, err := cliFixtureYAML.ReadFile(fixtureDir + "/changelog-mutations.yaml")
	if err != nil {
		t.Fatalf("read changelog mutation fixture: %v", err)
	}
	var fixture struct {
		RequiredNames []string `yaml:"required_names"`
		Mutations     []struct {
			Name          string     `yaml:"name"`
			Find          string     `yaml:"find"`
			Replace       string     `yaml:"replace"`
			Witness       string     `yaml:"witness"`
			Control       string     `yaml:"control"`
			MutatedExit   wantExit   `yaml:"mutated_exit"`
			MutatedStdout []wantLine `yaml:"mutated_stdout"`
		} `yaml:"mutations"`
	}
	if err := strictDecode(data, &fixture); err != nil {
		t.Fatalf("decode changelog mutation fixture: %v", err)
	}
	cases := map[string]cliCase{}
	for _, c := range loadCLIFixtures(t) {
		cases[c.Name] = c
	}
	seen := map[string]struct{}{}
	source, err := filepath.Abs("../../internal/intent/protected.go")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read protected-path source: %v", err)
	}
	for _, m := range fixture.Mutations {
		if _, duplicate := seen[m.Name]; duplicate {
			t.Fatalf("duplicate mutation %q", m.Name)
		}
		seen[m.Name] = struct{}{}
		witness, ok := cases[m.Witness]
		if !ok {
			t.Fatalf("mutation %q: unknown witness %q", m.Name, m.Witness)
		}
		control, ok := cases[m.Control]
		if !ok {
			t.Fatalf("mutation %q: unknown control %q", m.Name, m.Control)
		}
		if m.MutatedExit.Status == witness.Expected.Exit.Status {
			t.Fatalf("mutation %q: witness has the same baseline and mutated status", m.Name)
		}
		t.Run(m.Name, func(t *testing.T) {
			if count := strings.Count(string(original), m.Find); m.Find == "" || count != 1 {
				t.Fatalf("mutation anchor occurs %d times, want exactly one", count)
			}
			assertMutationCase(t, testBinaryPath, witness, witness.Expected.Exit.Status, witness.Expected.Stdout)
			assertMutationCase(t, testBinaryPath, control, control.Expected.Exit.Status, control.Expected.Stdout)

			dir := t.TempDir()
			mutatedSource := filepath.Join(dir, "protected.go")
			mutated := strings.Replace(string(original), m.Find, m.Replace, 1)
			if err := os.WriteFile(mutatedSource, []byte(mutated), 0o600); err != nil {
				t.Fatalf("write mutated source: %v", err)
			}
			overlay, err := json.Marshal(struct {
				Replace map[string]string
			}{Replace: map[string]string{source: mutatedSource}})
			if err != nil {
				t.Fatalf("encode Go overlay: %v", err)
			}
			overlayPath := filepath.Join(dir, "overlay.json")
			if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
				t.Fatalf("write Go overlay: %v", err)
			}
			binary := filepath.Join(dir, "release-intent")
			build := exec.Command("go", "build", "-overlay", overlayPath, "-o", binary, ".")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build mutated command: %v\n%s", err, out)
			}
			assertMutationCase(t, binary, witness, m.MutatedExit.Status, m.MutatedStdout)
			assertMutationCase(t, binary, control, control.Expected.Exit.Status, control.Expected.Stdout)
		})
	}
	checkRequiredNames(t, fixture.RequiredNames, seen, "changelog mutation fixture")
}

func assertMutationCase(t *testing.T, binary string, c cliCase, want exitStatus, stdout []wantLine) {
	t.Helper()
	state := setupProcessCase(t, c.Input)
	argv := make([]string, len(c.Input.Argv))
	for i, word := range c.Input.Argv {
		argv[i] = state.expand(word)
	}
	result := runBinaryAt(t, binary, state.dir, argv, c.Input.Stdin, nil)
	if result.Exit != want {
		t.Fatalf("%s: exit = %d, want %d; stdout: %q; stderr: %q", c.Name, result.Exit, want, result.Stdout, result.Stderr)
	}
	assertStdoutLines(t, c.Name, result.Stdout, plainLines(stdout))
}
