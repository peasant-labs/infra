package policy

import (
	_ "embed"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// manifestNames holds a corpus to its required-name manifest: every case
// named once and listed, and every listed name present.
func manifestNames(t *testing.T, corpus string, required, names []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			t.Fatalf("%s: case name %q is empty or repeated", corpus, name)
		}
		seen[name] = true
		if !slices.Contains(required, name) {
			t.Fatalf("%s: case %q is not in required_names", corpus, name)
		}
	}
	for _, name := range required {
		if !seen[name] {
			t.Fatalf("%s: required case %q is missing", corpus, name)
		}
	}
}

//go:embed testdata/shell_commands.yaml
var shellCommandsFixtureYAML []byte

// TestShellCommands drives the shell lexer over its corpus: each run block's
// simple commands as bare words, in the order the lexer finds them, and
// whether the block lexed completely.
func TestShellCommands(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string `yaml:"required_names"`
		Cases         []struct {
			Name     string     `yaml:"name"`
			Input    string     `yaml:"input"`
			Commands [][]string `yaml:"commands"`
			Complete bool       `yaml:"complete"`
		} `yaml:"cases"`
	}
	if err := strictUnmarshal(shellCommandsFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/shell_commands.yaml: %v", err)
	}
	var names []string
	for _, c := range fx.Cases {
		names = append(names, c.Name)
	}
	manifestNames(t, "shell_commands.yaml", fx.RequiredNames, names)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			cmds, complete := shellCommands(tc.Input)
			got := make([][]string, 0, len(cmds))
			for _, cmd := range cmds {
				got = append(got, cmd.bareWords())
			}
			if !slices.EqualFunc(got, tc.Commands, slices.Equal[[]string]) {
				t.Fatalf("shellCommands(%q) = %q, want %q", tc.Input, got, tc.Commands)
			}
			if complete != tc.Complete {
				t.Fatalf("shellCommands(%q) complete = %v, want %v", tc.Input, complete, tc.Complete)
			}
		})
	}
}

//go:embed testdata/ref_writes.yaml
var refWritesFixtureYAML []byte

// refWriteRow is one expected write. Kind is parsed into its closed member
// at load; Why is a fragment of an unproven write's reason.
type refWriteRow struct {
	Kind   string `yaml:"kind"`
	Branch string `yaml:"branch"`
	Tag    string `yaml:"tag"`
	Why    string `yaml:"why"`
}

// TestRefWrites drives the production ref-write reader over every push form
// and every gh or API write the corpus samples. A row gives either a run
// block or a whole step (to sample the shell and env scopes); the writes
// must match in order, kind, target, and reason.
func TestRefWrites(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string `yaml:"required_names"`
		Cases         []struct {
			Name   string        `yaml:"name"`
			Run    string        `yaml:"run"`
			Step   yaml.Node     `yaml:"step"`
			Writes []refWriteRow `yaml:"writes"`
		} `yaml:"cases"`
	}
	if err := strictUnmarshal(refWritesFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/ref_writes.yaml: %v", err)
	}
	var names []string
	for _, c := range fx.Cases {
		names = append(names, c.Name)
	}
	manifestNames(t, "ref_writes.yaml", fx.RequiredNames, names)
	sampled := map[refWriteKind]bool{}
	for _, tc := range fx.Cases {
		want := make([]refWrite, 0, len(tc.Writes))
		for _, w := range tc.Writes {
			kind, err := parseRefWriteKind(w.Kind)
			if err != nil {
				t.Fatalf("case %q: %v", tc.Name, err)
			}
			sampled[kind] = true
			want = append(want, refWrite{kind: kind, branch: w.Branch, tag: w.Tag, why: w.Why})
		}
		var got []refWrite
		if tc.Step.Kind == yaml.MappingNode {
			got = stepRefWrites(&tc.Step, nil, nil)
		} else {
			got = runRefWrites(tc.Run)
		}
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			if len(got) != len(want) {
				t.Fatalf("writes = %s, want %s", describeWrites(got), describeWrites(want))
			}
			for i := range want {
				g, w := got[i], want[i]
				if g.kind != w.kind || g.branch != w.branch || g.tag != w.tag || !strings.Contains(g.why, w.why) {
					t.Fatalf("write %d = %s, want %s", i, describeWrites(got[i:i+1]), describeWrites(want[i:i+1]))
				}
			}
		})
	}
	for _, kind := range allRefWriteKinds {
		if !sampled[kind] {
			t.Errorf("no case samples ref write kind %s", kind)
		}
	}
}

