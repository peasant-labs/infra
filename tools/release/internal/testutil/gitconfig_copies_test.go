package testutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGitIsolationCopiesMatch keeps the three independent modules' isolation
// implementation, scanner and behavioural proofs in lockstep.
func TestGitIsolationCopiesMatch(t *testing.T) {
	files := []string{
		"gitconfig.go",
		"gitconfig_guard_test.go",
		"gitconfig_isolation_test.go",
		"gitconfig_test.go",
		"testdata/git_isolation_scanner.yaml",
		"testdata/git_isolation_scanner_manifest.yaml",
	}
	root := filepath.Join("..", "..", "..", "..")
	for _, file := range files {
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read tools/release/internal/testutil/%s: %v", file, err)
		}
		for _, module := range []string{"runner-dispatcher", "unstring"} {
			path := filepath.Join(root, module, "internal", "testutil", file)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read %s/internal/testutil/%s: %v", module, file, err)
			} else if !bytes.Equal(got, want) {
				t.Errorf("%s/internal/testutil/%s differs from tools/release/internal/testutil/%s; synchronize the copies", module, file, file)
			}
		}
	}
}
