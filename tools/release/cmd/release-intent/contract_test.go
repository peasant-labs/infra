package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

type contractCLIInput struct {
	Command     intent.Command `yaml:"command"`
	Title       string         `yaml:"title"`
	Report      string         `yaml:"report"`
	State       string         `yaml:"state"`
	Until       string         `yaml:"until"`
	PrintBase   bool           `yaml:"print_base"`
	NoSurfaces  bool           `yaml:"no_surfaces"`
	LandedScope string         `yaml:"landed_scope"`
	RawSubject  string         `yaml:"raw_subject"`
	TagHead     bool           `yaml:"tag_head"`
	NoTag       bool           `yaml:"no_tag"`
	Adopting    bool           `yaml:"adopting"`
	Floor       string         `yaml:"floor"`
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
			result := runBinary(t, r.dir, args, row.Input.Title, nil)
			t.Logf("exit=%d stdout=%q stderr=%q", result.Exit, result.Stdout, result.Stderr)
			if result.Exit != row.Expected.Exit.Status || result.Stdout != row.Expected.Stdout || !strings.Contains(result.Stderr, row.Expected.Stderr) {
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
				t.Logf("plan:\n%s", data)
			}
		})
	}
}
