package main

import (
	"bytes"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/unstring/internal/astgrep/astgreptest"
	"gopkg.in/yaml.v3"
)

//go:embed testdata/typed/check.yaml
var typedCheckFixture []byte

//go:embed testdata/typed/mount.yaml
var typedMountFixture []byte

func TestTypedCheckIsOptInAndNotBaselined(t *testing.T) {
	var f struct {
		Module, Source, Config, Exemption string
		MarkedSource                      string `yaml:"markedSource"`
	}
	if err := yaml.Unmarshal(typedCheckFixture, &f); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", f.Module)
	write("src/example.go", f.Source)
	write(".unstring.yaml", f.Config)
	bin := unstringBinary(t)
	ag := astgreptest.Require(t)
	run := func(args ...string) (string, error) {
		t.Helper()
		c := exec.Command(bin, append(args, "--ast-grep", ag)...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOPROXY=off", "GOWORK=off")
		var output bytes.Buffer
		c.Stdout = &output
		c.Stderr = &output
		err := c.Run()
		return output.String(), err
	}
	typedConfig := f.Config + "typed:\n  goClosedSetLiteral: true\n"
	write(".unstring.yaml", typedConfig)
	if out, err := run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	baseline := filepath.Join(dir, ".unstring-baseline.yaml")
	before, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte("go-typed-closed-set-literal")) {
		t.Fatalf("init wrote a typed baseline entry: %s", before)
	}
	if out, err := run("update"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	updated, err := os.ReadFile(baseline)
	if err != nil || bytes.Contains(updated, []byte("go-typed-closed-set-literal")) {
		t.Fatalf("update wrote a typed entry: %s (%v)", updated, err)
	}
	before = updated
	write(".unstring.yaml", f.Config)
	if out, err := run("check"); err != nil || strings.Contains(out, "go-typed-closed-set-literal") {
		t.Fatalf("default check: %v\n%s", err, out)
	}
	write(".unstring.yaml", typedConfig)
	out, err := run("check")
	if err == nil || !strings.Contains(out, "go-typed-closed-set-literal") || !strings.Contains(out, "use User instead of 'user'") {
		t.Fatalf("typed check: %v\n%s", err, out)
	}
	after, err := os.ReadFile(baseline)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("check changed baseline")
	}
	write("src/example.go", f.MarkedSource)
	write(".unstring.yaml", typedConfig+f.Exemption)
	out, err = run("check")
	if err == nil || !strings.Contains(out, "go-typed-closed-set-literal  src/example.go:6  \"user\"") || !strings.Contains(out, "use User instead of 'user'") {
		t.Fatalf("marker and exemption suppressed typed finding: %v\n%s", err, out)
	}
	// A fabricated typed rule entry is rejected, never used to suppress the hit.
	write(".unstring-baseline.yaml", strings.Replace(string(before), "rules:\n", "rules:\n  go-typed-closed-set-literal:\n    src/example.go: 1\n", 1))
	out, err = run("check")
	if err == nil || !strings.Contains(out, "go-typed-closed-set-literal") {
		t.Fatalf("fabricated baseline: %v\n%s", err, out)
	}
}

// requiredTypedFixtureKeys are the keys each typed fixture must keep. The
// exclusion keys protect the tests that prove both typed passes skip an
// excluded file.
var requiredTypedFixtureKeys = map[string][]string{
	"check.yaml": {"module", "source", "config", "markedSource", "exemption", "vendored", "exclusion"},
	"mount.yaml": {"module", "source", "config", "typed", "want", "vendored", "vendoredWant", "exclusion"},
}

func TestRequiredTypedFixtureKeysExist(t *testing.T) {
	for name, data := range map[string][]byte{"check.yaml": typedCheckFixture, "mount.yaml": typedMountFixture} {
		var keys map[string]any
		if err := yaml.Unmarshal(data, &keys); err != nil {
			t.Fatalf("decode testdata/typed/%s: %v", name, err)
		}
		for _, want := range requiredTypedFixtureKeys[name] {
			if _, ok := keys[want]; !ok {
				t.Errorf("testdata/typed/%s is missing required key %q", name, want)
			}
		}
	}
}

