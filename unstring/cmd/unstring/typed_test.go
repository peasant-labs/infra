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
	var f struct{ Module, Source, Config string }
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
	if out, err := run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	baseline := filepath.Join(dir, ".unstring-baseline.yaml")
	before, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run("check"); err != nil || strings.Contains(out, "go-typed-closed-set-literal") {
		t.Fatalf("default check: %v\n%s", err, out)
	}
	write(".unstring.yaml", f.Config+"typed:\n  goClosedSetLiteral: true\n")
	out, err := run("check")
	if err == nil || !strings.Contains(out, "go-typed-closed-set-literal") || !strings.Contains(out, "use Role.User instead of 'user'") {
		t.Fatalf("typed check: %v\n%s", err, out)
	}
	after, err := os.ReadFile(baseline)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("check changed baseline")
	}
	// A fabricated typed rule entry is rejected, never used to suppress the hit.
	write(".unstring-baseline.yaml", strings.Replace(string(before), "rules:\n", "rules:\n  go-typed-closed-set-literal:\n    src/example.go: 1\n", 1))
	out, err = run("check")
	if err == nil || !strings.Contains(out, "go-typed-closed-set-literal") {
		t.Fatalf("fabricated baseline: %v\n%s", err, out)
	}
}
