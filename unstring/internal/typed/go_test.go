package typed_test

import (
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/unstring/internal/typed"
	"gopkg.in/yaml.v3"
)

//go:embed testdata/closed-set-literal-go.yaml
var corpus []byte

type expected struct {
	Set     string `yaml:"set"`
	Member  string `yaml:"member"`
	Literal string `yaml:"literal"`
	Line    int    `yaml:"line"`
	File    string `yaml:"file"`
	Message string `yaml:"message"`
}
type fixture struct {
	Name   string            `yaml:"name"`
	Code   string            `yaml:"code"`
	Files  map[string]string `yaml:"files"`
	Roots  []string          `yaml:"roots"`
	Error  string            `yaml:"error"`
	Expect []expected        `yaml:"expect"`
}

func LoadGoFixtures(t *testing.T) []fixture {
	t.Helper()
	var cases []fixture
	if err := yaml.Unmarshal(corpus, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}
func TestGoFixtures(t *testing.T) {
	cases := LoadGoFixtures(t)
	required := map[string]bool{"direct comparison": false, "reversed comparison": false, "switch case": false, "untyped literal": false, "string receiver": false, "interface receiver": false, "named type without constants": false, "ambiguous value": false, "non-string named type": false, "production test-only constant": false, "test file constant": false, "external test package": false, "test constant ambiguity": false, "import alias": false, "dot import": false, "foreign private constant": false, "shadowed constant": false, "multiple hits": false, "parenthesized literal": false, "overlapping roots": false, "loader error": false}
	for _, tc := range cases {
		if _, ok := required[tc.Name]; ok {
			required[tc.Name] = true
		}
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.26\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.Code != "" {
				tc.Files = map[string]string{"fixture.go": tc.Code}
			}
			for name, content := range tc.Files {
				filename := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			env := append(os.Environ(), "GOPROXY=off", "GOWORK=off")
			roots := tc.Roots
			if len(roots) == 0 {
				roots = []string{"."}
			}
			got, err := typed.ScanGo(context.Background(), dir, roots, env)
			if tc.Error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.Error) || !strings.Contains(err.Error(), "fix the package errors") {
					t.Fatalf("loader error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.Expect) {
				t.Fatalf("findings = %#v; want %#v", got, tc.Expect)
			}
			for i, want := range tc.Expect {
				h := got[i]
				if h.Set != want.Set || h.Member != want.Member || h.Literal != want.Literal || h.Hit.Line != want.Line || (want.File != "" && h.Hit.File != want.File) || (want.Message != "" && h.Message() != want.Message) {
					t.Fatalf("finding = %#v, message %q; want %#v", h, h.Message(), want)
				}
			}
		})
	}
	for name, found := range required {
		if !found {
			t.Errorf("missing fixture %q", name)
		}
	}
}
