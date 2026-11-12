package catalog_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
	"github.com/peasant-labs/infra/stringly/internal/catalog"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// The harness runs the real ast-grep binary. It must be the version the tool
// requires; a missing or different binary fails the tests with the install
// command, it never skips them.
var (
	astGrepOnce sync.Once
	astGrepPath string
	astGrepErr  error
)

func astGrepBinary(t *testing.T) string {
	t.Helper()
	astGrepOnce.Do(func() {
		p, err := exec.LookPath("ast-grep")
		if err != nil {
			astGrepErr = fmt.Errorf("ast-grep is not on PATH: %w", err)
			return
		}
		out, err := exec.Command(p, "--version").Output()
		if err != nil {
			astGrepErr = fmt.Errorf("%s --version failed: %w", p, err)
			return
		}
		if got := strings.TrimSpace(string(out)); got != "ast-grep "+astgrep.RequiredVersion {
			astGrepErr = fmt.Errorf("%s reports %q, but the tests need ast-grep %s", p, got, astgrep.RequiredVersion)
			return
		}
		astGrepPath = p
	})
	if astGrepErr != nil {
		t.Fatalf("%v. Fix: npm install --global @ast-grep/cli@%s, or put that version first on PATH.", astGrepErr, astgrep.RequiredVersion)
	}
	return astGrepPath
}

// sgRunner is a test double for the ast-grep runner boundary: it runs the
// real binary with the rules it is given and the languageGlobs the tool uses
// (TS and JS parsed as TSX).
type sgRunner struct {
	bin string
}

func newRunner(t *testing.T) sgRunner { return sgRunner{bin: astGrepBinary(t)} }

func (r sgRunner) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, r.bin, "--version").Output()
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "ast-grep "), err
}

// sgConfig is the scan config: the rule directory, and the TS file globs
// mapped to the TS rules' ast-grep language, from the rules package (the same
// values the command's scan config carries).
func sgConfig() ([]byte, error) {
	return yaml.Marshal(map[string]any{
		"ruleDirs": []string{"rules"},
		"languageGlobs": map[string][]string{
			rules.LangTS.AstGrepLanguage(): rules.LangTS.FileGlobs(),
		},
	})
}

func (r sgRunner) Scan(ctx context.Context, req astgrep.ScanRequest) ([]astgrep.Match, error) {
	tmp, err := os.MkdirTemp("", "stringly-catalog-test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := os.Mkdir(filepath.Join(tmp, "rules"), 0o755); err != nil {
		return nil, err
	}
	for _, f := range req.Rules {
		if err := os.WriteFile(filepath.Join(tmp, "rules", f.Name), f.Body, 0o644); err != nil {
			return nil, err
		}
	}
	config, err := sgConfig()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "sgconfig.yml"), config, 0o644); err != nil {
		return nil, err
	}
	args := append([]string{"scan", "--config", filepath.Join(tmp, "sgconfig.yml"), "--json=stream"}, req.Roots...)
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = req.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ast-grep scan: %w: %s", err, stderr.String())
	}
	var matches []astgrep.Match
	dec := json.NewDecoder(&stdout)
	for dec.More() {
		var rec struct {
			RuleID string `json:"ruleId"`
			File   string `json:"file"`
			Text   string `json:"text"`
			Range  struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
			MetaVariables struct {
				Single map[string]struct {
					Text string `json:"text"`
				} `json:"single"`
			} `json:"metaVariables"`
		}
		if err := dec.Decode(&rec); err != nil {
			return nil, err
		}
		vars := map[string]string{}
		for k, v := range rec.MetaVariables.Single {
			vars[k] = v.Text
		}
		matches = append(matches, astgrep.Match{
			RuleID: rec.RuleID,
			File:   strings.TrimPrefix(path.Clean(filepath.ToSlash(rec.File)), "./"),
			Line:   rec.Range.Start.Line + 1,
			Text:   rec.Text,
			Vars:   vars,
		})
	}
	return matches, nil
}

// writeTree writes files (path relative to dir, `/` separators) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fixtureModules resolves exactly one module version to a directory, without
// the go command. Any other request is the error a missing module produces.
type fixtureModules struct {
	module, version, dir string
}

func (f fixtureModules) Resolve(_ context.Context, module, version string, _ []string) (string, error) {
	if module != f.module || version != f.version {
		return "", &catalog.BuildError{
			What: fmt.Sprintf("the fixture resolver holds %s@%s, not %s@%s.", f.module, f.version, module, version),
			Why:  "test fixture", Where: "fixture resolver", Fix: "pin the fixture's version",
		}
	}
	return f.dir, nil
}

// schemaFixtureDir is the committed plain-source schema module.
const schemaFixtureDir = "testdata/schema-module"

const (
	schemaFixtureModule  = "example.test/schema"
	schemaFixtureVersion = "v0.0.1"
)

// buildProxy writes a GOPROXY=file:// tree for module@version from the plain
// source under src, with the standard library's archive/zip.
func buildProxy(t *testing.T, src, module, version string) string {
	t.Helper()
	proxy := t.TempDir()
	at := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		w, err := zw.Create(module + "@" + version + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"list":            []byte(version + "\n"),
		version + ".info": []byte(`{"Version":"` + version + `","Time":"2026-11-12T00:00:00Z"}`),
		version + ".mod":  gomod,
		version + ".zip":  buf.Bytes(),
	} {
		if err := os.WriteFile(filepath.Join(at, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return proxy
}

// offlineGoEnv is the environment that makes `go mod download` read only the
// file proxy and write a module cache the test can remove.
func offlineGoEnv(t *testing.T, proxy string) []string {
	t.Helper()
	return append(os.Environ(),
		"GOPROXY=file://"+filepath.ToSlash(proxy),
		"GOSUMDB=off",
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"GOMODCACHE="+filepath.Join(t.TempDir(), "modcache"),
		"GOFLAGS=-modcacherw",
	)
}

// decodeStrict decodes one YAML fixture, refusing unknown fields.
func decodeStrict(t *testing.T, name string, data []byte, v any) {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}
