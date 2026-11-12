package astgrep

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/stringly/internal/failure"
)

// DefaultCommand is the binary name looked up on PATH when no path is given.
const DefaultCommand = "ast-grep"

// DefaultLanguageGlobs returns the languageGlobs of a scan that sets none:
// every TS and JS form is parsed as TSX, so one rule covers .ts .tsx .js .jsx
// .mjs .cjs.
func DefaultLanguageGlobs() map[string][]string {
	return map[string][]string{"tsx": {"*.ts", "*.tsx", "*.js", "*.jsx", "*.mjs", "*.cjs"}}
}

// InstallFix is the fix line every ast-grep version failure prints.
const InstallFix = "enter the repository dev shell (nix develop), or run\n" +
	"npm install --global @ast-grep/cli@" + RequiredVersion + ", or pass --ast-grep PATH."

// Binary runs a real ast-grep executable.
type Binary struct {
	// Path is the executable: a path, or a name looked up on PATH.
	Path string
	// Env is the process environment for ast-grep, in os.Environ form. Nil
	// means the current process environment.
	Env []string
	// TempDir is where the scan's rule directory is created. Empty means
	// os.TempDir().
	TempDir string
}

var _ Runner = (*Binary)(nil)

// NewBinary returns a runner for the executable at path, or for
// DefaultCommand on PATH when path is empty.
func NewBinary(path string) *Binary {
	if path == "" {
		path = DefaultCommand
	}
	return &Binary{Path: path}
}

// Resolved returns the absolute path the executable resolves to, for
// messages. It returns the configured path when it cannot be resolved.
func (b *Binary) Resolved() string {
	p, err := exec.LookPath(b.Path)
	if err != nil {
		return b.Path
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// Version runs `ast-grep --version` and returns the version it prints.
func (b *Binary) Version(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, b.Path, "--version")
	cmd.Env = b.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s --version: %w: %s", b.Path, err, strings.TrimSpace(stderr.String()))
	}
	return parseVersion(stdout.String())
}

// parseVersion reads "ast-grep 0.51.2" (or a bare "0.51.2").
func parseVersion(out string) (string, error) {
	fields := strings.Fields(out)
	switch {
	case len(fields) >= 2 && (fields[0] == "ast-grep" || fields[0] == "sg"):
		return fields[1], nil
	case len(fields) == 1:
		return fields[0], nil
	}
	return "", fmt.Errorf("unrecognised version output %q", strings.TrimSpace(out))
}

// CheckVersion asserts that the binary reports exactly RequiredVersion. It
// returns a *failure.Error naming the found and required versions.
func CheckVersion(ctx context.Context, b *Binary) error {
	where := "version check, before any scan. No file was scanned."
	why := "rule matching and suppression parsing differ between ast-grep versions, so counts would\ndiffer between machines."
	got, err := b.Version(ctx)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return failure.New(
				fmt.Sprintf("ast-grep was not found at %s, and this tool requires exactly %s.", b.Path, RequiredVersion),
				"stringly scans with ast-grep; without it no rule can run.",
				where, InstallFix)
		}
		return failure.New(
			fmt.Sprintf("the version of ast-grep at %s could not be read (%v); this tool requires exactly %s.", b.Resolved(), err, RequiredVersion),
			why, where, InstallFix)
	}
	if got != RequiredVersion {
		return failure.New(
			fmt.Sprintf("ast-grep %s found at %s, but this tool requires exactly %s.", got, b.Resolved(), RequiredVersion),
			why, where, InstallFix)
	}
	return nil
}

// sgConfig is the temporary sgconfig.yml of one scan.
type sgConfig struct {
	RuleDirs      []string            `yaml:"ruleDirs"`
	LanguageGlobs map[string][]string `yaml:"languageGlobs"`
}

// Scan writes the rules and an sgconfig.yml to a temporary directory, runs
// `ast-grep scan --config <tmp>/sgconfig.yml --json=stream <roots>` from
// req.Dir, and decodes every record. The temporary directory is removed
// before Scan returns. It never passes --error=unused-suppression: a
// repository's own ast-grep-ignore comments surface as unused-suppression
// records, which the caller ignores.
func (b *Binary) Scan(ctx context.Context, req ScanRequest) ([]Match, error) {
	if len(req.Rules) == 0 || len(req.Roots) == 0 {
		return nil, nil
	}
	tmp, err := os.MkdirTemp(b.TempDir, "stringly-rules-")
	if err != nil {
		return nil, fmt.Errorf("create the temporary rule directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	ruleDir := filepath.Join(tmp, "rules")
	if err := os.Mkdir(ruleDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", ruleDir, err)
	}
	seen := map[string]bool{}
	for _, r := range req.Rules {
		if r.Name == "" || r.Name != filepath.Base(r.Name) || seen[r.Name] {
			return nil, fmt.Errorf("rule file name %q is empty, a path, or used twice", r.Name)
		}
		seen[r.Name] = true
		if err := os.WriteFile(filepath.Join(ruleDir, r.Name), r.Body, 0o600); err != nil {
			return nil, fmt.Errorf("write rule %s: %w", r.Name, err)
		}
	}
	globs := req.LanguageGlobs
	if globs == nil {
		globs = DefaultLanguageGlobs()
	}
	cfg, err := yaml.Marshal(sgConfig{RuleDirs: []string{"rules"}, LanguageGlobs: globs})
	if err != nil {
		return nil, fmt.Errorf("encode sgconfig.yml: %w", err)
	}
	cfgPath := filepath.Join(tmp, "sgconfig.yml")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", cfgPath, err)
	}

	args := append([]string{"scan", "--config", cfgPath, "--json=stream"}, req.Roots...)
	cmd := exec.CommandContext(ctx, b.Path, args...)
	cmd.Dir = req.Dir
	cmd.Env = b.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, &ScanError{Args: args, Err: err, Stderr: strings.TrimSpace(stderr.String())}
	}
	return decodeStream(stdout.Bytes())
}

// ScanError is a failed ast-grep invocation, with ast-grep's own stderr.
type ScanError struct {
	Args   []string
	Err    error
	Stderr string
}

func (e *ScanError) Error() string {
	return fmt.Sprintf("ast-grep %s: %v\n%s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}

// record is the part of one --json=stream record the tool reads.
type record struct {
	RuleID string `json:"ruleId"`
	File   string `json:"file"`
	Text   string `json:"text"`
	Lines  string `json:"lines"`
	Range  struct {
		Start position `json:"start"`
		End   position `json:"end"`
	} `json:"range"`
	MetaVariables struct {
		Single map[string]struct {
			Text string `json:"text"`
		} `json:"single"`
	} `json:"metaVariables"`
}

type position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

func decodeStream(out []byte) ([]Match, error) {
	var matches []Match
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("decode ast-grep record %d: %w", n, err)
		}
		m := Match{
			RuleID:   r.RuleID,
			File:     filepath.ToSlash(strings.TrimPrefix(r.File, "./")),
			Line:     r.Range.Start.Line + 1,
			EndLine:  r.Range.End.Line + 1,
			Column:   r.Range.Start.Column + 1,
			Text:     r.Text,
			LineText: firstLine(r.Lines),
		}
		if len(r.MetaVariables.Single) > 0 {
			m.Vars = make(map[string]string, len(r.MetaVariables.Single))
			for k, v := range r.MetaVariables.Single {
				m.Vars[k] = v.Text
			}
		}
		matches = append(matches, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read ast-grep output: %w", err)
	}
	return matches, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
