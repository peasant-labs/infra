package main

import (
	"bytes"
	"embed"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	"gopkg.in/yaml.v3"
)

//go:embed testdata/*.yaml
var cliFixtureYAML embed.FS

//go:embed testdata/queue/*.json
var queueJSONFS embed.FS

//go:embed testdata/workflows
var workflowTreesFS embed.FS

const (
	fixtureDir      = "testdata"
	queueFixtureDir = fixtureDir + "/queue"
)

// corpusFile is a CLI corpus document: a required-name manifest and the rows.
type corpusFile[I any, E any] struct {
	RequiredNames []string              `yaml:"required_names"`
	Cases         testcase.Corpus[I, E] `yaml:"cases"`
}

// loadCorpus reads one corpus fixture and applies the checks every CLI
// corpus shares: valid rows, the exact required-name manifest, and an accept
// sibling on every must-fail row. Closed-set fields decode straight into
// their typed owners, so a row with an unknown member fails here, at load.
func loadCorpus[I any, E any](t *testing.T, file string) []testcase.Case[I, E] {
	t.Helper()
	data, err := cliFixtureYAML.ReadFile(fixtureDir + "/" + file)
	if err != nil {
		t.Fatalf("read fixture %q: %v", file, err)
	}
	var f corpusFile[I, E]
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixture %q: %v", file, err)
	}
	if err := f.Cases.Validate(); err != nil {
		t.Fatalf("fixture %q: %v", file, err)
	}
	seen := map[string]struct{}{}
	for _, c := range f.Cases.Cases {
		seen[c.Name] = struct{}{}
	}
	checkRequiredNames(t, f.RequiredNames, seen, file)
	checkCorpusSiblings(t, data, file)
	return f.Cases.Cases
}

// checkRequiredNames asserts exact membership: every required name is a
// case, and every case is a required name.
func checkRequiredNames(t *testing.T, required []string, seen map[string]struct{}, what string) {
	t.Helper()
	if len(required) == 0 {
		t.Fatalf("%s: required_names is empty; a corpus with no named case can lose coverage silently", what)
	}
	want := map[string]struct{}{}
	for _, name := range required {
		if _, dup := want[name]; dup {
			t.Fatalf("%s: required name %q is listed twice", what, name)
		}
		want[name] = struct{}{}
		if _, ok := seen[name]; !ok {
			t.Fatalf("%s: required case %q is missing", what, name)
		}
	}
	for name := range seen {
		if _, ok := want[name]; !ok {
			t.Fatalf("%s: case %q is not in required_names", what, name)
		}
	}
}

