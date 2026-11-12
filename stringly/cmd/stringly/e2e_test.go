package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/stringly/internal/astgrep/astgreptest"
)

// e2eBuildTags are the build tags of the binary under test. It is empty: the
// test builds the release binary, with the rules and catalog wired in
// wiring.go.
const e2eBuildTags = ""

// e2eVersion is the version the binary under test is linked with.
const e2eVersion = "v0.1.0"

// schemaModule is the module path and version of testdata/e2e/schema-module.
const (
	schemaModule  = "example.test/schema"
	schemaVersion = "v0.0.1"
)

//go:embed testdata/e2e/mutations.yaml
var mutationsYAML []byte

type replaceStep struct {
	File string `yaml:"file"`
	Old  string `yaml:"old"`
	New  string `yaml:"new"`
}

type baselineWant struct {
	Unchanged    bool     `yaml:"unchanged"`
	ChangedLines []string `yaml:"changedLines"`
	Contains     []string `yaml:"contains"`
	Lacks        []string `yaml:"lacks"`
}

type mutationStep struct {
	Replace     *replaceStep  `yaml:"replace"`
	Delete      string        `yaml:"delete"`
	Git         []string      `yaml:"git"`
	StubAstGrep string        `yaml:"stubAstGrep"`
	EmptyProxy  bool          `yaml:"emptyProxy"`
	Run         []string      `yaml:"run"`
	Exit        int           `yaml:"exit"`
	Kinds       []string      `yaml:"kinds"`
	Stdout      []string      `yaml:"stdout"`
	Stderr      []string      `yaml:"stderr"`
	Baseline    *baselineWant `yaml:"baseline"`
}

type mutation struct {
	Name  string         `yaml:"name"`
	Steps []mutationStep `yaml:"steps"`
}

type mutationsFixture struct {
	SetupEntries []string   `yaml:"setupEntries"`
	Mutations    []mutation `yaml:"mutations"`
}

// The mutations every change to the command must keep (design section 12.4).
var requiredMutations = []string{
	"clean-tree",
	"new-hit-listed-file-ts",
	"new-hit-listed-file-go",
	"new-hit-unlisted-file-ts",
	"new-hit-unlisted-file-go",
	"hit-removed-then-update",
	"update-refuses-raise",
	"rename-moves-entry",
	"catalog-growth-closed-member",
	"catalog-growth-union-member",
	"catalog-growth-go-constant",
	"baseline-from-other-tool-version",
	"tool-upgrade-with-new-hit",
	"tool-upgrade-new-hit-marked",
	"init-first-adoption",
	"init-refused-when-baseline-exists",
	"marker-on-closed-set-rule",
	"marker-on-go-closed-set-rule",
	"marker-accepted-on-other-rule",
	"wrong-ast-grep",
	"schema-module-missing",
}

func loadMutations(t *testing.T) mutationsFixture {
	t.Helper()
	var f mutationsFixture
	dec := yaml.NewDecoder(bytes.NewReader(mutationsYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/e2e/mutations.yaml: %v", err)
	}
	return f
}

func TestRequiredMutationsExist(t *testing.T) {
	var names []string
	for _, m := range loadMutations(t).Mutations {
		if slices.Contains(names, m.Name) {
			t.Errorf("testdata/e2e/mutations.yaml has two mutations named %q", m.Name)
		}
		names = append(names, m.Name)
	}
	for _, want := range requiredMutations {
		if !slices.Contains(names, want) {
			t.Errorf("testdata/e2e/mutations.yaml is missing required mutation %q", want)
		}
	}
}

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// stringlyBinary builds the command once per test process, linked with
// e2eVersion, into a directory removed when the process exits.
func stringlyBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "stringly-e2e-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "stringly")
		args := []string{"build", "-ldflags", "-X main.version=" + e2eVersion, "-o", binPath}
		if e2eBuildTags != "" {
			args = append(args, "-tags", e2eBuildTags)
		}
		out, err := exec.Command("go", append(args, ".")...).CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if binPath != "" {
		os.RemoveAll(filepath.Dir(binPath))
	}
	os.Exit(code)
}

// requireTool fails the test when a tool the harness runs is missing.
func requireTool(t *testing.T, name, install string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("the end-to-end test runs %s and it is not on PATH: %s", name, install)
	}
	return p
}

