package rules_test

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
	"github.com/peasant-labs/infra/stringly/internal/config"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// The rule fixtures run every static rule through the real ast-grep binary:
// a rule's matching exists only inside ast-grep, so canned output could not
// show that a rule matches what it should.
//
// The binary is STRINGLY_AST_GREP when set, else `ast-grep` on PATH. It must
// report exactly astgrep.RequiredVersion. A missing or different binary fails
// the tests with the install command; it never skips them. CI installs it with
// `npm install --global @ast-grep/cli@0.51.2` before `go test ./...`.
const astGrepEnv = "STRINGLY_AST_GREP"

//go:embed testdata/*.yaml
var testdataFS embed.FS

// ruleFixture is one testdata/<rule-id>.yaml file.
type ruleFixture struct {
	Rule string `yaml:"rule"`
	// Params apply to every case unless the case sets its own.
	Params fixtureParams `yaml:"params"`
	Cases  []fixtureCase `yaml:"cases"`
}

type fixtureCase struct {
	// Name is `flags-…` when the case expects at least one hit and
	// `passes-…` when it expects none.
	Name   string            `yaml:"name"`
	Params fixtureParams     `yaml:"params"`
	Files  map[string]string `yaml:"files"`
	// Hits are the exact 1-based hit lines per file. A file not listed must
	// have no hit.
	Hits map[string][]int `yaml:"hits"`
}

// fixtureParams overrides the production defaults field by field; a nil
// field keeps the value from the enclosing level.
type fixtureParams struct {
	EndpointPrefixes []string `yaml:"endpointPrefixes"`
	EndpointCallees  []string `yaml:"endpointCallees"`
	MountFiles       []string `yaml:"mountFiles"`
	DataDirs         []string `yaml:"dataDirs"`
}

func (o fixtureParams) over(p rules.Params) rules.Params {
	if o.EndpointPrefixes != nil {
		p.EndpointPrefixes = o.EndpointPrefixes
	}
	if o.EndpointCallees != nil {
		p.EndpointCallees = o.EndpointCallees
	}
	if o.MountFiles != nil {
		p.MountFiles = o.MountFiles
	}
	if o.DataDirs != nil {
		p.DataDirs = o.DataDirs
	}
	return p
}

// defaultParams are the values a repository gets when its config sets none.
func defaultParams() rules.Params {
	return rules.Params{
		EndpointPrefixes: config.DefaultEndpointPrefixes(),
		DataDirs:         config.DefaultDataDirs(),
	}
}

// manifest is testdata/manifest.yaml: the case names each static rule's
// fixture must carry.
type manifest struct {
	Required map[string][]string `yaml:"required"`
}

func decodeStrict(t *testing.T, name string, v any) {
	t.Helper()
	b, err := testdataFS.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode testdata/%s: %v", name, err)
	}
}

// loadRuleFixtures returns the fixture of every static rule, keyed by id. A
// fixture file whose name is a generated rule's id belongs to the catalog's
// tests and is not loaded here.
func loadRuleFixtures(t *testing.T) map[rules.RuleID]ruleFixture {
	t.Helper()
	entries, err := testdataFS.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	out := map[rules.RuleID]ruleFixture{}
	for _, e := range entries {
		id, err := rules.ParseRuleID(strings.TrimSuffix(e.Name(), ".yaml"))
		if err != nil || id.Origin() != rules.OriginStatic {
			continue
		}
		var f ruleFixture
		decodeStrict(t, e.Name(), &f)
		if f.Rule != id.String() {
			t.Errorf("testdata/%s declares rule %q; the file name and the rule must agree", e.Name(), f.Rule)
		}
		out[id] = f
	}
	return out
}

func TestEveryStaticRuleHasAFixtureAndTheManifestNames(t *testing.T) {
	fixtures := loadRuleFixtures(t)
	var m manifest
	decodeStrict(t, "manifest.yaml", &m)

	for _, id := range rules.AllRuleIDs {
		_, hasFixture := fixtures[id]
		_, inManifest := m.Required[id.String()]
		static := id.Origin() == rules.OriginStatic
		if static && !hasFixture {
			t.Errorf("static rule %s has no testdata/%s.yaml", id, id)
		}
		if static != inManifest {
			t.Errorf("testdata/manifest.yaml: rule %s listed = %v, but its origin is %s; the manifest lists exactly the static rules", id, inManifest, id.Origin())
		}
	}
	for key := range m.Required {
		if _, err := rules.ParseRuleID(key); err != nil {
			t.Errorf("testdata/manifest.yaml lists %q: %v", key, err)
		}
	}

	for id, f := range fixtures {
		names := map[string]bool{}
		for _, c := range f.Cases {
			if names[c.Name] {
				t.Errorf("%s: case %q appears twice", id, c.Name)
			}
			names[c.Name] = true
			switch {
			case strings.HasPrefix(c.Name, "flags-"):
				if len(c.Hits) == 0 {
					t.Errorf("%s/%s: a flags- case must expect at least one hit", id, c.Name)
				}
			case strings.HasPrefix(c.Name, "passes-"):
				if len(c.Hits) != 0 {
					t.Errorf("%s/%s: a passes- case must expect no hit", id, c.Name)
				}
			default:
				t.Errorf("%s/%s: a case name starts with flags- or passes-", id, c.Name)
			}
			for file := range c.Hits {
				if _, ok := c.Files[file]; !ok {
					t.Errorf("%s/%s: expects hits in %s, which the case does not write", id, c.Name, file)
				}
			}
		}
		for _, want := range m.Required[id.String()] {
			if !names[want] {
				t.Errorf("%s: required case %q (testdata/manifest.yaml) is missing from testdata/%s.yaml", id, want, id)
			}
		}
	}
}

