package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

type versionSourceInput struct {
	ProtectedPattern string `yaml:"protected_pattern"`
	Document         string `yaml:"document"`
}

type versionSourceExpected struct {
	Error       string `yaml:"error"`
	VersionFile string `yaml:"version_file"`
}

// Config validation already refuses this overlap. Exercise the production
// file guard directly too, so a future caller cannot silently preserve dirty
// non-version bytes by passing an invalid programmatic config.
func TestOpenReleaseFilesRejectsDirtyVersion(t *testing.T) {
	for _, row := range loadCorpus[versionSourceInput, versionSourceExpected](t, "version-source.yaml") {
		t.Run(row.Name, func(t *testing.T) {
			r := newPendingRepo(t, repoStandard)
			tip := r.Head()
			cfg := r.Config()
			cfg.ProtectedPaths = []string{row.Input.ProtectedPattern}
			if row.Input.Document != "" {
				r.WriteVersionDocument(0, row.Input.Document)
			}
			runner := r.Runner()
			gitPath, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(r.Dir())
			content := intent.Content{Changelog: "generated\n", Versions: map[string]string{cfg.VersionFiles[0].Path: "0.2.0"}}
			files, err := openReleaseFiles(context.Background(), prepareGit{path: gitPath}, runner, cfg, content, tip)
			for _, file := range files {
				defer file.file.Close()
			}
			if row.Classification == testcase.MustFail {
				if err == nil || !strings.Contains(err.Error(), row.Expected.Error) {
					t.Fatalf("error = %v, want %q", err, row.Expected.Error)
				}
				if got := r.ReadVersionDocument(0); got != row.Input.Document {
					t.Fatalf("dirty version document changed: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 {
				t.Fatalf("openReleaseFiles returned %d files, want 2", len(files))
			}
			if string(files[1].after) != row.Expected.VersionFile {
				t.Fatalf("version output = %q, want %q", files[1].after, row.Expected.VersionFile)
			}
		})
	}
}
