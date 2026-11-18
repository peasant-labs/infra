package typed_test

import (
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"reflect"
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
}
type fixture struct {
	Name   string     `yaml:"name"`
	Code   string     `yaml:"code"`
	Expect []expected `yaml:"expect"`
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
	required := map[string]bool{"direct comparison": false, "reversed comparison": false, "switch case": false, "untyped literal": false, "string receiver": false, "interface receiver": false, "named type without constants": false, "ambiguous value": false, "non-string named type": false}
	for _, tc := range cases {
		if _, ok := required[tc.Name]; ok {
			required[tc.Name] = true
		}
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.26\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(tc.Code), 0600); err != nil {
				t.Fatal(err)
			}
			env := append(os.Environ(), "GOPROXY=off", "GOWORK=off")
			got, err := typed.ScanGo(context.Background(), dir, []string{"."}, env)
			if err != nil {
				t.Fatal(err)
			}
			actual := []expected{}
			for _, h := range got {
				actual = append(actual, expected{h.Set, h.Member, h.Literal, h.Hit.Line})
			}
			if !reflect.DeepEqual(actual, tc.Expect) {
				t.Fatalf("findings = %#v; want %#v", actual, tc.Expect)
			}
		})
	}
	for name, found := range required {
		if !found {
			t.Errorf("missing fixture %q", name)
		}
	}
}
