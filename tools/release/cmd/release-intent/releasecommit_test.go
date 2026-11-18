package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

type commitCaseInput struct {
	Scenario        string            `yaml:"scenario"`
	Plan            string            `yaml:"plan"`
	FreezeDir       string            `yaml:"freeze_dir"`
	BaseFiles       map[string]string `yaml:"base_files"`
	Files           map[string]string `yaml:"files"`
	Current         bool              `yaml:"current"`
	CurrentFiles    map[string]string `yaml:"current_files"`
	NoCurrentPlan   bool              `yaml:"no_current_plan"`
	Stage           []string          `yaml:"stage"`
	Symlinks        map[string]string `yaml:"symlinks"`
	GenerateFloor   bool              `yaml:"generate_floor"`
	PlanFloorAction string            `yaml:"plan_floor_action"`
	GeneratedTo     string            `yaml:"generated_to"`
	ReleaseFloor    bool              `yaml:"release_floor"`
	Migration       *migrationHistory `yaml:"migration"`
}

type migrationHistory struct {
	PreSubject       string `yaml:"pre_subject"`
	MigrationSubject string `yaml:"migration_subject"`
	PostSubject      string `yaml:"post_subject"`
	State            string `yaml:"state"`
}
type commitCaseExpected struct {
	Exit        wantExit         `yaml:"exit"`
	Push        bool             `yaml:"push"`
	Version     string           `yaml:"version"`
	Error       string           `yaml:"error"`
	Title       string           `yaml:"title"`
	Identity    string           `yaml:"identity"`
	Changelog   string           `yaml:"changelog"`
	VersionFile string           `yaml:"version_file"`
	Paths       string           `yaml:"paths"`
	Code        intent.ErrorCode `yaml:"code"`
	Details     []string         `yaml:"details"`
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
			var generatedPlan []byte
			var migration, base string
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
			} else if s == "migration-floor" {
				r = newReleasedRepo(t, repoStandard)
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
			case "protected", "protected-version-dirty", "protected-version-glob":
				r.cfg.ProtectedPaths = []string{"package.json"}
				if s == "protected-version-glob" {
					r.cfg.ProtectedPaths = []string{"*.json"}
				}
				data, err := yaml.Marshal(r.cfg)
				if err != nil {
					t.Fatal(err)
				}
				r.WriteRawConfig(string(data))
				r.Commit(titleDocs)
			case "protected-files", "protected-staged", "protected-symlink", "protected-plan", "protected-current", "protected-current-changed", "protected-current-plan":
				r.cfg.ProtectedPaths = []string{"versions.go", "generated/*-[0-9]*"}
				if s == "protected-plan" || s == "protected-current-plan" {
					r.cfg.Surfaces = map[string][]string{"local-api": {"local"}}
					r.cfg.SpecFreeze = true
					r.cfg.FreezeDir = "generated"
				}
				r.WriteConfig(r.cfg)
				r.WriteFile("versions.go", "before\n")
				r.Commit(titleDocs)
				if s == "protected-plan" || s == "protected-current-plan" {
					r.WriteFile("local.txt", "changed\n")
					r.Commit(titled(intent.TypeFeat, "local-api", intent.LevelMinor, "freeze changes"))
				}
			case "freeze-outputs":
				freezeDir := row.Input.FreezeDir
				if freezeDir == "" {
					freezeDir = "generated"
				}
				r.cfg.ProtectedPaths = []string{freezeDir + "/*-[0-9]*"}
				r.cfg.FreezeOutputs = []string{freezeDir + "/api.json", "registry.yaml", "mirrors/*.json"}
				r.cfg.Surfaces = map[string][]string{"api": {"api", "request"}, "types": {"types"}}
				r.cfg.SpecFreeze = true
				r.cfg.FreezeDir = freezeDir
				r.WriteConfig(r.cfg)
				r.WriteFile(freezeDir+"/api.json", "working before\n")
				r.WriteFile("registry.yaml", "registry before\n")
				r.WriteFile("mirrors/api.json", "mirror before\n")
				for name, content := range row.Input.BaseFiles {
					r.WriteFile(name, content)
				}
				r.Commit(titleDocs)
				r.WriteFile("api.txt", "changed\n")
				r.Commit(titled(intent.TypeFeat, "api", intent.LevelMinor, "freeze api"))
				r.WriteFile("types.txt", "changed\n")
				r.Commit(titled(intent.TypeFix, "types", intent.LevelPatch, "freeze types"))
			case "migration-floor":
				if row.Input.Migration == nil {
					t.Fatal("migration history is required")
				}
				history := row.Input.Migration
				base = r.Head()
				r.cfg.ProtectedPaths = []string{"generated/*-[0-9]*"}
				r.cfg.Surfaces = map[string][]string{"local-api": {"local"}}
				r.cfg.SpecFreeze = true
				r.cfg.FreezeDir = "generated"
				r.WriteConfig(r.cfg)
				r.WriteFile("feature.txt", "feature\n")
				r.CommitRaw(history.PreSubject)
				r.WriteFile("generated/local.json", "working spec\n")
				r.CommitRaw(history.MigrationSubject)
				migration = r.Head()
				r.WriteFile("work.txt", "fix\n")
				r.CommitRaw(history.PostSubject)
				scratch := t.TempDir()
				statePath := filepath.Join(scratch, "state.yaml")
				planPath := filepath.Join(scratch, "plan.yaml")
				if err := os.WriteFile(statePath, []byte(history.State), 0o600); err != nil {
					t.Fatal(err)
				}
				freezeArgs := argvOf(intent.CommandFreezePlan, intent.FlagBranch, r.Trunk().String(), intent.FlagState, statePath, intent.FlagOut, planPath)
				if row.Input.GenerateFloor {
					freezeArgs = append(freezeArgs, intent.FlagFreezeFloor.String(), migration)
				}
				if row.Input.ReleaseFloor {
					freezeArgs = append(freezeArgs, intent.FlagFloor.String(), migration)
				}
				result := r.Run(freezeArgs...)
				if result.Exit != exitAccepted {
					t.Fatalf("generate freeze plan: %+v", result)
				}
				var err error
				generatedPlan, err = os.ReadFile(planPath)
				if err != nil {
					t.Fatal(err)
				}
				parsed, err := intent.ParseFreezePlan(generatedPlan, r.cfg)
				wantFloor := ""
				if row.Input.GenerateFloor {
					wantFloor = migration
				}
				if err != nil || parsed.FreezeFloor != wantFloor || len(parsed.Surfaces) != 1 || parsed.Surfaces[0].To.String() != row.Input.GeneratedTo {
					t.Fatalf("generated plan = %+v, error %v; document:\n%s", parsed, err, generatedPlan)
				}
				switch row.Input.PlanFloorAction {
				case "":
				case "omit":
					parsed.FreezeFloor = ""
				case "base":
					parsed.FreezeFloor = base
				case "missing":
					parsed.FreezeFloor = "not-on-chain"
				default:
					t.Fatalf("unknown plan floor action %q", row.Input.PlanFloorAction)
				}
				if row.Input.PlanFloorAction != "" {
					generatedPlan, err = yaml.Marshal(parsed)
					if err != nil {
						t.Fatal(err)
					}
				}
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
			case "protected-version-dirty":
				r.SetOrdinaryField(0, "scripts", "changed")
			case "protected-files", "protected-staged", "protected-symlink", "protected-plan", "protected-current", "protected-current-changed", "protected-current-plan":
				r.WriteFile("versions.go", "after\n")
				if s == "protected-staged" {
					r.git(verbAdd, "--", "versions.go")
				}
				if s == "protected-symlink" {
					if err := os.MkdirAll(filepath.Join(r.dir, "generated"), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("../versions.go", filepath.Join(r.dir, "generated", "types-0.2.0.json")); err != nil {
						t.Fatal(err)
					}
				} else if s == "protected-files" || s == "protected-plan" || s == "protected-current" || s == "protected-current-changed" || s == "protected-current-plan" {
					name := "generated/types-0.2.0.json"
					if s == "protected-plan" || s == "protected-current-plan" {
						name = "generated/local-0.2.0.json"
					}
					r.WriteFile(name, "new spec\n")
				}
			case "freeze-outputs", "migration-floor":
				for name, content := range row.Input.Files {
					r.WriteFile(name, content)
				}
				for name, target := range row.Input.Symlinks {
					if err := os.Symlink(target, filepath.Join(r.dir, name)); err != nil {
						t.Fatal(err)
					}
				}
				for _, name := range row.Input.Stage {
					r.git(verbAdd, "--", name)
				}
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
			if row.Input.ReleaseFloor {
				args = append(args, intent.FlagFloor.String(), migration)
			}
			if s == "protected-current" || s == "protected-current-changed" || s == "protected-current-plan" {
				first := r.Run(args...)
				if first.Exit != exitAccepted {
					t.Fatalf("prepare existing protected release branch: %+v", first)
				}
				mustGitIn(t, r.dir, "update-ref", "refs/remotes/origin/release", r.Head())
				mustGitIn(t, r.dir, "checkout", "-q", "-B", r.branchName(r.BotBranch(intent.PRKindRelease)), tip)
				r.WriteFile("versions.go", "after\n")
				spec := "new spec\n"
				if s == "protected-current-changed" {
					spec = "different spec\n"
				}
				name := "generated/types-0.2.0.json"
				if s == "protected-current-plan" {
					name = "generated/local-0.2.0.json"
				}
				r.WriteFile(name, spec)
				args = append(args, intent.FlagCurrent.String(), "refs/remotes/origin/release")
			}
			if row.Input.Plan != "" || generatedPlan != nil {
				planPath := filepath.Join(t.TempDir(), "freeze.yaml")
				planData := []byte(row.Input.Plan)
				if generatedPlan != nil {
					planData = generatedPlan
				}
				if err := os.WriteFile(planPath, planData, 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, intent.FlagFreezePlan.String(), planPath)
			}
			if s == "freeze-outputs" && row.Input.Current {
				first := r.Run(args...)
				if first.Exit != exitAccepted {
					t.Fatalf("prepare current freeze release branch: %+v", first)
				}
				mustGitIn(t, r.dir, "update-ref", "refs/remotes/origin/release", r.Head())
				mustGitIn(t, r.dir, "checkout", "-q", "-B", r.branchName(r.BotBranch(intent.PRKindRelease)), tip)
				for name, content := range row.Input.CurrentFiles {
					r.WriteFile(name, content)
				}
				if row.Input.NoCurrentPlan {
					args = argvOf(intent.CommandReleasePR, intent.FlagBranch, r.Trunk().String(), intent.FlagCommit)
				}
				args = append(args, intent.FlagCurrent.String(), "refs/remotes/origin/release")
			}
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
				if row.Expected.Code != "" {
					if !strings.Contains(result.Stdout, "error="+row.Expected.Code.String()) {
						t.Fatalf("stdout %q does not contain typed code %q", result.Stdout, row.Expected.Code)
					}
					for _, label := range []string{"what:", "why:", "where:", "when:", "means:", "fix:"} {
						if !strings.Contains(result.Stderr, label) {
							t.Fatalf("diagnostic %q lacks %s", result.Stderr, label)
						}
					}
				}
				for _, detail := range row.Expected.Details {
					want := strings.ReplaceAll(detail, "${BASE}", base)
					if !strings.Contains(result.Stderr, want) {
						t.Fatalf("stderr %q does not contain %q", result.Stderr, want)
					}
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
			lines := 6
			if s == "old-write" {
				lines = 4
			}
			if len(splitLines(result.Stdout)) != lines || outputs[intent.OutputFresh] != "true" || outputs[intent.OutputVersion] != row.Expected.Version || outputs[intent.OutputError] != "" {
				t.Fatalf("unexpected release output: %s", result.Stdout)
			}
			if s != "old-write" && (outputs[intent.OutputSource] != tip || outputs[intent.OutputTitle] == "") {
				t.Fatalf("commit output did not name source and title: %s", result.Stdout)
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
				if r.Head() != tip || mustGitIn(t, r.dir, "status", "--porcelain=v1", "--untracked-files=all") != beforeStatus {
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
			wantPaths := row.Expected.Paths
			if wantPaths == "" {
				wantPaths = "CHANGELOG.md\npackage.json"
			}
			if got := mustGitIn(t, r.dir, "diff", "--name-only", "--no-renames", tip, "HEAD"); got != wantPaths {
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
			if subject != outputs[intent.OutputTitle] {
				t.Fatalf("printed title %q differs from commit subject %q", outputs[intent.OutputTitle], subject)
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
			checkArgs := argvOf(intent.CommandReleasePR, intent.FlagBranch, r.Trunk().String(), intent.FlagCheck)
			if row.Input.ReleaseFloor {
				checkArgs = append(checkArgs, intent.FlagFloor.String(), migration)
			}
			check := r.Run(checkArgs...)
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