// typedRepo writes files into a new Go module directory and returns a
// function that runs the built command there, offline, with stdout and
// stderr combined.
func typedRepo(t *testing.T, files map[string]string) (write func(name, content string), run func(args ...string) (string, error)) {
	t.Helper()
	dir := t.TempDir()
	write = func(name, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		write(name, content)
	}
	bin := unstringBinary(t)
	ag := astgreptest.Require(t)
	run = func(args ...string) (string, error) {
		t.Helper()
		c := exec.Command(bin, append(args, "--ast-grep", ag)...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOPROXY=off", "GOWORK=off")
		var output bytes.Buffer
		c.Stdout = &output
		c.Stderr = &output
		err := c.Run()
		return output.String(), err
	}
	return write, run
}

// An excluded file is skipped by the typed closed-set pass, and a file next
// to it is still reported.
func TestTypedCheckSkipsExcludedFiles(t *testing.T) {
	var f struct {
		Module, Source, Config, Vendored, Exclusion string
	}
	if err := yaml.Unmarshal(typedCheckFixture, &f); err != nil {
		t.Fatal(err)
	}
	const vendored = "src/third_party/sqlite/blob.go"
	typedConfig := f.Config + "typed:\n  goClosedSetLiteral: true\n"
	write, run := typedRepo(t, map[string]string{
		"go.mod": f.Module, "src/example.go": f.Source, vendored: f.Vendored, ".unstring.yaml": typedConfig,
	})
	if out, err := run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out, err := run("check")
	if err == nil || !strings.Contains(out, vendored) || !strings.Contains(out, "src/example.go") {
		t.Fatalf("without the exclusion, check must report both files: %v\n%s", err, out)
	}
	write(".unstring.yaml", typedConfig+f.Exclusion)
	out, err = run("check")
	if err == nil || !strings.Contains(out, "src/example.go") {
		t.Fatalf("the exclusion hid the file next to it: %v\n%s", err, out)
	}
	if strings.Contains(out, vendored) {
		t.Fatalf("the typed pass reported an excluded file:\n%s", out)
	}
}

// An excluded file is skipped by the typed mount pass, and a mount next to
// it is still reported.
func TestTypedMountCheckSkipsExcludedFiles(t *testing.T) {
	var f struct {
		Module, Source, Config, Typed, Vendored, Exclusion string
		Want                                               []string
		VendoredWant                                       string `yaml:"vendoredWant"`
	}
	if err := yaml.Unmarshal(typedMountFixture, &f); err != nil {
		t.Fatal(err)
	}
	write, run := typedRepo(t, map[string]string{
		"go.mod": f.Module, "src/router.go": f.Source, "src/third_party/upstream/router.go": f.Vendored,
		".unstring.yaml": f.Config + f.Typed,
	})
	if out, err := run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out, err := run("check")
	if err == nil || !strings.Contains(out, f.VendoredWant) || !strings.Contains(out, f.Want[0]) {
		t.Fatalf("without the exclusion, check must report both mounts: %v\n%s", err, out)
	}
	write(".unstring.yaml", f.Config+f.Exclusion+f.Typed)
	out, err = run("check")
	if err == nil || !strings.Contains(out, f.Want[0]) {
		t.Fatalf("the exclusion hid the mount next to it: %v\n%s", err, out)
	}
	if strings.Contains(out, "src/third_party/") {
		t.Fatalf("the typed mount pass reported an excluded file:\n%s", out)
	}
}

func TestTypedMountCheckIsOptInAndNotBaselined(t *testing.T) {
	var f struct {
		Module, Source, TestSource, Config, Typed string
		Want                                      []string
		TestWant                                  string   `yaml:"testWant"`
		MissingRouter                             string   `yaml:"missingRouter"`
		MissingRouterWant                         []string `yaml:"missingRouterWant"`
	}
	if err := yaml.Unmarshal(typedMountFixture, &f); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", f.Module)
	write("src/router.go", f.Source)
	write("src/router_test.go", f.TestSource)
	bin := unstringBinary(t)
	ag := astgreptest.Require(t)
	run := func(args ...string) (string, error) {
		t.Helper()
		c := exec.Command(bin, append(args, "--ast-grep", ag)...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOPROXY=off", "GOWORK=off")
		var output bytes.Buffer
		c.Stdout = &output
		c.Stderr = &output
		err := c.Run()
		return output.String(), err
	}
	const rule = "go-typed-mount-literal"
	write(".unstring.yaml", f.Config+f.Typed)
	if out, err := run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if out, err := run("update"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	baseline := filepath.Join(dir, ".unstring-baseline.yaml")
	before, err := os.ReadFile(baseline)
	if err != nil || bytes.Contains(before, []byte(rule)) {
		t.Fatalf("init or update wrote a typed entry: %s (%v)", before, err)
	}
	write(".unstring.yaml", f.Config)
	if out, err := run("check"); err != nil || strings.Contains(out, rule) {
		t.Fatalf("default check: %v\n%s", err, out)
	}
	write(".unstring.yaml", f.Config+f.Typed)
	out, err := run("check")
	if err == nil {
		t.Fatalf("typed mount check passed:\n%s", out)
	}
	for _, want := range f.Want {
		if !strings.Contains(out, want) {
			t.Errorf("typed mount check output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, f.TestWant) {
		t.Fatalf("default scan included test mount:\n%s", out)
	}
	write(".unstring.yaml", f.Config+f.Typed+"    includeTests: true\n")
	out, err = run("check")
	if err == nil || !strings.Contains(out, f.TestWant) {
		t.Fatalf("included test mount missing: %v\n%s", err, out)
	}
	write(".unstring.yaml", f.Config+f.Typed)
	if after, err := os.ReadFile(baseline); err != nil || !bytes.Equal(before, after) {
		t.Fatal("check changed the baseline")
	}
	// A fabricated typed rule entry is rejected, never used to suppress the hit.
	write(".unstring-baseline.yaml", strings.Replace(string(before), "rules:\n", "rules:\n  "+rule+":\n    src/router.go: 1\n", 1))
	if out, err := run("check"); err == nil || !strings.Contains(out, rule) {
		t.Fatalf("fabricated baseline: %v\n%s", err, out)
	}
	write(".unstring-baseline.yaml", string(before))
	write(".unstring.yaml", f.Config+f.MissingRouter)
	out, err = run("check")
	if err == nil {
		t.Fatalf("check accepted a router no root imports:\n%s", out)
	}
	for _, want := range f.MissingRouterWant {
		if !strings.Contains(out, want) {
			t.Errorf("missing-router output lacks %q:\n%s", want, out)
		}
	}
}
