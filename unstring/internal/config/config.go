package config

// DefaultPath is the config file the tool reads when `--config` is not given,
// relative to the working directory.
const DefaultPath = ".unstring.yaml"

// FormatVersion is the only `version:` value this tool reads.
const FormatVersion = 1

// DefaultBaselinePath is the baseline file when the config names none,
// relative to the config directory.
const DefaultBaselinePath = ".unstring-baseline.yaml"

// ExemptAllRules is the `rule:` value of an exempt entry that names every
// rule, used for generated code.
const ExemptAllRules = "*"

// DefaultEndpointPrefixes returns the API prefixes used when the config sets
// none. `/api/v1` keeps third-party paths such as `/api/v4` out.
func DefaultEndpointPrefixes() []string { return []string{"/api/v1"} }

// DefaultDataDirs returns the data-directory names used when the config sets
// none.
func DefaultDataDirs() []string { return []string{"testdata", "fixtures", "__fixtures__", "captures"} }

// Config is a repository's `.unstring.yaml`. Paths are relative to the
// directory that holds the file, with `/` separators.
type Config struct {
	// Version is the config format version; it must equal FormatVersion.
	Version int `yaml:"version"`
	// Baseline is the baseline file's path.
	Baseline string `yaml:"baseline"`
	// TS lists the TS and JS roots; nil when the repository has none.
	TS *Roots `yaml:"ts"`
	// Go lists the Go roots; nil when the repository has none.
	Go *Roots `yaml:"go"`
	// Schema names the contract module whose sets join the catalog.
	Schema Schema `yaml:"schema"`
	// Endpoints shapes the endpoint rules.
	Endpoints Endpoints `yaml:"endpoints"`
	// DataDirs are the directory names the data-path rules look for.
	DataDirs []string `yaml:"dataDirs"`
	// Exempt lists the definition-site exemptions, each with a reason.
	Exempt []Exempt `yaml:"exempt"`
}

// Roots are the directories scanned for one language.
type Roots struct {
	Roots []string `yaml:"roots"`
}

// Schema names the pinned contract module.
type Schema struct {
	// Module is the Go module path, for example
	// "github.com/peasant-labs/schema".
	Module string `yaml:"module"`
}

// Endpoints configures the endpoint rules.
type Endpoints struct {
	// Prefixes are the API path prefixes.
	Prefixes []string `yaml:"prefixes"`
	// Callees are TS functions whose first argument is an API path.
	Callees []string `yaml:"callees"`
	// MountFiles are Go router files in which any `/…` argument is an
	// endpoint literal.
	MountFiles []string `yaml:"mountFiles"`
}

// Exempt is one definition-site exemption.
type Exempt struct {
	// Rule is a rule id, or ExemptAllRules for generated code. It is checked
	// against rules.AllRuleIDs when the config is loaded.
	Rule string `yaml:"rule"`
	// Paths are the exempt files or globs.
	Paths []string `yaml:"paths"`
	// Reason says why the site is exempt. It is required.
	Reason string `yaml:"reason"`
}
