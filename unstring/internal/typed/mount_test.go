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

//go:embed testdata/mount-literal-go.yaml
var mountCorpus []byte

type mountExpected struct {
	File     string `yaml:"file"`
	Line     int    `yaml:"line"`
	Receiver string `yaml:"receiver"`
	Method   string `yaml:"method"`
	Pattern  string `yaml:"pattern"`
	Text     string `yaml:"text"`
}

type mountFixture struct {
	Name    string            `yaml:"name"`
	Routers []string          `yaml:"routers"`
	Tests   string            `yaml:"tests"`
	Roots   []string          `yaml:"roots"`
	Files   map[string]string `yaml:"files"`
	Error   string            `yaml:"error"`
	Expect  []mountExpected   `yaml:"expect"`
}

// The cases every change to the mount pass must keep.
var requiredMountFixtures = []string{
	"mount on chi mux",
	"router interface in route callback",
	"mount after with",
	"route and mount group literals",
	"method pattern argument",
	"mount outside router file",
	"string helpers",
	"test helper in test file excluded by default",
	"test file included on opt-in",
	"wrapper embedding a router",
	"wrapper forwarding a parameter",
	"non-router receiver with mount method names",
	"serve mux not configured",
	"serve mux configured",
	"non-literal patterns",
	"literal concatenation",
	"production directive names test file",
	"test directive names production file",
	"overlapping roots",
	"router package not imported",
}

func loadMountFixtures(t *testing.T) []mountFixture {
	t.Helper()
	var cases []mountFixture
	dec := yaml.NewDecoder(strings.NewReader(string(mountCorpus)))
	dec.KnownFields(true)
	if err := dec.Decode(&cases); err != nil {
		t.Fatalf("decode testdata/mount-literal-go.yaml: %v", err)
	}
	return cases
}

func TestMountFixtures(t *testing.T) {
	stub, err := filepath.Abs(filepath.Join("testdata", "chi"))
	if err != nil {
		t.Fatal(err)
	}
	goMod := "module fixture\n\ngo 1.26\n\nrequire github.com/go-chi/chi/v5 v5.0.0\n\nreplace github.com/go-chi/chi/v5 => " + filepath.ToSlash(stub) + "\n"
	cases := loadMountFixtures(t)
	found := map[string]bool{}
	for _, tc := range cases {
		if found[tc.Name] {
			t.Errorf("two fixtures named %q", tc.Name)
		}
		found[tc.Name] = true
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"go.mod": goMod}
			for name, content := range tc.Files {
				files[name] = content
			}
			for name, content := range files {
				filename := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			roots := tc.Roots
			if len(roots) == 0 {
				roots = []string{"."}
			}
			routers := tc.Routers
			if len(routers) == 0 {
				routers = typed.DefaultMountRouters()
			}
			env := append(os.Environ(), "GOPROXY=off", "GOWORK=off")
			prog, err := typed.LoadGo(context.Background(), dir, roots, env)
			if err != nil {
				t.Fatal(err)
			}
			got, err := prog.MountLiterals(typed.MountOptions{Routers: routers, IncludeTests: tc.Tests == "include"})
			if tc.Error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.Error) {
					t.Fatalf("error = %v; want one containing %q", err, tc.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.Expect) {
				t.Fatalf("findings = %+v; want %+v", got, tc.Expect)
			}
			for i, want := range tc.Expect {
				h := got[i]
				if h.Hit.Rule != typed.GoMountLiteral || h.Hit.File != want.File || h.Hit.Line != want.Line || h.Receiver != want.Receiver || h.Method != want.Method || h.Pattern != want.Pattern || (want.Text != "" && h.Hit.Text != want.Text) {
					t.Errorf("finding %d = %+v; want %+v", i, h, want)
				}
				if !strings.Contains(h.Message(), h.Receiver) || !strings.Contains(h.Message(), h.Method) || !strings.Contains(h.Message(), h.Hit.Text) {
					t.Errorf("message %q does not name the receiver, method and literal", h.Message())
				}
			}
		})
	}
	for _, name := range requiredMountFixtures {
		if !found[name] {
			t.Errorf("testdata/mount-literal-go.yaml is missing required fixture %q", name)
		}
	}
}
