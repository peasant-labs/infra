package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

type contractCLIInput struct {
	Command             intent.Command `yaml:"command"`
	Title               string         `yaml:"title"`
	Report              string         `yaml:"report"`
	State               string         `yaml:"state"`
	Until               string         `yaml:"until"`
	PrintBase           bool           `yaml:"print_base"`
	NoSurfaces          bool           `yaml:"no_surfaces"`
	LandedScope         string         `yaml:"landed_scope"`
	RawSubject          string         `yaml:"raw_subject"`
	TagHead             bool           `yaml:"tag_head"`
	NoTag               bool           `yaml:"no_tag"`
	Adopting            bool           `yaml:"adopting"`
	Floor               string         `yaml:"floor"`
	MigrationShape      bool           `yaml:"migration_shape"`
	FreezeFloor         string         `yaml:"freeze_floor"`
	PreMigrationSubject string         `yaml:"pre_migration_subject"`
	MigrationSubject    string         `yaml:"migration_subject"`
}

type contractCLIExpected struct {
	Exit   wantExit `yaml:"exit"`
	Stdout string   `yaml:"stdout"`
	Stderr string   `yaml:"stderr"`
	To     string   `yaml:"to"`
}

func TestCLIContract(t *testing.T) {
	for _, row := range loadCorpus[contractCLIInput, contractCLIExpected](t, "cli-contract.yaml") {
		t.Run(row.Name, func(t *testing.T) {
			r := newTestRepo(t, repoStandard)
			if !row.Input.NoSurfaces {
				r.cfg.Surfaces = map[string][]string{"local-api": {"local"}}
				r.cfg.SpecFreeze = true
				r.cfg.FreezeDir = "generated"
			}
			r.Seed()
			if row.Input.Adopting {
				if err := os.Remove(filepath.Join(r.dir, intent.DefaultConfigPath)); err != nil {
					t.Fatal(err)
				}
			}
			r.Commit(titleDocs)
			base := r.Head()
			if !row.Input.NoTag {
				r.Tag("0.1.0")
			}
			if row.Input.Adopting {
				r.WriteConfig(r.cfg)
			}
			migration := ""
			if row.Input.MigrationShape {
				r.WriteFile("feature.txt", "feature\n")
				r.CommitRaw(row.Input.PreMigrationSubject)
				r.WriteFile("generated/local.json", "working spec\n")
				r.CommitRaw(row.Input.MigrationSubject)
				migration = r.Head()
			}
			r.WriteFile("work.txt", "change\n")
			scope := row.Input.LandedScope
			if scope == "" {
				scope = "local-api"
			}
			if row.Input.RawSubject != "" {
				r.CommitRaw(row.Input.RawSubject)
			} else {
				r.Commit(titled(intent.TypeFeat, scope, intent.LevelMinor, "change"))
			}
			head := r.Head()
			side := ""
			if row.Input.FreezeFloor == "${SIDE}" {
				mustGitIn(t, r.dir, "checkout", "-q", "-b", "side")
				r.WriteFile("side.txt", "side\n")
				r.Commit(titleDocs)
				side = r.Head()
				r.Checkout(r.Trunk())
			}
			if row.Input.TagHead {
				r.Tag("0.2.0")
			}
			args := argvOf(row.Input.Command)
			scratch := t.TempDir()
			report := filepath.Join(scratch, "report.yaml")
			state := filepath.Join(scratch, "state.yaml")
			out := filepath.Join(scratch, "plan.yaml")
			if row.Input.Report != "" {
				data := strings.NewReplacer("${BASE}", base, "${HEAD}", head).Replace(row.Input.Report)
				if err := os.WriteFile(report, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, intent.FlagReport.String(), report)
			}
			if row.Input.Command == intent.CommandFreezePlan {
				if row.Input.State != "" {
					if err := os.WriteFile(state, []byte(row.Input.State), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				args = append(args, intent.FlagBranch.String(), r.Trunk().String(), intent.FlagState.String(), state, intent.FlagOut.String(), out)
			}
			if row.Input.Until != "" {
				args = append(args, intent.FlagUntil.String(), row.Input.Until)
			}
			if row.Input.PrintBase {
				args = append(args, intent.FlagPrintRangeBase.String())
			}
			if row.Input.Floor != "" {
				args = append(args, intent.FlagFloor.String(), strings.ReplaceAll(row.Input.Floor, "${HEAD}", head))
			}
			freezeFloor := strings.NewReplacer("${MIGRATION}", migration, "${SIDE}", side).Replace(row.Input.FreezeFloor)
			if row.Input.FreezeFloor != "" {
				args = append(args, intent.FlagFreezeFloor.String(), freezeFloor)
			}
			result := runBinary(t, r.dir, args, row.Input.Title, nil)
			t.Logf("exit=%d stdout=%q stderr=%q", result.Exit, result.Stdout, result.Stderr)
			stderr := strings.ReplaceAll(row.Expected.Stderr, "${SIDE}", side)
			if result.Exit != row.Expected.Exit.Status || result.Stdout != row.Expected.Stdout || !strings.Contains(result.Stderr, stderr) {
				t.Fatalf("got %+v; want %+v", result, row.Expected)
			}
			if row.Expected.To != "" {
				data, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := intent.ParseFreezePlan(data, r.cfg)
				if err != nil || len(plan.Surfaces) != 1 || plan.Surfaces[0].To.String() != row.Expected.To {
					t.Fatalf("plan=%+v error=%v\n%s", plan, err, data)
				}
				if plan.FreezeFloor != freezeFloor || strings.Contains(string(data), "freeze_floor:") != (freezeFloor != "") {
					t.Fatalf("unexpected freeze floor %q in plan:\n%s", plan.FreezeFloor, data)
				}
				t.Logf("plan:\n%s", data)
			}
		})
	}
}