// checkCorpusSiblings enforces the accept-sibling rule: every must-fail row
// names an accept sibling in the same corpus, so a reject proves it fails for
// the named fault and not for an unrelated input error.
func checkCorpusSiblings(t *testing.T, data []byte, what string) {
	t.Helper()
	var doc struct {
		Cases struct {
			Cases []struct {
				Name           string                  `yaml:"name"`
				Classification testcase.Classification `yaml:"classification"`
				Sibling        string                  `yaml:"sibling"`
			} `yaml:"cases"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: cannot parse sibling references: %v", what, err)
	}
	byName := map[string]testcase.Classification{}
	for _, c := range doc.Cases.Cases {
		byName[c.Name] = c.Classification
	}
	for _, c := range doc.Cases.Cases {
		if c.Classification != testcase.MustFail {
			continue
		}
		if c.Sibling == "" {
			t.Fatalf("%s: must-fail case %q names no accept sibling", what, c.Name)
		}
		class, ok := byName[c.Sibling]
		if !ok {
			t.Fatalf("%s: must-fail case %q names missing sibling %q", what, c.Name, c.Sibling)
		}
		if class != testcase.MustPass {
			t.Fatalf("%s: must-fail case %q names sibling %q which is %q, not must-pass", what, c.Name, c.Sibling, class)
		}
	}
}

// --- typed expectations ---

// wantExit is an expected exit status, decoded into the command's closed set.
type wantExit struct{ Status exitStatus }

func (w *wantExit) UnmarshalYAML(node *yaml.Node) error {
	var n int
	if err := node.Decode(&n); err != nil {
		return err
	}
	status := exitStatus(n)
	if !slices.Contains(allExitStatuses, status) {
		return fmt.Errorf("line %d: exit status %d is not in the closed set %v", node.Line, n, allExitStatuses)
	}
	w.Status = status
	return nil
}

// wantLine is one expected stdout line in its wire spelling, parsed at load
// into the typed key and a value checked against the key's own closed set.
// The comparison is still the exact line the key renders.
type wantLine struct{ outputLine }

func (w *wantLine) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	key, value, err := intent.ParseOutputLine(raw)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	if err := checkOutputValue(key, value); err != nil {
		return fmt.Errorf("line %d: expected line %q: %w", node.Line, raw, err)
	}
	w.outputLine = out(key, value)
	return nil
}

// checkOutputValue parses the value of a key whose values are a closed set
// into that set's typed owner. An empty value is the key's "nothing" where
// the command prints one. Keys with open values pass through.
func checkOutputValue(key intent.OutputKey, value string) error {
	var err error
	switch key {
	case intent.OutputAction:
		_, err = intent.ParseAction(value)
	case intent.OutputGate:
		_, err = intent.ParseGate(value)
	case intent.OutputKind:
		_, err = intent.ParsePRKind(value)
	case intent.OutputLevel:
		_, err = intent.ParseLevel(value)
	case intent.OutputError:
		if value != "" {
			_, err = intent.ParseErrorCode(value)
		}
	case intent.OutputVersion:
		if value != "" {
			_, err = intent.ParseSemVer(value)
		}
	case intent.OutputCandidateFresh:
		if value != "" {
			_, err = strconv.ParseBool(value)
		}
	case intent.OutputFresh, intent.OutputPush, intent.OutputBreaking, intent.OutputOK:
		_, err = strconv.ParseBool(value)
	}
	return err
}

func plainLines(want []wantLine) []string {
	out := make([]string, 0, len(want))
	for _, w := range want {
		out = append(out, w.String())
	}
	return out
}

// splitLines splits captured stdout into its lines.
func splitLines(stdout string) []string {
	trimmed := strings.TrimSuffix(stdout, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// assertStdoutLines asserts stdout is exactly the expected lines, in order.
func assertStdoutLines(t *testing.T, name, stdout string, want []string) {
	t.Helper()
	lines := splitLines(stdout)
	if len(lines) != len(want) {
		t.Fatalf("%s: stdout has %d lines %q, want %d %q (exact match, in order)", name, len(lines), stdout, len(want), want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("%s: stdout line %d = %q, want %q (exact match)", name, i, lines[i], want[i])
		}
	}
}

// outputValues reads stdout as typed output lines. A line that is not a
// key=value line of the closed key set fails the test.
func outputValues(t *testing.T, name, stdout string) map[intent.OutputKey]string {
	t.Helper()
	values := map[intent.OutputKey]string{}
	for _, line := range splitLines(stdout) {
		key, value, err := intent.ParseOutputLine(line)
		if err != nil {
			t.Fatalf("%s: stdout line %q is not a typed output line: %v", name, line, err)
		}
		values[key] = value
	}
	return values
}

// assertKeysAbsent asserts no stdout line carries one of the keys.
func assertKeysAbsent(t *testing.T, name, stdout string, absent []intent.OutputKey) {
	t.Helper()
	for _, line := range splitLines(stdout) {
		key, _, err := intent.ParseOutputLine(line)
		if err != nil {
			continue
		}
		if slices.Contains(absent, key) {
			t.Fatalf("%s: stdout %q must not carry a %q line", name, stdout, key)
		}
	}
}

// argvOf builds a command line from typed members: the command, then flags
// and their values.
func argvOf(command intent.Command, words ...any) []string {
	argv := []string{command.String()}
	for _, w := range words {
		argv = append(argv, fmt.Sprint(w))
	}
	return argv
}

// strictDecode decodes one YAML document and refuses unknown keys.
func strictDecode(data []byte, into any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(into)
}

// wantKey is an output key in its wire spelling, parsed into the closed set.
type wantKey struct{ Key intent.OutputKey }

func (w *wantKey) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	key, err := intent.ParseOutputKey(raw)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	w.Key = key
	return nil
}

func plainKeys(want []wantKey) []intent.OutputKey {
	out := make([]intent.OutputKey, 0, len(want))
	for _, w := range want {
		out = append(out, w.Key)
	}
	return out
}

// decodeMember decodes a YAML scalar into a member of a test-side closed
// set, refusing anything else.
func decodeMember[T ~string](node *yaml.Node, all []T, set string) (T, error) {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return "", err
	}
	member := T(raw)
	if !slices.Contains(all, member) {
		return "", fmt.Errorf("line %d: unknown %s %q: want one of %v", node.Line, set, raw, all)
	}
	return member, nil
}
