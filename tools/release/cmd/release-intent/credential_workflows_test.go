package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/policy"
	"gopkg.in/yaml.v3"
)

// The CLI reuses the policy corpus's trees and edits, rather than maintaining
// a second copy of the credential workflows. This checks the actual process
// exit and diagnostic boundary in addition to the policy package's fixtures.
type credentialWorkflowInput struct {
	PolicyCase    string `yaml:"policy_case"`
	PrintRequired bool   `yaml:"print_required"`
}

type credentialWorkflowExpected struct {
	Exit wantExit         `yaml:"exit"`
	Rule policy.QueueRule `yaml:"rule"`
}

type policyWorkflowCase struct {
	Name  string `yaml:"name"`
	Input struct {
		Overlay string `yaml:"overlay"`
		Edits   []struct {
			File string `yaml:"file"`
			Old  string `yaml:"old"`
			New  string `yaml:"new"`
		} `yaml:"edits"`
	} `yaml:"input"`
}

func TestCLICredentialWorkflows(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "policy", "testdata", "workflow_queue")
	data, err := os.ReadFile(filepath.Join(root, "cases.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policyCases struct {
		Cases []policyWorkflowCase `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &policyCases); err != nil {
		t.Fatal(err)
	}
	byName := map[string]policyWorkflowCase{}
	for _, row := range policyCases.Cases {
		byName[row.Name] = row
	}
	for _, row := range loadCorpus[credentialWorkflowInput, credentialWorkflowExpected](t, "cli-credential-workflows.yaml") {
		t.Run(row.Name, func(t *testing.T) {
			sample, ok := byName[row.Input.PolicyCase]
			if !ok {
				t.Fatalf("policy corpus has no case %q", row.Input.PolicyCase)
			}
			dir := t.TempDir()
			copyCredentialTree(t, filepath.Join(root, "base"), dir)
			if sample.Input.Overlay != "" {
				copyCredentialTree(t, filepath.Join(root, "overlays", sample.Input.Overlay), dir)
			}
			for _, edit := range sample.Input.Edits {
				name := filepath.Join(dir, edit.File)
				old, err := os.ReadFile(name)
				if err != nil || edit.Old == "" || strings.Count(string(old), edit.Old) != 1 {
					t.Fatalf("read unique fixture edit %q: %v", edit.File, err)
				}
				if err := os.WriteFile(name, []byte(strings.Replace(string(old), edit.Old, edit.New, 1)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			argv := []string{intent.CommandCheckWorkflows.String(), intent.FlagDir.String(), "workflows"}
			if row.Input.PrintRequired {
				argv = append(argv, intent.FlagPrintRequired.String())
			}
			got := runBinary(t, dir, argv, "", nil)
			if got.Exit != row.Expected.Exit.Status {
				t.Fatalf("exit = %d, want %d: %s", got.Exit, row.Expected.Exit.Status, got.Stderr)
			}
			if row.Expected.Rule != "" && !strings.Contains(got.Stderr, row.Expected.Rule.String()) {
				t.Fatalf("stderr does not name %s: %s", row.Expected.Rule, got.Stderr)
			}
			if row.Input.PrintRequired && got.Stdout != "merge-gate\ntitle-guard\n" {
				t.Fatalf("required contexts = %q", got.Stdout)
			}
		})
	}
}

func copyCredentialTree(t *testing.T, src, dst string) {
	t.Helper()
	err := fs.WalkDir(os.DirFS(src), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, name)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy credential fixture %s: %v", src, err)
	}
}
