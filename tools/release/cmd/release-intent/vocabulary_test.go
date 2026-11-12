package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

type vocabularyFixture struct {
	Program  string `yaml:"program"`
	Commands []struct {
		Name  string `yaml:"name"`
		Usage string `yaml:"usage"`
		Flags []struct {
			Flag     string `yaml:"flag"`
			Required bool   `yaml:"required"`
		} `yaml:"flags"`
	} `yaml:"commands"`
	Flags []struct {
		Flag       string `yaml:"flag"`
		TakesValue bool   `yaml:"takes_value"`
	} `yaml:"flags"`
	GitSubcommands   []string `yaml:"git_subcommands"`
	ExitStatuses     []int    `yaml:"exit_statuses"`
	ReportedVerdicts []string `yaml:"reported_verdicts"`
	Rejected         struct {
		Commands         []string `yaml:"commands"`
		Flags            []string `yaml:"flags"`
		GitSubcommands   []string `yaml:"git_subcommands"`
		ReportedVerdicts []string `yaml:"reported_verdicts"`
	} `yaml:"rejected"`
}

func loadVocabularyFixture(t *testing.T) vocabularyFixture {
	t.Helper()
	data, err := cliFixtureYAML.ReadFile(fixtureDir + "/cli-vocabulary.yaml")
	if err != nil {
		t.Fatalf("read the vocabulary fixture: %v", err)
	}
	var f vocabularyFixture
	if err := strictDecode(data, &f); err != nil {
		t.Fatalf("parse the vocabulary fixture: %v", err)
	}
	return f
}

func spellings[T ~string](all []T) []string {
	out := make([]string, 0, len(all))
	for _, member := range all {
		out = append(out, string(member))
	}
	return out
}

// TestVocabulary_ExactMembership pins every closed set of the command line
// to the fixture's wire spellings, member for member and in order.
func TestVocabulary_ExactMembership(t *testing.T) {
	t.Parallel()
	f := loadVocabularyFixture(t)
	if intent.ProgramName != f.Program {
		t.Fatalf("program name = %q, want %q", intent.ProgramName, f.Program)
	}
	var commands, flags []string
	for _, c := range f.Commands {
		commands = append(commands, c.Name)
	}
	for _, fl := range f.Flags {
		flags = append(flags, fl.Flag)
	}
	if got := spellings(intent.AllCommands); !reflect.DeepEqual(got, commands) {
		t.Fatalf("AllCommands = %v, want exactly %v", got, commands)
	}
	if got := spellings(intent.AllFlags); !reflect.DeepEqual(got, flags) {
		t.Fatalf("AllFlags = %v, want exactly %v", got, flags)
	}
	if got := spellings(allGitSubcommands); !reflect.DeepEqual(got, f.GitSubcommands) {
		t.Fatalf("allGitSubcommands = %v, want exactly %v", got, f.GitSubcommands)
	}
	if got := spellings(reportedVerdicts()); !reflect.DeepEqual(got, f.ReportedVerdicts) {
		t.Fatalf("reportedVerdicts = %v, want exactly %v", got, f.ReportedVerdicts)
	}
	var statuses []int
	for _, s := range allExitStatuses {
		statuses = append(statuses, int(s))
	}
	if !reflect.DeepEqual(statuses, f.ExitStatuses) {
		t.Fatalf("allExitStatuses = %v, want exactly %v", statuses, f.ExitStatuses)
	}
}

// TestVocabulary_CommandFlagSets pins each command's flag set, which flags
// it requires, and the synopsis rendered from them. It also proves every
// flag is described and belongs to at least one command.
func TestVocabulary_CommandFlagSets(t *testing.T) {
	t.Parallel()
	f := loadVocabularyFixture(t)
	used := map[intent.Flag]bool{}
	for _, c := range f.Commands {
		command, err := intent.ParseCommand(c.Name)
		if err != nil {
			t.Fatalf("fixture command %q: %v", c.Name, err)
		}
		var want []flagUse
		for _, fl := range c.Flags {
			flag, err := intent.ParseFlag(fl.Flag)
			if err != nil {
				t.Fatalf("fixture command %q flag %q: %v", c.Name, fl.Flag, err)
			}
			want = append(want, flagUse{Flag: flag, Required: fl.Required})
			used[flag] = true
		}
		if got := flagsOf(command); !reflect.DeepEqual(got, want) {
			t.Fatalf("flagsOf(%s) = %v, want exactly %v", command, got, want)
		}
		if got := usageOf(command); got != c.Usage {
			t.Fatalf("usageOf(%s) = %q, want %q", command, got, c.Usage)
		}
	}
	for _, fl := range f.Flags {
		flag, err := intent.ParseFlag(fl.Flag)
		if err != nil {
			t.Fatalf("fixture flag %q: %v", fl.Flag, err)
		}
		shape := shapeOf(flag)
		if shape.Meaning == "" {
			t.Fatalf("flag %s has no description in shapeOf", flag)
		}
		if takesValue(flag) != fl.TakesValue {
			t.Fatalf("takesValue(%s) = %v, want %v", flag, takesValue(flag), fl.TakesValue)
		}
		if !used[flag] {
			t.Fatalf("flag %s belongs to no command", flag)
		}
	}
}

// TestVocabulary_RefusesUnknownWords proves each parser refuses a word
// outside its set and never substitutes a member.
func TestVocabulary_RefusesUnknownWords(t *testing.T) {
	t.Parallel()
	f := loadVocabularyFixture(t)
	for _, raw := range f.Rejected.Commands {
		if got, err := parseCommand(raw); err == nil || got != "" {
			t.Fatalf("parseCommand(%q) = %q, %v; want a refusal and no command", raw, got, err)
		}
	}
	for _, raw := range f.Rejected.Flags {
		if got, err := intent.ParseFlag(raw); err == nil || got != "" {
			t.Fatalf("ParseFlag(%q) = %q, %v; want a refusal and no flag", raw, got, err)
		}
		for _, command := range intent.AllCommands {
			if _, err := parseFlags(command, []string{raw}); err == nil {
				t.Fatalf("parseFlags(%s, [%q]) accepted an unknown flag", command, raw)
			}
		}
	}
	for _, raw := range f.Rejected.GitSubcommands {
		if gitSubcommand(raw).IsValid() {
			t.Fatalf("git subcommand %q must not be on the runner's allowlist", raw)
		}
	}
	for _, raw := range f.Rejected.ReportedVerdicts {
		if got, ok := parseReportedVerdict(raw); ok || got != intent.VerdictNone {
			t.Fatalf("parseReportedVerdict(%q) = %q, %v; want a refusal", raw, got, ok)
		}
	}
}

// TestRun_EveryCommandHasAHandler proves the dispatch switch covers the
// closed command set: with no arguments every command reaches its own flag
// check or handler, never the missing-handler error.
func TestRun_EveryCommandHasAHandler(t *testing.T) {
	f := loadVocabularyFixture(t)
	for _, c := range f.Commands {
		command, err := intent.ParseCommand(c.Name)
		if err != nil {
			t.Fatalf("fixture command %q: %v", c.Name, err)
		}
		result := runBinary(t, t.TempDir(), argvOf(command), "", nil)
		if result.Exit == exitAccepted {
			continue
		}
		if strings.Contains(result.Stderr, "has no handler") {
			t.Fatalf("%s: dispatch has no handler: %s", command, result.Stderr)
		}
	}
}
