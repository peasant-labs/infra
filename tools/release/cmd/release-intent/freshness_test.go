package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	"gopkg.in/yaml.v3"
)

// freshScenario names the history one freshness row builds.
type freshScenario string

const (
	// freshAfterWrite commits the written content as the release.
	freshAfterWrite freshScenario = "fresh-after-write"
	// freshStaleNewReleasing lands one more releasing commit between the
	// write and the release commit.
	freshStaleNewReleasing freshScenario = "stale-new-releasing"
	// freshCheckStaleTree is the same stale history, then a check of the tree.
	freshCheckStaleTree freshScenario = "check-stale-tree"
	// freshOlderFreshNewerStale leaves an older fresh release untagged and
	// hand-builds a newer stale one.
	freshOlderFreshNewerStale freshScenario = "older-fresh-newer-stale"
	// freshReadError deletes the version file in the release commit.
	freshReadError freshScenario = "read-error"
	// freshRejectedLaterMinor lands a minor after a fresh release, then writes.
	freshRejectedLaterMinor freshScenario = "rejected-later-minor-writes-full"
	// freshLaterSubjectChecksFull does the same, then checks the written tree.
	freshLaterSubjectChecksFull freshScenario = "fresh-candidate-later-subject-checks-full"
	// freshStaleRebuild rebuilds after a stale release commit.
	freshStaleRebuild freshScenario = "stale-rebuild-single-section"
)

var allFreshScenarios = []freshScenario{
	freshAfterWrite, freshStaleNewReleasing, freshCheckStaleTree, freshOlderFreshNewerStale,
	freshReadError, freshRejectedLaterMinor, freshLaterSubjectChecksFull, freshStaleRebuild,
}

func (s *freshScenario) UnmarshalYAML(node *yaml.Node) error {
	member, err := decodeMember(node, allFreshScenarios, "freshness scenario")
	*s = member
	return err
}

type freshInput struct {
	BaseChangelog string        `yaml:"baseChangelog"`
	Scenario      freshScenario `yaml:"scenario"`
}

type freshExpected struct {
	Exit wantExit `yaml:"exit"`
	// Action is the plan's action, for rows that end on a plan.
	Action intent.Action `yaml:"action"`
	// CandidateFresh is the plan's freshness line, for rows that end on a plan.
	CandidateFresh *bool `yaml:"candidate_fresh"`
	// Lines is the exact stdout of the row's final release-pr run.
	Lines []wantLine `yaml:"lines"`
	// CheckLines is the exact stdout of the check that follows the write,
	// for scenarios that run both.
	CheckLines []wantLine `yaml:"check_lines"`
	AbsentKeys []wantKey  `yaml:"absent_keys"`
	// Sections is how many changelog sections the rebuilt version may have.
	Sections int `yaml:"sections"`
}

type freshCase = testcase.Case[freshInput, freshExpected]

// Commit dates are pinned away from the test day, so a dated heading shows
// the pending commit's date and never today's.
const (
	dateSeed         = "2020-01-01T00:00:00Z"
	datePending      = "2020-01-02T00:00:00Z"
	dateRelease      = "2020-01-03T00:00:00Z"
	dateLater        = "2020-01-04T00:00:00Z"
	dateLaterRelease = "2020-01-05T00:00:00Z"
)

var (
	titlePendingPatch = titled(intent.TypeFeat, "graph", intent.LevelPatch, "add facet")
	titleLaterPatch   = titled(intent.TypeFeat, "graph", intent.LevelPatch, "later facet")
	titleLaterMinor   = titled(intent.TypeFeat, "graph", intent.LevelMinor, "later facet")
	titleCut          = releaseTitled(intent.LevelPatch, "cut it")
	titleCutAgain     = releaseTitled(intent.LevelPatch, "cut again")
)

// laterFile is the one file a later commit adds; it is outside the release
// content.
const laterFile = "later.txt"

// newFreshRepo seeds a repository whose changelog is the row's base
// changelog and whose seed carries an annotated release tag: the plan path
// must run on the tag shape with a tag object between the name and the
// commit.
func newFreshRepo(t *testing.T, baseChangelog string) *testRepo {
	t.Helper()
	r := newTestRepo(t, repoStandard)
	r.Seed()
	r.WriteChangelog(baseChangelog)
	r.Commit(titleSeed, at(dateSeed))
	r.TagAnnotated(r.fix.SeedVersion)
	return r
}

