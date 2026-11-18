package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// This integration test uses the normal temp-repository builder and built
// binary, exercising the same config, git reads and writes as the workflows.
func TestStreamCommands(t *testing.T) {
	type input struct {
		Config         string `yaml:"config"`
		Outside        string `yaml:"outside"`
		Inside         string `yaml:"inside"`
		Queue          string `yaml:"queue"`
		InvalidQueue   string `yaml:"invalid_queue"`
		ReleasingQueue string `yaml:"releasing_queue"`
		SecondConfig   string `yaml:"second_config"`
	}
	type expected struct {
		Tag          string   `yaml:"tag"`
		Version      string   `yaml:"version"`
		Gate         string   `yaml:"gate"`
		ReleasePaths []string `yaml:"release_paths"`
	}
	for _, c := range loadCorpus[input, expected](t, "streams.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			r := newTestRepo(t, repoStandard)
			cfg, err := intent.ParseConfig([]byte(c.Input.Config))
			if err != nil {
				t.Fatal(err)
			}
			r.cfg = cfg
			r.WriteRawConfig(c.Input.Config)
			result := r.Run("check-config", "--config", intent.DefaultConfigPath)
			if result.Exit != exitAccepted {
				t.Fatalf("check config: %+v", result)
			}
			r.WriteFile("second-config.yaml", c.Input.SecondConfig)
			result = r.Run("check-config", "--config", "second-config.yaml")
			if result.Exit != exitRejected || !strings.Contains(result.Stdout, "error=stream-invalid") {
				t.Fatalf("second stream: %+v", result)
			}
			if err := os.Remove(filepath.Join(r.dir, "second-config.yaml")); err != nil {
				t.Fatal(err)
			}
			r.WriteChangelog("# changelog\n\n## 0.1.0\n\n- old\n")
			r.WriteFile(cfg.VersionFiles[0].Path, "{\"version\":\"0.1.0\",\"name\":\"example\"}\n")
			r.WriteFile("outside.txt", "old\n")
			r.Commit(titleSeedForStream(), at(dateSeed))
			r.TagAnnotated("0.1.0")
			r.WriteFile("outside.txt", "new\n")
			r.CommitRaw(c.Input.Outside, at(datePending))
			checkTitle := func(title string, author, branch string, base, head string) processResult {
				return runBinary(t, r.dir, []string{"check-title", "--base", base, "--head", head, "--author", author, "--head-branch", branch}, title+"\n", nil)
			}
			result = checkTitle(c.Input.Outside, "author", "work", "HEAD~1", "HEAD")
			if result.Exit != exitAccepted {
				t.Fatalf("outside check-title: %+v", result)
			}
			result = r.Run("plan", "--branch", "HEAD", "--mode", "tag")
			values := outputValues(t, "outside plan", result.Stdout)
			if result.Exit != exitAccepted || values[intent.OutputAction] != "close-pr" || values[intent.OutputTag] != "" {
				t.Fatalf("outside plan: %+v", result)
			}
			r.WriteFile("unstring/rule.go", "package rule\n")
			r.CommitRaw(c.Input.Inside, at(dateLater))
			result = checkTitle(c.Input.Inside, "author", "work", "HEAD~1", "HEAD")
			if result.Exit != exitAccepted {
				t.Fatalf("inside check-title: %+v", result)
			}
			result = r.Run("plan", "--branch", "HEAD", "--mode", "tag")
			values = outputValues(t, "mixed plan", result.Stdout)
			if result.Exit != exitAccepted || values[intent.OutputTag] != c.Expected.Tag || values[intent.OutputRangeBase] != "unstring/v0.1.0" {
				t.Fatalf("mixed plan: %+v", result)
			}
			result = r.Run("release-pr", "--branch", "HEAD", "--write")
			if result.Exit != exitAccepted {
				t.Fatalf("release write: %+v", result)
			}
			if got := r.ReadChangelog(); !strings.Contains(got, "add a rule") || strings.Contains(got, "repair the router") {
				t.Fatalf("wrong changelog content: %q", got)
			}
			if got := r.ReadVersionField(0); got != c.Expected.Version {
				t.Fatalf("version = %q", got)
			}
			if got := r.ReadFile("outside.txt"); got != "new\n" {
				t.Fatalf("outside content changed: %q", got)
			}
			if got := strings.Fields(r.git(verbDiff, "--name-only")); !reflect.DeepEqual(got, c.Expected.ReleasePaths) {
				t.Fatalf("release changed paths %v, want %v", got, c.Expected.ReleasePaths)
			}
			result = r.Run("release-pr", "--branch", "HEAD", "--check")
			if result.Exit != exitAccepted {
				t.Fatalf("release check: %+v", result)
			}
			versionBytes := r.ReadVersionDocument(0)
			r.SetVersionField(0, "0.1.0")
			result = r.Run("release-pr", "--branch", "HEAD", "--check")
			if result.Exit != exitRejected || !strings.Contains(result.Stdout, "fresh=false") {
				t.Fatalf("stale release check: %+v", result)
			}
			r.WriteVersionDocument(0, versionBytes)
			r.CommitRaw("release[minor]: "+c.Expected.Tag, at(dateLaterRelease))
			result = r.Run("plan", "--branch", "HEAD", "--mode", "tag")
			values = outputValues(t, "landed release", result.Stdout)
			if result.Exit != exitAccepted || values[intent.OutputAction] != "verify" || values[intent.OutputTag] != c.Expected.Tag {
				t.Fatalf("landed release: %+v", result)
			}
			result = checkTitle("release[minor]: "+c.Expected.Tag, cfg.AutomationAuthor, cfg.BotBranches[intent.PRKindRelease], "HEAD~1", "HEAD")
			if result.Exit != exitAccepted {
				t.Fatalf("release guard: %+v", result)
			}
			r.WriteFile("outside.txt", "illicit\n")
			r.CommitRaw("chore: extra file")
			result = checkTitle("release[minor]: "+c.Expected.Tag, cfg.AutomationAuthor, cfg.BotBranches[intent.PRKindRelease], "HEAD~2", "HEAD")
			if result.Exit != exitRejected || !strings.Contains(result.Stdout, "error=release-content-scope") {
				t.Fatalf("release scope guard: %+v", result)
			}
			queue := filepath.Join(r.dir, "queue.json")
			if err := os.WriteFile(queue, []byte(c.Input.Queue), 0600); err != nil {
				t.Fatal(err)
			}
			result = r.Run("queue-gate", "--queue", queue, "--release-pr", "10")
			if result.Exit != exitAccepted || outputValues(t, "queue", result.Stdout)[intent.OutputGate] != c.Expected.Gate {
				t.Fatalf("queue: %+v", result)
			}
			if err := os.WriteFile(queue, []byte(c.Input.InvalidQueue), 0600); err != nil {
				t.Fatal(err)
			}
			result = r.Run("queue-gate", "--queue", queue, "--release-pr", "10")
			if result.Exit != exitAccepted || outputValues(t, "bad queue", result.Stdout)[intent.OutputGate] != "hold" {
				t.Fatalf("bad queue: %+v", result)
			}
			if err := os.WriteFile(queue, []byte(c.Input.ReleasingQueue), 0600); err != nil {
				t.Fatal(err)
			}
			result = r.Run("queue-gate", "--queue", queue, "--release-pr", "10")
			if result.Exit != exitAccepted || outputValues(t, "stream queue", result.Stdout)[intent.OutputGate] != "hold" {
				t.Fatalf("stream queue: %+v", result)
			}
			result = r.Run("verify-tag", "--tag", "unstring/v0.1.0", "--branch", "HEAD")
			if result.Exit != exitAccepted {
				t.Fatalf("verify tag: %+v", result)
			}
			result = r.Run("verify-tag", "--tag", "v0.1.0", "--branch", "HEAD")
			if result.Exit != exitRejected {
				t.Fatalf("wrong tag accepted: %+v", result)
			}
		})
	}
}

func titleSeedForStream() intent.Title {
	return intent.Title{Type: intent.TypeChore, Subject: "adopt release config"}
}
