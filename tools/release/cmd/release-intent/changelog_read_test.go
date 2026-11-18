package main

import (
	"context"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

type releaseReadScenario string

const (
	readSourceTip       releaseReadScenario = "source-tip"
	readCandidateParent releaseReadScenario = "candidate-parent"
)

var allReleaseReadScenarios = []releaseReadScenario{readSourceTip, readCandidateParent}

func (s *releaseReadScenario) UnmarshalYAML(node *yaml.Node) error {
	member, err := decodeMember(node, allReleaseReadScenarios, "release changelog read")
	*s = member
	return err
}

type releaseReadInput struct {
	Scenario releaseReadScenario `yaml:"scenario"`
	Fail     bool                `yaml:"fail"`
}

type releaseReadExpected struct {
	Exit    wantExit `yaml:"exit"`
	Message string   `yaml:"message"`
}

// TestReleaseChangelogReadFailures injects blob-fetch failures at the new
// source-tip and candidate-parent boundaries while leaving Git history real.
func TestReleaseChangelogReadFailures(t *testing.T) {
	for _, row := range loadCorpus[releaseReadInput, releaseReadExpected](t, "changelog-read-failures.yaml") {
		t.Run(row.Name, func(t *testing.T) {
			ctx := context.Background()
			r := newPendingRepo(t, repoStandard)
			if row.Input.Scenario == readCandidateParent {
				mustWrite(r, row.Name)
				r.Commit(titleCut)
			}
			runner := r.Runner()
			readRev := r.Head()
			if row.Input.Scenario == readCandidateParent {
				readRev = r.Parent()
			}
			if row.Input.Fail {
				runner = changelogReadFailureRunner{GitRunner: runner, rev: readRev, path: r.cfg.Changelog}
			}
			facts, cfg, err := Gather(ctx, runner, intent.CommandPlan, r.Head(), intent.DefaultConfigPath, "", intent.ModeTag, intent.VerdictNone)
			if err == nil && row.Input.Scenario == readSourceTip {
				var decision intent.Decision
				decision, err = intent.Plan(cfg, facts)
				if err == nil {
					_, err = releaseContent(ctx, runner, cfg, facts, decision)
				}
			}
			if status := statusOf(err); status != row.Expected.Exit.Status {
				t.Fatalf("exit = %d, want %d: %v", status, row.Expected.Exit.Status, err)
			}
			if row.Expected.Message != "" && (err == nil || !strings.Contains(err.Error(), row.Expected.Message)) {
				t.Fatalf("error %v does not contain %q", err, row.Expected.Message)
			}
		})
	}
}