func commitPending(r *testRepo) {
	r.t.Helper()
	r.WriteFile("work.txt", "more\n")
	r.Commit(titlePendingPatch, at(datePending))
}

// commitLater commits only the later file, leaving written release content
// in the working tree uncommitted.
func commitLater(r *testRepo, title intent.Title) {
	r.t.Helper()
	r.WriteFile(laterFile, "later\n")
	r.Commit(title, at(dateLater), only(laterFile))
}

var (
	argvWrite = argvOf(intent.CommandReleasePR, intent.FlagBranch, headRev, intent.FlagWrite)
	argvCheck = argvOf(intent.CommandReleasePR, intent.FlagBranch, headRev, intent.FlagCheck)
	argvPlan  = argvOf(intent.CommandPlan, intent.FlagBranch, headRev, intent.FlagMode, intent.ModeTag, intent.FlagVerdict, intent.VerdictTaggable)
)

// mustWrite writes the release content and fails the test on any refusal.
func mustWrite(r *testRepo, name string) {
	r.t.Helper()
	if result := r.Run(argvWrite...); result.Exit != exitAccepted {
		r.t.Fatalf("%s: %v exit = %d, want %d\nstdout: %q\nstderr: %q", name, argvWrite, result.Exit, exitAccepted, result.Stdout, result.Stderr)
	}
}

// TestFreshness drives the built binary on temp repositories: write, commit,
// plan and check across heading styles, stale histories, read errors and the
// stale-rebuild section count.
func TestFreshness(t *testing.T) {
	for _, c := range loadCorpus[freshInput, freshExpected](t, "freshness.yaml") {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			r := newFreshRepo(t, c.Input.BaseChangelog)
			commitPending(r)
			mustWrite(r, c.Name)
			switch c.Input.Scenario {
			case freshAfterWrite:
				r.Commit(titleCut, at(dateRelease))
				assertPlanRow(t, c, r.Run(argvPlan...))
			case freshStaleNewReleasing, freshCheckStaleTree:
				// A release built from an older tip: one releasing commit
				// lands, then the stale content is committed as the release.
				commitLater(r, titleLaterPatch)
				r.Commit(titleCut, at(dateLaterRelease))
				if c.Input.Scenario == freshCheckStaleTree {
					assertRunLines(t, c.Name, r.Run(argvCheck...), c.Expected.Exit.Status, c.Expected.Lines)
					return
				}
				assertPlanRow(t, c, r.Run(argvPlan...))
			case freshOlderFreshNewerStale:
				// The older release commit is fresh but stays untagged, so it
				// is a candidate, never the range base.
				older := r.Commit(titleCut, at(dateRelease))
				// A new pending subject, then a hand-built release: its bytes
				// do not match the generator, so the newer candidate is stale.
				commitLater(r, titleLaterPatch)
				next := laterVersion(t, r, intent.LevelPatch)
				r.WriteChangelog(r.fix.ChangelogTitle + "\n## " + next.String() + "\n\n- hand-written entry\n")
				r.SetVersionField(0, next)
				newer := r.Commit(titleCutAgain, at(dateLaterRelease))
				result := r.Run(argvPlan...)
				assertPlanRow(t, c, result)
				if got := outputValues(t, c.Name, result.Stdout)[intent.OutputCandidate]; got != newer {
					t.Fatalf("%s: candidate = %q, want the newer stale release %q", c.Name, got, newer)
				}
				if tags := r.TagsAt(older); len(tags) != 0 {
					t.Fatalf("%s: older release commit carries tags %v, want none", c.Name, tags)
				}
			case freshReadError:
				r.RemoveVersionFile(0)
				r.Commit(titleCut, at(dateRelease))
				result := r.Run(argvPlan...)
				if result.Exit != c.Expected.Exit.Status {
					t.Fatalf("%s: exit = %d, want %d\nstdout: %q", c.Name, result.Exit, c.Expected.Exit.Status, result.Stdout)
				}
				assertKeysAbsent(t, c.Name, result.Stdout, plainKeys(c.Expected.AbsentKeys))
			case freshRejectedLaterMinor, freshLaterSubjectChecksFull:
				r.Commit(titleCut, at(dateRelease))
				// A later minor that touches no changelog file: the candidate
				// stays fresh and the plan speaks for the before set, yet the
				// release content must be the full set.
				commitLater(r, titleLaterMinor)
				plan := r.Run(argvPlan...)
				if fresh := outputValues(t, c.Name, plan.Stdout)[intent.OutputCandidateFresh]; plan.Exit != exitAccepted || fresh != strconv.FormatBool(true) {
					t.Fatalf("%s: candidate must stay fresh, got exit=%d stdout=%q", c.Name, plan.Exit, plan.Stdout)
				}
				write := r.Run(argvWrite...)
				assertRunLines(t, c.Name, write, c.Expected.Exit.Status, c.Expected.Lines)
				version := outputValues(t, c.Name, write.Stdout)[intent.OutputVersion]
				changelog := r.ReadChangelog()
				for _, want := range []string{version, titleLaterMinor.Subject, titlePendingPatch.Subject} {
					if !strings.Contains(changelog, want) {
						t.Fatalf("%s: rewritten changelog must hold the %s section with both subjects, missing %q:\n%s", c.Name, version, want, changelog)
					}
				}
				if got := r.ReadVersionField(0); got != version {
					t.Fatalf("%s: rewritten version field = %q, want %q", c.Name, got, version)
				}
				if c.Input.Scenario == freshLaterSubjectChecksFull {
					// The check compares the tree with the same full set.
					assertRunLines(t, c.Name, r.Run(argvCheck...), c.Expected.Exit.Status, c.Expected.CheckLines)
				}
			case freshStaleRebuild:
				commitLater(r, titleLaterPatch)
				r.Commit(titleCut, at(dateLaterRelease))
				rebuild := r.Run(argvWrite...)
				if rebuild.Exit != c.Expected.Exit.Status {
					t.Fatalf("%s: rebuild exit = %d, want %d\nstdout: %q", c.Name, rebuild.Exit, c.Expected.Exit.Status, rebuild.Stdout)
				}
				version := outputValues(t, c.Name, rebuild.Stdout)[intent.OutputVersion]
				if version == "" {
					t.Fatalf("%s: the rebuild printed no version: %q", c.Name, rebuild.Stdout)
				}
				changelog := r.ReadChangelog()
				sections := 0
				for _, line := range strings.Split(changelog, "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), "## ") && strings.Contains(line, version) {
						sections++
					}
				}
				if sections != c.Expected.Sections {
					t.Fatalf("%s: rebuilt changelog has %d sections for %s, want %d (exactly one section per version)\n%s", c.Name, sections, version, c.Expected.Sections, changelog)
				}
			default:
				t.Fatalf("freshness scenario %q is in the closed set but has no builder", c.Input.Scenario)
			}
		})
	}
}