func TestStaticRuleFixtures(t *testing.T) {
	bin := requireAstGrep(t)
	for id, f := range loadRuleFixtures(t) {
		fileParams := f.Params.over(defaultParams())
		for _, c := range f.Cases {
			t.Run(id.String()+"/"+c.Name, func(t *testing.T) {
				t.Parallel()
				reg, err := rules.NewRegistry(c.Params.over(fileParams))
				if err != nil {
					t.Fatalf("NewRegistry: %v", err)
				}
				rule, ok := reg.Rule(id)
				if !ok {
					t.Fatalf("the registry has no rule %s", id)
				}
				got := scanCase(t, bin, rule, c.Files)
				want := map[string][]int{}
				for file, lines := range c.Hits {
					want[file] = sortedUnique(lines)
				}
				if !equalHits(got, want) {
					t.Errorf("hits differ\n got: %s\nwant: %s\nrendered rule:\n%s", formatHits(got), formatHits(want), rule.Document)
				}
			})
		}
	}
}

// scanCase writes the case tree and the one rule to a temporary directory,
// runs `ast-grep scan --json=stream` from the tree's root the way the tool
// does, and returns the distinct hit lines per file.
func scanCase(t *testing.T, bin string, rule rules.Rule, files map[string]string) map[string][]int {
	t.Helper()
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	cfg := filepath.Join(root, "config")
	for name, body := range files {
		p := filepath.Join(tree, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(cfg, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "rules", rule.ID.String()+".yml"), rule.Document, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "sgconfig.yml"), sgconfig(), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "scan", "--config", filepath.Join(cfg, "sgconfig.yml"), "--json=stream", ".")
	cmd.Dir = tree
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ast-grep scan failed: %v\nstderr:\n%s\nrendered rule:\n%s", err, stderr.String(), rule.Document)
	}

	got := map[string][]int{}
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		var rec struct {
			RuleID string `json:"ruleId"`
			File   string `json:"file"`
			Range  struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("ast-grep printed a line that is not a JSON record: %v\n%s", err, sc.Text())
		}
		id, err := rules.ParseDocumentID(rec.RuleID)
		if err != nil || id != rule.ID {
			t.Fatalf("ast-grep reported a record of %q while only %s was loaded: %v", rec.RuleID, rule.ID, err)
		}
		file := filepath.ToSlash(rec.File)
		got[file] = append(got[file], rec.Range.Start.Line+1)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for f, lines := range got {
		got[f] = sortedUnique(lines)
	}
	return got
}

// sgconfig is the tool's scan config: the rule directory and the default
// languageGlobs, which parse every TS and JS extension with ast-grep's TSX
// parser.
func sgconfig() []byte {
	b, err := yaml.Marshal(map[string]any{"ruleDirs": []string{"rules"}, "languageGlobs": astgrep.DefaultLanguageGlobs()})
	if err != nil {
		panic(err)
	}
	return b
}

var (
	astGrepOnce sync.Once
	astGrepPath string
	astGrepErr  error
)

// requireAstGrep returns the pinned ast-grep binary or fails the test with
// the install command.
func requireAstGrep(t *testing.T) string {
	t.Helper()
	astGrepOnce.Do(func() {
		bin := os.Getenv(astGrepEnv)
		if bin == "" {
			p, err := exec.LookPath("ast-grep")
			if err != nil {
				astGrepErr = fmt.Errorf("ast-grep is not on PATH and %s is not set", astGrepEnv)
				return
			}
			bin = p
		}
		out, err := exec.Command(bin, "--version").Output()
		if err != nil {
			astGrepErr = fmt.Errorf("running %s --version failed: %v", bin, err)
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "ast-grep"))
		if got != astgrep.RequiredVersion {
			astGrepErr = fmt.Errorf("%s reports ast-grep %s", bin, got)
			return
		}
		astGrepPath = bin
	})
	if astGrepErr != nil {
		t.Fatalf("the rule fixtures need ast-grep %s: %v. Install it with "+
			"`npm install --global @ast-grep/cli@%s` (or `npm install --prefix <dir> @ast-grep/cli@%s`) "+
			"and put it on PATH or set %s to the binary; then re-run go test",
			astgrep.RequiredVersion, astGrepErr, astgrep.RequiredVersion, astgrep.RequiredVersion, astGrepEnv)
	}
	return astGrepPath
}

func sortedUnique(lines []int) []int {
	out := slices.Clone(lines)
	slices.Sort(out)
	return slices.Compact(out)
}

func equalHits(a, b map[string][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func formatHits(h map[string][]int) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %v", k, h[k])
	}
	return "{" + strings.Join(parts, "; ") + "}"
}
