package policy

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

//go:embed testdata/npm-install.yaml
var npmInstallFixture []byte

func TestGlobalNpmInstall(t *testing.T) {
	t.Parallel()
	var f struct {
		Required []string `yaml:"required_names"`
		Cases    []struct {
			Name  string `yaml:"name"`
			Input string `yaml:"input"`
			Want  bool   `yaml:"want"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(npmInstallFixture, &f); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range f.Cases {
		if seen[row.Name] {
			t.Fatalf("duplicate case %q", row.Name)
		}
		seen[row.Name] = true
		if got := globalNpmInstallMatch(row.Input); got != row.Want {
			t.Errorf("%s: global = %t, want %t (input: %q)", row.Name, got, row.Want, row.Input)
		}
	}
	for _, name := range f.Required {
		if !seen[name] {
			t.Errorf("missing required case %q", name)
		}
	}
	if len(seen) != len(f.Required) {
		t.Fatal("case names differ from manifest")
	}
}

func TestCheckPrivateNpmStandalone(t *testing.T) {
	t.Parallel()
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "ci.yml"), []byte("name: CI\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: npm ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if diags := CheckPrivateNpm(clean); len(diags) != 0 {
		t.Fatalf("clean dir diagnostics = %+v, want none", diags)
	}

	dirty := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirty, "bad.yml"), []byte("name: Bad\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: npm install -g example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diags := CheckPrivateNpm(dirty)
	if len(diags) != 1 || diags[0].Rule != QueueRulePrivateNpm || !strings.Contains(diags[0].Message, "$RUNNER_TEMP") {
		t.Fatalf("dirty dir diagnostics = %+v, want one %s diagnostic", diags, QueueRulePrivateNpm)
	}
}