// writeModuleProxy writes a file:// module proxy that serves the schema
// fixture module: @v/list, .info, .mod and .zip, built from plain files.
func writeModuleProxy(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join("testdata", "e2e", "schema-module")
	at := filepath.Join(root, schemaModule, "@v")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		w, err := zw.Create(schemaModule + "@" + schemaVersion + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"list":                  []byte(schemaVersion + "\n"),
		schemaVersion + ".info": []byte(`{"Version":"` + schemaVersion + `","Time":"2026-11-12T00:00:00Z"}`),
		schemaVersion + ".mod":  mod,
		schemaVersion + ".zip":  zbuf.Bytes(),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(at, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// e2eRepo is one mutation's repository and the environment its runs get.
type e2eRepo struct {
	dir      string
	tmp      string
	path     []string // PATH entries, first wins
	proxy    string
	modcache string
	bin      string
}

func (r *e2eRepo) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "TMPDIR", "GOPROXY", "GOSUMDB", "GOFLAGS", "GOWORK", "GOMODCACHE", "GONOSUMDB", "GOPRIVATE", "GONOPROXY", "STRINGLY_DEV":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PATH="+strings.Join(r.path, string(os.PathListSeparator)),
		"TMPDIR="+r.tmp,
		"GOPROXY=file://"+filepath.ToSlash(r.proxy),
		"GOSUMDB=off",
		"GOFLAGS=-modcacherw",
		"GOWORK=off",
		"GOMODCACHE="+r.modcache,
	)
}

func (r *e2eRepo) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=stringly e2e", "-c", "user.email=e2e@example.test", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (r *e2eRepo) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run stringly %s: %v", strings.Join(args, " "), err)
		}
		code = ee.ExitCode()
	}
	// The tool removes its temporary rule directory on every exit.
	if left, _ := os.ReadDir(r.tmp); len(left) > 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("stringly %s left %v in its temporary directory", strings.Join(args, " "), names)
	}
	return code, stdout.String(), stderr.String()
}

func (r *e2eRepo) baseline(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, ".stringly-baseline.yaml"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return data
}

func newE2ERepo(t *testing.T, astGrep string) *e2eRepo {
	t.Helper()
	root := t.TempDir()
	r := &e2eRepo{
		dir:      filepath.Join(root, "repo"),
		tmp:      filepath.Join(root, "tmp"),
		proxy:    filepath.Join(root, "proxy"),
		modcache: filepath.Join(root, "modcache"),
		bin:      stringlyBinary(t),
	}
	for _, d := range []string{r.tmp, r.proxy} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	goBin := requireTool(t, "go", "install Go")
	gitBin := requireTool(t, "git", "install git")
	r.path = []string{filepath.Dir(astGrep), filepath.Dir(goBin), filepath.Dir(gitBin)}
	r.path = append(r.path, filepath.SplitList(os.Getenv("PATH"))...)
	writeModuleProxy(t, r.proxy)
	copyTree(t, filepath.Join("testdata", "e2e", "tree"), r.dir)
	r.git(t, "init", "-q", "-b", "main")
	r.git(t, "add", "-A")
	r.git(t, "commit", "-q", "-m", "tree")
	return r
}

var kindLine = regexp.MustCompile(`(?m)^\S.*  \[([a-z-]+)\]$`)

func reportedKinds(stdout string) []string {
	var kinds []string
	for _, m := range kindLine.FindAllStringSubmatch(stdout, -1) {
		if !slices.Contains(kinds, m[1]) {
			kinds = append(kinds, m[1])
		}
	}
	slices.Sort(kinds)
	return kinds
}

