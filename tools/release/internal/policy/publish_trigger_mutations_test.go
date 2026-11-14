package policy

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// The committed source is byte-for-byte from the named Fairtrade commit, so
// a shallow infra checkout never needs another repository or a network fetch.
//
//go:embed all:testdata/fairtrade-pilot testdata/publish_trigger_mutations.yaml
var publishTriggerFixtures embed.FS

type pilotEdit struct {
	File    string `yaml:"file"`
	Find    string `yaml:"find"`
	Replace string `yaml:"replace"`
}

type pilotSource struct {
	File   string `yaml:"file"`
	SHA256 string `yaml:"sha256"`
}

type publishTriggerCase struct {
	Name         string      `yaml:"name"`
	Trigger      string      `yaml:"trigger"`
	DropNeed     bool        `yaml:"drop_need"`
	Edits        []pilotEdit `yaml:"edits"`
	Rules        []QueueRule `yaml:"rules"`
	WantContains []string    `yaml:"want_contains"`
	Sibling      string      `yaml:"sibling"`
}

type publishTriggerCorpus struct {
	Repository    string               `yaml:"repository"`
	Commit        string               `yaml:"commit"`
	Source        []pilotSource        `yaml:"source"`
	Preparation   []pilotEdit          `yaml:"preparation"`
	RequiredNames []string             `yaml:"required_names"`
	Cases         []publishTriggerCase `yaml:"cases"`
}

func applyPilotEdit(t *testing.T, dir string, edit pilotEdit) {
	t.Helper()
	file := filepath.Join(dir, edit.File)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), edit.Find); count != 1 || edit.Find == "" {
		t.Fatalf("%s: mutation anchor occurs %d times, want exactly once: %q", edit.File, count, edit.Find)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(data), edit.Find, edit.Replace, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func materializePilot(t *testing.T, preparation []pilotEdit) string {
	t.Helper()
	dir := t.TempDir()
	const root = "testdata/fairtrade-pilot"
	if err := fs.WalkDir(publishTriggerFixtures, root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := publishTriggerFixtures.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	for _, edit := range preparation {
		applyPilotEdit(t, dir, edit)
	}
	return dir
}

// TestPublishTriggerMutations tests the actual directory checker, not a
// standalone trigger helper. Preparation adapts only the real pilot's older
// verifier and release boundary to today's pre-existing confinement policy;
// each row then mutates that checked consumer copy.
func TestPublishTriggerMutations(t *testing.T) {
	data, err := publishTriggerFixtures.ReadFile("testdata/publish_trigger_mutations.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fx publishTriggerCorpus
	if err := strictUnmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.Repository != "https://github.com/peasant-labs/fairtrade-design-system" || fx.Commit != "1da19970ce24a0ca5a993a8f3ee6a0a4879cbf4a" {
		t.Fatal("pilot provenance is missing or changed")
	}
	var names []string
	accepted := map[string]bool{}
	for _, tc := range fx.Cases {
		names = append(names, tc.Name)
		accepted[tc.Name] = len(tc.Rules) == 0
	}
	manifestNames(t, "publish_trigger_mutations.yaml", fx.RequiredNames, names)
	required := map[string]bool{}
	for _, name := range fx.RequiredNames {
		if required[name] {
			t.Fatalf("required case %q is listed twice", name)
		}
		required[name] = true
	}
	files := map[string]bool{}
	for _, source := range fx.Source {
		if files[source.File] {
			t.Fatalf("source file %q listed twice", source.File)
		}
		files[source.File] = true
		data, err := publishTriggerFixtures.ReadFile("testdata/fairtrade-pilot/" + source.File)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != source.SHA256 {
			t.Fatalf("source %s no longer matches its commit snapshot", source.File)
		}
	}
	if err := fs.WalkDir(publishTriggerFixtures, "testdata/fairtrade-pilot", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !files[strings.TrimPrefix(name, "testdata/fairtrade-pilot/")] {
			t.Fatalf("unmanifested pilot source %s", name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			if len(tc.Rules) > 0 && !accepted[tc.Sibling] {
				t.Fatalf("reject sibling %q is not an accepting case", tc.Sibling)
			}
			if len(tc.Rules) == 0 && (tc.Sibling != "" || len(tc.WantContains) != 0) {
				t.Fatal("accepting case has a sibling or diagnostic expectation")
			}
			dir := materializePilot(t, fx.Preparation)
			const npm = ".github/workflows/npm-publish.yml"
			if tc.Trigger != "" {
				applyPilotEdit(t, dir, pilotEdit{File: npm, Find: "on:\n  push:\n    tags: ['fairtrade-v*']\n", Replace: tc.Trigger})
			}
			if tc.DropNeed {
				applyPilotEdit(t, dir, pilotEdit{File: npm, Find: "  npm-publish:\n    needs: verify\n", Replace: "  npm-publish:\n"})
			}
			for _, edit := range tc.Edits {
				applyPilotEdit(t, dir, edit)
			}
			queue, err := LoadQueuePolicy(filepath.Join(dir, DefaultPath))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := intent.LoadConfig(filepath.Join(dir, intent.DefaultConfigPath))
			if err != nil {
				t.Fatal(err)
			}
			diags, _ := CheckWorkflows(queue, cfg, filepath.Join(dir, ".github/workflows"))
			fired := map[QueueRule]bool{}
			var messages []string
			for _, d := range diags {
				fired[d.Rule] = true
				messages = append(messages, d.Message)
			}
			joined := strings.Join(messages, "\n")
			for _, rule := range AllQueueRules {
				if fired[rule] != slices.Contains(tc.Rules, rule) {
					t.Fatalf("rule %s fired = %v; want exactly %v:\n%s", rule, fired[rule], tc.Rules, joined)
				}
			}
			for _, want := range tc.WantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("diagnostics lack %q:\n%s", want, joined)
				}
			}
		})
	}
}
