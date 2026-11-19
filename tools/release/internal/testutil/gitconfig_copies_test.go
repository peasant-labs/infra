package testutil

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGitIsolationCopiesMatch keeps the independent modules' test support in lockstep.
func TestGitIsolationCopiesMatch(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	var copies []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata" || entry.Name() == "node_modules" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		copyDir := filepath.Join(filepath.Dir(path), "internal", "testutil")
		if _, err := os.Stat(filepath.Join(copyDir, "gitconfig.go")); err == nil {
			copies = append(copies, copyDir)
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) < 2 {
		t.Fatalf("found %d git isolation copies; run from a complete repository checkout", len(copies))
	}
	files := func(dir string) []string {
		t.Helper()
		var paths []string
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			// This comparison test exists in only one module.
			if rel != "gitconfig_copies_test.go" {
				paths = append(paths, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(paths)
		return paths
	}
	wantFiles := files(".")
	for _, copyDir := range copies {
		gotFiles := files(copyDir)
		if !slices.Equal(gotFiles, wantFiles) {
			t.Errorf("%s files = %v, want %v", copyDir, gotFiles, wantFiles)
			continue
		}
		for _, file := range wantFiles {
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(copyDir, file))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from tools/release/internal/testutil/%s", filepath.Join(copyDir, file), file)
			}
		}
	}
}
