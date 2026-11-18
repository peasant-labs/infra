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
