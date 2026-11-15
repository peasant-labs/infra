package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

type commitCaseInput struct {
	Scenario string `yaml:"scenario"`
}
type commitCaseExpected struct {
	Exit        wantExit `yaml:"exit"`
	Push        bool     `yaml:"push"`
	Version     string   `yaml:"version"`
	Error       string   `yaml:"error"`
	Title       string   `yaml:"title"`
	Identity    string   `yaml:"identity"`
	Changelog   string   `yaml:"changelog"`
	VersionFile string   `yaml:"version_file"`
}

// gitIn runs Git only in the disposable repositories made by this test. The
// release command itself is always the built production binary.
func gitIn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = hermeticGitEnv(t)
	out, err := c.CombinedOutput()
	return string(out), err
}

func mustGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitIn(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

// The wrapper is confined to the disposable process and repository. It
// mutates the real Git index at an exact boundary, so the production binary's
// staged-byte and immutable-tree checks are exercised rather than mocked.
func tamperGitEnv(t *testing.T, r *testRepo, scenario string) []string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	payload := filepath.Join(dir, "unexpected")
	if err := os.WriteFile(payload, []byte("unexpected staged content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blob := mustGitIn(t, r.dir, "hash-object", "-w", payload)
	mode, target := "stage", r.cfg.Changelog
	if scenario == "tamper-tree" {
		mode, target = "tree", "work.txt"
	}
	script := `#!/bin/sh
if [ "$TEST_TAMPER_MODE" = stage ] && [ "$1" = update-index ] && [ "$2" = -z ]; then
  "$TEST_REAL_GIT" "$@" || exit $?
  "$TEST_REAL_GIT" update-index --cacheinfo "100644,$TEST_TAMPER_BLOB,$TEST_TAMPER_TARGET"
  exit $?
fi
if [ "$TEST_TAMPER_MODE" = tree ] && [ "$1" = write-tree ]; then
  "$TEST_REAL_GIT" update-index --cacheinfo "100644,$TEST_TAMPER_BLOB,$TEST_TAMPER_TARGET" || exit $?
fi
exec "$TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "TEST_REAL_GIT=" + realGit, "TEST_TAMPER_MODE=" + mode, "TEST_TAMPER_BLOB=" + blob, "TEST_TAMPER_TARGET=" + target}
}

func TestReleaseCommit(t *testing.T) {
	for _, row := range loadCorpus[commitCaseInput, commitCaseExpected](t, "release-commit.yaml") {
		row := row
		t.Run(row.Name, func(t *testing.T) {
			s := row.Input.Scenario
			var r *testRepo
			hasCurrent := s == "unchanged" || s == "advanced" || s == "later" || s == "current-extra" || s == "current-unrelated" || s == "current-mode" || s == "current-conflict"
			if hasCurrent {
				setup := setupCurrentSame
				if s == "current-conflict" {
					setup = setupCurrentConflict
				}
				r = setupCurrentBranch(t, setup)
				if s == "current-extra" || s == "current-unrelated" || s == "current-mode" {
					r.Checkout(r.BotBranch(intent.PRKindRelease))
					switch s {
					case "current-extra":
						r.SetOrdinaryField(0, "tampered", true)
					case "current-unrelated":
						r.WriteFile("work.txt", "unrelated content\n")
					case "current-mode":
						if err := os.Chmod(filepath.Join(r.dir, r.cfg.VersionFiles[0].Path), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					r.Commit(titleDocs)
					r.Checkout(r.Trunk())
				}
				if s == "advanced" || s == "later" {
					r.WriteFile("later.txt", "later\n")
					if s == "advanced" {
						r.Commit(titleDocs)
					} else {
						r.Commit(titled(intent.TypeFix, "work", intent.LevelPatch, "later fix"))
					}
				}
			} else {
				r = newPendingRepo(t, repoStandard)
			}

			switch s {
			case "breaking":
				r.WriteFile("breaking.txt", "change\n")
				r.Commit(intent.Title{Type: intent.TypeFix, Scopes: []string{"repair"}, Breaking: true, Level: intent.LevelMinor, Subject: "incompatible fix"})
			case "empty", "version-only":
				original := r.ReadVersionDocument(0)
				result := r.Run(argvOf(intent.CommandReleasePR, intent.FlagBranch, headRev, intent.FlagWrite)...)
				if result.Exit != exitAccepted {
					t.Fatalf("prepare existing content: %+v", result)
				}
				if s == "version-only" {
					r.WriteVersionDocument(0, original)
				}
				r.Commit(releaseTitled(intent.LevelMinor, "generated"))
			case "missing-version":
				r.RemoveVersionFile(0)
				r.Commit(titleDocs)
			case "invalid-version":
				r.RemoveVersionField(0)
				r.Commit(titleDocs)
			case "symlink":
				r.git(verbRemove, "-qf", "--", r.cfg.VersionFiles[0].Path)
				if err := os.Symlink("work.txt", filepath.Join(r.dir, r.cfg.VersionFiles[0].Path)); err != nil {
					t.Fatal(err)
				}
				r.Commit(titleDocs)
			case "symlink-parent":
				r.cfg.VersionFiles[0].Path = "alias/package.json"
				r.WriteConfig(r.cfg)
				if err := os.Symlink(".", filepath.Join(r.dir, "alias")); err != nil {
					t.Fatal(err)
				}
				r.Commit(titleDocs)
			case "pathspec":
				r.cfg.Changelog = ":(glob)*"
				r.WriteConfig(r.cfg)
				r.Commit(titleDocs)
			case "protected":
				r.cfg.ProtectedPaths = []string{"package.json"}
				r.WriteConfig(r.cfg)
				r.Commit(titleDocs)
			case "duplicate":
				r.cfg.VersionFiles = append(r.cfg.VersionFiles, r.cfg.VersionFiles[0])
				r.WriteConfig(r.cfg)
				r.Commit(titleDocs)
			}
			tip := r.Head()
			// The bare remote has the exact source tip but not the new release commit.
			bare := filepath.Join(t.TempDir(), "remote.git")
			mustGitIn(t, r.dir, "clone", "--bare", "--no-local", r.dir, bare)
			if s != "wrong-branch" {
				if hasCurrent {
					// The remote-tracking ref records the existing release branch before rebuilding.
					mustGitIn(t, r.dir, "update-ref", "refs/remotes/origin/release", r.Resolve(r.BotBranch(intent.PRKindRelease)))
					mustGitIn(t, r.dir, "checkout", "-q", "-B", r.branchName(r.BotBranch(intent.PRKindRelease)), tip)
				} else {
					r.CreateBranch(r.BotBranch(intent.PRKindRelease))
				}
			}
			if s == "wrong-tip" {
				mustGitIn(t, r.dir, "checkout", "-q", "-B", r.branchName(r.BotBranch(intent.PRKindRelease)), tip+"~1")
			}
			switch s {
			case "dirty", "staged":
				r.WriteFile("work.txt", "unrelated dirty\n")
				if s == "staged" {
					r.git(verbAdd, "--", "work.txt")
				}
			case "untracked":
				r.WriteFile("untracked.txt", "untracked\n")
			case "json-dirty":
				r.SetOrdinaryField(0, "scripts", "changed")
			case "assume-unchanged", "skip-worktree":
				option := "--assume-unchanged"
				if s == "skip-worktree" {
					option = "--skip-worktree"
				}
				mustGitIn(t, r.dir, "update-index", option, "--", "work.txt")
				r.WriteFile("work.txt", "hidden change\n")
			case "hardlink":
				outside := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(outside, []byte(r.ReadVersionDocument(0)), 0o644); err != nil {
					t.Fatal(err)
				}
				name := filepath.Join(r.dir, r.cfg.VersionFiles[0].Path)
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, name); err != nil {
					t.Fatal(err)
				}
			}
			args := argvOf(intent.CommandReleasePR, intent.FlagBranch, r.Trunk().String(), intent.FlagCommit)
			if s == "missing-source" {
				args = argvOf(intent.CommandReleasePR, intent.FlagBranch, "refs/remotes/origin/missing", intent.FlagCommit)
			}
			if s == "mixed-write" {
				args = append(args, intent.FlagWrite.String())
			}
			if s == "mixed-check" {
				args = append(args, intent.FlagCheck.String())
			}
			if s == "old-write" {
				args = argvOf(intent.CommandReleasePR, intent.FlagBranch, r.Trunk().String(), intent.FlagWrite)
			}
			if hasCurrent {
				args = append(args, intent.FlagCurrent.String(), "refs/remotes/origin/release")
			}
			if s == "missing-current" {
				args = append(args, intent.FlagCurrent.String(), "refs/remotes/origin/missing")
			}
			headBefore := r.Head()
			beforeStatus := mustGitIn(t, r.dir, "status", "--porcelain=v1", "--untracked-files=all")
			var result processResult
			if s == "initial" {
				outputFile := filepath.Join(t.TempDir(), "github-output")
				if err := os.WriteFile(outputFile, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				result = runBinary(t, r.dir, args, "", []string{githubOutputEnv + "=" + outputFile})
				output, err := os.ReadFile(outputFile)
				if err != nil || string(output) != result.Stdout {
					t.Fatalf("step outputs %q differ from stdout %q: %v", output, result.Stdout, err)
				}
			} else if s == "tamper-stage" || s == "tamper-tree" {
				result = runBinary(t, r.dir, args, "", tamperGitEnv(t, r, s))
			} else if s == "alternate-index" {
				index := filepath.Join(t.TempDir(), "outside-index")
				result = runBinary(t, r.dir, args, "", []string{"GIT_INDEX_FILE=" + index})
				if _, err := os.Stat(index); !os.IsNotExist(err) {
					t.Fatalf("alternate index was touched: %v", err)
				}
			} else {
				result = r.Run(args...)
			}
			if result.Exit != row.Expected.Exit.Status {
				t.Fatalf("exit %v, want %v\nstdout: %s\nstderr: %s", result.Exit, row.Expected.Exit.Status, result.Stdout, result.Stderr)
			}
			if row.Expected.Error != "" {
				assertKeysAbsent(t, row.Name, result.Stdout, []intent.OutputKey{intent.OutputPush, intent.OutputFresh})
				if !strings.Contains(strings.ToLower(result.Stderr), strings.ToLower(row.Expected.Error)) {
					t.Fatalf("stderr %q does not contain %q", result.Stderr, row.Expected.Error)
				}
				if r.Head() != headBefore {
					t.Fatal("failed preparation moved HEAD")
				}
				if got := mustGitIn(t, r.dir, "status", "--porcelain=v1", "--untracked-files=all"); s != "tamper-stage" && s != "tamper-tree" && got != beforeStatus {
					t.Fatalf("failed preparation changed worktree: before %q, after %q", beforeStatus, got)
				}
				return
			}
			outputs := outputValues(t, row.Name, result.Stdout)
			if outputs[intent.OutputPush] != map[bool]string{true: "true", false: "false"}[row.Expected.Push] {
				t.Fatalf("unexpected output: %s", result.Stdout)
			}
			if len(splitLines(result.Stdout)) != 4 || outputs[intent.OutputFresh] != "true" || outputs[intent.OutputVersion] != row.Expected.Version || outputs[intent.OutputError] != "" {
				t.Fatalf("unexpected release output: %s", result.Stdout)
			}
			if s == "old-write" {
				if r.Head() != tip || mustGitIn(t, r.dir, "status", "--porcelain=v1") == "" {
					t.Fatal("legacy write must leave generated content uncommitted")
				}
				_, err := gitIn(t, r.dir, "bundle", "create", filepath.Join(t.TempDir(), "old.bundle"), r.BotBranch(intent.PRKindRelease).String(), "^"+tip)
				if err == nil {
					t.Fatal("write-only negative control unexpectedly created a bundle")
				}
				return
			}
			if !row.Expected.Push {
				if r.Head() != tip || mustGitIn(t, r.dir, "status", "--porcelain=v1") != "" {
					t.Fatal("no-op changed the branch or tree")
				}
				return
			}
			if r.Head() == tip || r.Parent() != tip {
				t.Fatal("expected exactly one new commit on the source tip")
			}
			if parents := strings.Fields(mustGitIn(t, r.dir, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 2 || parents[1] != tip {
				t.Fatalf("release commit has unexpected parents: %v", parents)
			}
			if status := mustGitIn(t, r.dir, "status", "--porcelain=v1"); status != "" {
				t.Fatalf("working tree after commit is not clean: %s", status)
			}
			if got := mustGitIn(t, r.dir, "diff", "--name-only", "--no-renames", tip, "HEAD"); got != "CHANGELOG.md\npackage.json" {
				t.Fatalf("unexpected commit paths: %q", got)
			}
			packageBytes, err := gitIn(t, r.dir, "show", "HEAD:package.json")
			if err != nil || packageBytes != row.Expected.VersionFile {
				t.Fatalf("unexpected committed version document: %q, want %q, error %v", packageBytes, row.Expected.VersionFile, err)
			}
			changelog, err := gitIn(t, r.dir, "show", "HEAD:CHANGELOG.md")
			date := mustGitIn(t, r.dir, "show", "-s", "--format=%cs", tip)
			wantChangelog := strings.ReplaceAll(row.Expected.Changelog, "$DATE", date)
			if err != nil || changelog != wantChangelog {
				t.Fatalf("committed changelog = %q, want %q, error %v", changelog, wantChangelog, err)
			}
			subject := mustGitIn(t, r.dir, "show", "-s", "--format=%s", "HEAD")
			if subject != row.Expected.Title {
				t.Fatalf("title = %q, want %q", subject, row.Expected.Title)
			}
			if _, err := intent.ParseTitle(r.cfg, r.fix.SeedVersion, subject); err != nil {
				t.Fatalf("invalid commit title %q: %v", subject, err)
			}
			identity := mustGitIn(t, r.dir, "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD")
			if identity != row.Expected.Identity {
				t.Fatalf("wrong commit identity: %q", identity)
			}
			sourceTime := mustGitIn(t, r.dir, "show", "-s", "--format=%cI", tip)
			commitTimes := mustGitIn(t, r.dir, "show", "-s", "--format=%aI|%cI", "HEAD")
			if commitTimes != sourceTime+"|"+sourceTime {
				t.Fatalf("commit dates %q do not both match source %q", commitTimes, sourceTime)
			}
			check := r.Run(argvOf(intent.CommandReleasePR, intent.FlagBranch, r.Trunk().String(), intent.FlagCheck)...)
			if check.Exit != exitAccepted {
				t.Fatalf("committed tree is not accepted by the production freshness check: %+v", check)
			}
			bundle := filepath.Join(t.TempDir(), "release.bundle")
			mustGitIn(t, r.dir, "bundle", "create", bundle, r.BotBranch(intent.PRKindRelease).String(), "^"+tip)
			mustGitIn(t, bare, "bundle", "verify", bundle)
			mustGitIn(t, bare, "fetch", bundle, r.BotBranch(intent.PRKindRelease).String()+":refs/heads/imported-release")
			if got := mustGitIn(t, bare, "rev-parse", "refs/heads/imported-release"); got != r.Head() {
				t.Fatalf("fetched %s, expected %s", got, r.Head())
			}
			if s == "deterministic" {
				first := r.Head()
				mustGitIn(t, r.dir, "checkout", "-q", "-B", r.branchName(r.BotBranch(intent.PRKindRelease)), tip)
				second := r.Run(args...)
				if second.Exit != exitAccepted || r.Head() != first {
					t.Fatalf("same source did not produce the same commit: first %s, second %s, result %+v", first, r.Head(), second)
				}
			}
		})
	}
}