// assertPlanRow checks a row that ends on a plan: the exit status, the
// action and the candidate's freshness, each compared as its typed member.
func assertPlanRow(t *testing.T, c freshCase, result processResult) {
	t.Helper()
	if result.Exit != c.Expected.Exit.Status {
		t.Fatalf("%s: exit = %d, want %d\nstdout: %q\nstderr: %q", c.Name, result.Exit, c.Expected.Exit.Status, result.Stdout, result.Stderr)
	}
	got := outputValues(t, c.Name, result.Stdout)
	action, err := intent.ParseAction(got[intent.OutputAction])
	if err != nil {
		t.Fatalf("%s: the plan printed an action outside the closed set: %v", c.Name, err)
	}
	if action != c.Expected.Action {
		t.Fatalf("%s: action = %q, want %q", c.Name, action, c.Expected.Action)
	}
	if c.Expected.CandidateFresh == nil {
		t.Fatalf("%s: a row that ends on a plan must state candidate_fresh", c.Name)
	}
	if want := strconv.FormatBool(*c.Expected.CandidateFresh); got[intent.OutputCandidateFresh] != want {
		t.Fatalf("%s: %s = %q, want %q", c.Name, intent.OutputCandidateFresh, got[intent.OutputCandidateFresh], want)
	}
}

// assertRunLines checks one run's exit status and exact stdout lines.
func assertRunLines(t *testing.T, name string, result processResult, exit exitStatus, want []wantLine) {
	t.Helper()
	if result.Exit != exit {
		t.Fatalf("%s: exit = %d, want %d\nstdout: %q\nstderr: %q", name, result.Exit, exit, result.Stdout, result.Stderr)
	}
	assertStdoutLines(t, name, result.Stdout, plainLines(want))
}
