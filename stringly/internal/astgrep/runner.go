package astgrep

import "context"

// RequiredVersion is the exact ast-grep version the tool runs with. Rule
// matching and suppression parsing differ between ast-grep versions, so any
// other version stops the run before a scan.
const RequiredVersion = "0.51.2"

// Runner is the boundary to the ast-grep binary.
type Runner interface {
	// Version returns the version the binary reports, for example "0.51.2".
	Version(ctx context.Context) (string, error)
	// Scan runs the given rules over the roots and returns every match. The
	// temporary rule directory is removed before Scan returns, on success and
	// on failure.
	Scan(ctx context.Context, req ScanRequest) ([]Match, error)
}

// ScanRequest is one ast-grep invocation.
type ScanRequest struct {
	// Dir is the working directory of the scan: the directory that holds the
	// repository's config. Every path in a Match is relative to it.
	Dir string
	// Roots are the paths to scan, relative to Dir.
	Roots []string
	// Rules are the ast-grep rule documents to run, one file each.
	Rules []RuleFile
}

// RuleFile is one ast-grep rule document written to the scan's temporary rule
// directory.
type RuleFile struct {
	// Name is the file's base name, for example "jsx-literal-testid.yml".
	Name string
	// Body is the YAML document.
	Body []byte
}

// Match is one record of `ast-grep scan --json=stream`.
type Match struct {
	// RuleID is ast-grep's rule id for the record. It is a tool RuleID only
	// when the matching document was one of the tool's rules.
	RuleID string
	// File is the path relative to the scan's Dir, with `/` separators.
	File string
	// Line is the 1-based start line of the match (ast-grep reports 0-based
	// lines; the runner converts them).
	Line int
	// Text is the matched source text.
	Text string
	// Vars maps each single metavariable the rule bound (without the `$`) to
	// its matched text.
	Vars map[string]string
}
