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
	// Typed enables optional type-aware checks, independently of the baseline.
	Typed Typed `yaml:"typed"`
	// Schema names the contract module whose sets join the catalog.
	Schema Schema `yaml:"schema"`
	// Endpoints shapes the endpoint rules.
	Endpoints Endpoints `yaml:"endpoints"`
	// DataDirs are the directory names the data-path rules look for.
	DataDirs []string `yaml:"dataDirs"`
	// Exempt lists the definition-site exemptions, each with a reason.
	Exempt []Exempt `yaml:"exempt"`
	// Exclude lists the paths no rule scans and no catalog harvests: vendored
	// third-party code the repository does not own.
	Exclude []Exclude `yaml:"exclude"`
}

// DefaultMountRouters returns the router packages the typed mount pass
// matches when the config names none.
func DefaultMountRouters() []string { return []string{"github.com/go-chi/chi/v5"} }

// Typed configures opt-in type-aware checks.
type Typed struct {
	GoClosedSetLiteral bool `yaml:"goClosedSetLiteral"`
	// GoMountLiteral configures the typed route-mount pass.
	GoMountLiteral GoMountLiteral `yaml:"goMountLiteral"`
}

// GoMountLiteral configures the typed pass that reports a route mounted from
// a literal pattern on a router type resolved with go/types.
type GoMountLiteral struct {
	// Enabled turns the pass on; the other keys are refused without it.
	Enabled bool `yaml:"enabled"`
	// Routers are the import paths of the router packages whose mount
	// methods are matched. Defaults to DefaultMountRouters.
	Routers []string `yaml:"routers"`
	// IncludeTests scans `_test.go` files too. Off by default: a test
	// usually builds its own router, which is fixture code and not a
	// production mount.
	IncludeTests bool `yaml:"includeTests"`
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
	// MountFiles are Go router files, as exact paths or globs, in which any
	// `/…` argument is an endpoint literal. A glob is not expanded here;
	// ast-grep matches the files: list.
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

// Exclude is one exclusion: paths that are outside the repository's
// ownership, such as a vendored copy of an upstream library. An excluded file
// is not scanned by any rule of either language, contributes no catalog
// member, and has no baseline entry.
type Exclude struct {
	// Paths are the excluded files, directories or globs, matched with
	// MatchPath like exempt paths.
	Paths []string `yaml:"paths"`
	// Reason says why the code is not the repository's own. It is required.
	Reason string `yaml:"reason"`
}