func describeWrites(writes []refWrite) string {
	var parts []string
	for _, w := range writes {
		parts = append(parts, "{"+w.kind.String()+" branch="+w.branch+" tag="+w.tag+" why="+w.why+"}")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

//go:embed testdata/push_mutations.yaml
var pushMutationsFixtureYAML []byte

// TestPushTargetMutations copies the realistic consumer workflow set (the
// queue corpus base: a release job, two bot jobs, a guard and CI) into a temp
// dir, proves the copy passes, then changes only one push target per row and
// runs the production checker. Each mutation must fire exactly its listed
// rules, carrying the listed fragments. The rows cover each push form against
// each job class that pushes.
func TestPushTargetMutations(t *testing.T) {
	t.Parallel()
	var fx struct {
		RequiredNames []string `yaml:"required_names"`
		Cases         []struct {
			Name         string      `yaml:"name"`
			Workflow     string      `yaml:"workflow"`
			Find         string      `yaml:"find"`
			Replace      string      `yaml:"replace"`
			Rules        []QueueRule `yaml:"rules"`
			WantContains []string    `yaml:"want_contains"`
		} `yaml:"cases"`
	}
	if err := strictUnmarshal(pushMutationsFixtureYAML, &fx); err != nil {
		t.Fatalf("parse testdata/push_mutations.yaml: %v", err)
	}
	var names []string
	for _, c := range fx.Cases {
		names = append(names, c.Name)
	}
	manifestNames(t, "push_mutations.yaml", fx.RequiredNames, names)

	check := func(t *testing.T, dir string) []Diagnostic {
		t.Helper()
		policy, err := LoadQueuePolicy(filepath.Join(dir, DefaultPath))
		if err != nil {
			t.Fatalf("policy does not load: %v", err)
		}
		cfg, err := intent.LoadConfig(filepath.Join(dir, intent.DefaultConfigPath))
		if err != nil {
			t.Fatalf("release config does not load: %v", err)
		}
		diags, _ := CheckWorkflows(policy, cfg, filepath.Join(dir, fixtureWorkflowsDir))
		return diags
	}
	if diags := check(t, materializeQueueFixture(t, "")); len(diags) != 0 {
		t.Fatalf("the unmutated copy yields diagnostics: %+v", diags)
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			if len(tc.Rules) == 0 || len(tc.WantContains) == 0 {
				t.Fatal("a mutation row names no rule or no fragment (fixture error)")
			}
			dir := materializeQueueFixture(t, "")
			file := filepath.Join(dir, fixtureWorkflowsDir, tc.Workflow)
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(string(data), tc.Find); n != 1 {
				t.Fatalf("find %q occurs %d times in %s, want exactly once", tc.Find, n, tc.Workflow)
			}
			mutated := strings.Replace(string(data), tc.Find, tc.Replace, 1)
			if err := os.WriteFile(file, []byte(mutated), 0o644); err != nil {
				t.Fatal(err)
			}
			diags := check(t, dir)
			fired := map[QueueRule]bool{}
			var msgs []string
			for _, d := range diags {
				fired[d.Rule] = true
				msgs = append(msgs, d.Message)
			}
			joined := strings.Join(msgs, "\n")
			for _, rule := range AllQueueRules {
				if fired[rule] != slices.Contains(tc.Rules, rule) {
					t.Fatalf("rule %s fired = %v, want the exact set %v:\n%s", rule, fired[rule], tc.Rules, joined)
				}
			}
			for _, want := range tc.WantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("diagnostics do not contain %q:\n%s", want, joined)
				}
			}
		})
	}
}

// strictUnmarshal decodes a corpus with unknown fields refused.
func strictUnmarshal(data []byte, out any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	return dec.Decode(out)
}
