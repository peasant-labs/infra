package rules

// Rule is one ast-grep rule the tool runs, static or generated, together with
// the message parts a finding of that rule prints.
//
// The rule's language and whether it accepts an allow marker are properties of
// its id, not fields, so a Rule cannot disagree with the closed set.
type Rule struct {
	// ID is the rule's id. The ast-grep document's `id:` must equal it.
	ID RuleID
	// Document is the ast-grep rule document (YAML) written to the scan's
	// temporary rule directory. Its severity is `warning`: the checker alone
	// decides pass or fail.
	Document []byte
	// Message holds the rule-specific parts of the message contract. The
	// `where` part is the checker's: it names the baseline comparison.
	Message Message
}

// Lang returns the one language the rule is written for.
func (r Rule) Lang() Lang { return r.ID.Lang() }

// Origin reports whether the rule is embedded or generated.
func (r Rule) Origin() Origin { return r.ID.Origin() }

// AcceptsMarker reports whether a `unstring-allow` marker may suppress a hit
// of this rule.
func (r Rule) AcceptsMarker() bool { return r.ID.AcceptsMarker() }

// Message is the rule-specific text of a finding: what was found, why it is
// wrong, and how to fix it. Every part is non-empty for every rule.
type Message struct {
	// What names what was found, for example "a test id is set from a string
	// literal".
	What string
	// Why says why the literal is wrong: what fails silently when it drifts.
	Why string
	// Fix names the typed value, label set or contract type to use instead,
	// and whether the rule accepts an allow marker.
	Fix string
}

// Params are the repository settings that shape the static rules. They come
// from the repository's `.unstring.yaml`; defaults are applied by the config
// package before they reach here.
type Params struct {
	// EndpointPrefixes are the API path prefixes the endpoint rules look for,
	// for example "/api/v1".
	EndpointPrefixes []string
	// EndpointCallees are TS functions whose first argument is an API path.
	EndpointCallees []string
	// MountFiles are Go router files, relative to the config directory, in
	// which any string argument starting with "/" is an endpoint literal.
	MountFiles []string
	// DataDirs are the directory names whose relative paths the data-path
	// rules flag, for example "testdata".
	DataDirs []string
}

// Registry is the set of static rules, built for one repository's Params.
// It holds every RuleID whose Origin is OriginStatic and nothing else.
type Registry interface {
	// Rules returns every static rule, in AllRuleIDs order.
	Rules() []Rule
	// Rule returns the static rule with the given id, and false when the id is
	// not a static rule.
	Rule(id RuleID) (Rule, bool)
}
