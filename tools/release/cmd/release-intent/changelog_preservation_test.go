package main

import (
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	"gopkg.in/yaml.v3"
)

type preservationMode string

const (
	preservationWrite  preservationMode = "write"
	preservationCommit preservationMode = "commit"
	preservationStale  preservationMode = "stale"
)

var allPreservationModes = []preservationMode{preservationWrite, preservationCommit, preservationStale}

func (s *preservationMode) UnmarshalYAML(node *yaml.Node) error {
	member, err := decodeMember(node, allPreservationModes, "changelog preservation mode")
	*s = member
	return err
}

type preservationInput struct {
	Mode            preservationMode `yaml:"mode"`
	SeedVersion     string           `yaml:"seed_version"`
	BaseChangelog   string           `yaml:"base_changelog"`
	Corrected       string           `yaml:"corrected_changelog"`
	CorrectionTitle string           `yaml:"correction_title"`
	PendingTitle    string           `yaml:"pending_title"`
	ReleaseTitle    string           `yaml:"release_title"`
	AfterCandidate  string           `yaml:"after_candidate"`
	Contains        []string         `yaml:"contains"`
	Absent          []string         `yaml:"absent"`
	TamperFind      string           `yaml:"tamper_find"`
	TamperReplace   string           `yaml:"tamper_replace"`
	ExpectedVersion string           `yaml:"expected_version"`
}

type preservationExpected struct {
	CheckExit wantExit `yaml:"check_exit"`
}

type preservationCase = testcase.Case[preservationInput, preservationExpected]

func TestCorrectionPreservation(t *testing.T) {
	for _, row := range loadCorpus[preservationInput, preservationExpected](t, "changelog-preservation.yaml") {
		t.Run(row.Name, func(t *testing.T) {
			checkCorrectionPreservation(t, row)
		})
	}
}

// checkCorrectionPreservation runs the built CLI against a disposable Git
// history, including the production title guard, writer, checker and planner.
func checkCorrectionPreservation(t *testing.T, row preservationCase) {
	t.Helper()
	in := row.Input
	r := newTestRepo(t, repoAllBots)
	r.Seed()
	r.WriteChangelog(in.BaseChangelog)
	r.Commit(titleSeed, at(dateSeed))
	seed, err := intent.ParseSemVer(in.SeedVersion)
	if err != nil {
		t.Fatalf("parse fixture seed version: %v", err)
	}
	r.TagAnnotated(seed)
	parseTitle := func(text string) intent.Title {
		title, err := intent.ParseTitle(r.cfg, seed, text)
		if err != nil {
			t.Fatalf("parse fixture title %q: %v", text, err)
		}
		return title
	}
	r.WriteChangelog(in.Corrected)
	correction := r.Commit(parseTitle(in.CorrectionTitle), at(datePending))
	guard := runBinary(t, r.Dir(), argvOf(intent.CommandCheckTitle,
		intent.FlagBase, r.Parent(), intent.FlagHead, correction,
		intent.FlagAuthor, "octocat", intent.FlagHeadBranch, "feature/correction"), in.CorrectionTitle+"\n", nil)
	if guard.Exit != exitAccepted {
		t.Fatalf("correction guard rejected: exit %d: %s", guard.Exit, guard.Stderr)
	}
	assertCorrectionPlan(t, r.Run(argvPlan...), intent.ActionClosePR, "", "", "")

	r.WriteFile("work.txt", "a releasing change\n")
	source := r.Commit(parseTitle(in.PendingTitle), at(dateLater))
	assertCorrectionPlan(t, r.Run(argvPlan...), intent.ActionOpenPR, "", in.ExpectedVersion, "")

	switch in.Mode {
	case preservationWrite, preservationStale:
		mustWrite(r, row.Name)
	case preservationCommit:
		r.CreateBranch(r.BotBranch(intent.PRKindRelease))
		result := r.Run(argvOf(intent.CommandReleasePR, intent.FlagBranch, source, intent.FlagCommit)...)
		if result.Exit != exitAccepted {
			t.Fatalf("release-pr --commit failed: exit %d: %s", result.Exit, result.Stderr)
		}
	default:
		t.Fatalf("unknown preservation mode %q", in.Mode)
	}
	content := r.ReadChangelog()
	for _, want := range in.Contains {
		if !strings.Contains(content, want) {
			t.Fatalf("corrected text missing %q from generated changelog:\n%s", want, content)
		}
	}
	for _, unwanted := range in.Absent {
		if strings.Contains(content, unwanted) {
			t.Fatalf("generated changelog retains unwanted %q:\n%s", unwanted, content)
		}
	}
	if in.Mode == preservationStale {
		if strings.Count(content, in.TamperFind) != 1 {
			t.Fatalf("tamper anchor %q is not unique", in.TamperFind)
		}
		r.WriteChangelog(strings.Replace(content, in.TamperFind, in.TamperReplace, 1))
	}
	check := r.Run(argvOf(intent.CommandReleasePR, intent.FlagBranch, source, intent.FlagCheck)...)
	if check.Exit != row.Expected.CheckExit.Status {
		t.Fatalf("release-pr --check exit = %d, want %d; stdout %q; stderr %q", check.Exit, row.Expected.CheckExit.Status, check.Stdout, check.Stderr)
	}
	if in.Mode == preservationStale {
		if !strings.Contains(check.Stdout, "error=release-content-stale") {
			t.Fatalf("tampered content must be rejected as stale: %q", check.Stdout)
		}
		return
	}

	release := r.Head()
	if in.Mode == preservationWrite {
		release = r.Commit(parseTitle(in.ReleaseTitle), at(dateLaterRelease))
	} else {
		// The generated commit is the first-parent result of landing the PR.
		r.Checkout(r.Trunk())
		r.git(verbMerge, "--ff-only", r.branchName(r.BotBranch(intent.PRKindRelease)))
	}
	assertCorrectionPlan(t, r.Run(argvPlan...), intent.ActionTag, release, in.ExpectedVersion, "true")

	// A later correction must not displace the real release candidate.
	r.AppendChangelog("\n" + in.AfterCandidate + "\n")
	r.Commit(parseTitle(in.CorrectionTitle), at(dateLaterRelease))
	assertCorrectionPlan(t, r.Run(argvPlan...), intent.ActionTag, release, in.ExpectedVersion, "true")
}

func assertCorrectionPlan(t *testing.T, result processResult, action intent.Action, candidate, version, fresh string) {
	t.Helper()
	if result.Exit != exitAccepted {
		t.Fatalf("plan failed: exit %d; stdout %q; stderr %q", result.Exit, result.Stdout, result.Stderr)
	}
	got := outputValues(t, "correction plan", result.Stdout)
	if got[intent.OutputAction] != action.String() || got[intent.OutputCandidate] != candidate ||
		got[intent.OutputVersion] != version || got[intent.OutputCandidateFresh] != fresh {
		t.Fatalf("candidate plan = action %q candidate %q version %q fresh %q; want %q %q %q %q",
			got[intent.OutputAction], got[intent.OutputCandidate], got[intent.OutputVersion], got[intent.OutputCandidateFresh],
			action, candidate, version, fresh)
	}
}