func lines(b []byte) []string { return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") }

func TestEndToEnd(t *testing.T) {
	astGrep := astgreptest.Require(t)
	f := loadMutations(t)
	for _, m := range f.Mutations {
		t.Run(m.Name, func(t *testing.T) {
			t.Parallel()
			r := newE2ERepo(t, astGrep)
			if code, out, errOut := r.run(t, "init"); code != 0 {
				t.Fatalf("setup: init exited %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
			}
			initial := r.baseline(t)
			for _, want := range f.SetupEntries {
				if !slices.Contains(lines(initial), want) {
					t.Fatalf("setup: the first baseline lacks the line %q:\n%s", want, initial)
				}
			}
			r.git(t, "add", ".stringly-baseline.yaml")
			r.git(t, "commit", "-q", "-m", "baseline")

			for i, s := range m.Steps {
				where := fmt.Sprintf("step %d", i+1)
				switch {
				case s.Replace != nil:
					p := filepath.Join(r.dir, filepath.FromSlash(s.Replace.File))
					data, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					if n := strings.Count(string(data), s.Replace.Old); n != 1 {
						t.Fatalf("%s: %q occurs %d times in %s, want once", where, s.Replace.Old, n, s.Replace.File)
					}
					if err := os.WriteFile(p, []byte(strings.Replace(string(data), s.Replace.Old, s.Replace.New, 1)), 0o644); err != nil {
						t.Fatal(err)
					}
				case s.Delete != "":
					if err := os.Remove(filepath.Join(r.dir, filepath.FromSlash(s.Delete))); err != nil {
						t.Fatal(err)
					}
				case s.Git != nil:
					r.git(t, s.Git...)
				case s.StubAstGrep != "":
					stubDir := filepath.Join(filepath.Dir(r.dir), "stub-bin")
					if err := os.MkdirAll(stubDir, 0o755); err != nil {
						t.Fatal(err)
					}
					script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'ast-grep " + s.StubAstGrep + "'; exit 0; fi\necho 'the stub was asked to scan' >&2\nexit 99\n"
					if err := os.WriteFile(filepath.Join(stubDir, "ast-grep"), []byte(script), 0o755); err != nil {
						t.Fatal(err)
					}
					r.path = append([]string{stubDir}, r.path...)
				case s.EmptyProxy:
					r.proxy = filepath.Join(filepath.Dir(r.dir), "empty-proxy")
					r.modcache = filepath.Join(filepath.Dir(r.dir), "empty-modcache")
					if err := os.MkdirAll(r.proxy, 0o755); err != nil {
						t.Fatal(err)
					}
				case s.Run != nil:
					before := r.baseline(t)
					code, stdout, stderr := r.run(t, s.Run...)
					after := r.baseline(t)
					ctx := fmt.Sprintf("%s: stringly %s\nstdout:\n%s\nstderr:\n%s", where, strings.Join(s.Run, " "), stdout, stderr)
					if code != s.Exit {
						t.Fatalf("exit %d, want %d\n%s", code, s.Exit, ctx)
					}
					if s.Kinds != nil {
						want := slices.Sorted(slices.Values(s.Kinds))
						if got := reportedKinds(stdout); !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
							t.Errorf("finding kinds %q, want %q\n%s", got, want, ctx)
						}
					}
					for _, want := range s.Stdout {
						if !strings.Contains(stdout, want) {
							t.Errorf("stdout lacks %q\n%s", want, ctx)
						}
					}
					for _, want := range s.Stderr {
						if !strings.Contains(stderr, want) {
							t.Errorf("stderr lacks %q\n%s", want, ctx)
						}
					}
					if strings.Contains(stderr, "the stub was asked to scan") {
						t.Errorf("the tool scanned with a wrong ast-grep\n%s", ctx)
					}
					if b := s.Baseline; b != nil {
						checkBaseline(t, b, before, after, ctx)
					}
				default:
					t.Fatalf("%s has no action", where)
				}
			}
		})
	}
}

func checkBaseline(t *testing.T, b *baselineWant, before, after []byte, ctx string) {
	t.Helper()
	if b.Unchanged && !bytes.Equal(before, after) {
		t.Errorf("the baseline changed:\n%s\nwas:\n%s\n%s", after, before, ctx)
	}
	if b.ChangedLines != nil {
		old, cur := lines(before), lines(after)
		if len(old) != len(cur) {
			t.Errorf("the baseline has %d lines, was %d:\n%s\n%s", len(cur), len(old), after, ctx)
		} else {
			var changed []string
			for i := range cur {
				if cur[i] != old[i] {
					changed = append(changed, cur[i])
				}
			}
			if !slices.Equal(changed, b.ChangedLines) {
				t.Errorf("changed baseline lines %q, want %q:\n%s\n%s", changed, b.ChangedLines, after, ctx)
			}
		}
	}
	for _, want := range b.Contains {
		if !slices.Contains(lines(after), want) {
			t.Errorf("the baseline lacks the line %q:\n%s\n%s", want, after, ctx)
		}
	}
	for _, bad := range b.Lacks {
		if slices.Contains(lines(after), bad) {
			t.Errorf("the baseline has the line %q:\n%s\n%s", bad, after, ctx)
		}
	}
}
