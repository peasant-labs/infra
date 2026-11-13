package policy

import "gopkg.in/yaml.v3"

// hasContinueOnError reports whether a step or job sets the continue-on-error
// key at all. GitHub evaluates that key as a value, and the documented
// fromJSON example (`continue-on-error: ${{ fromJSON(...) }}`) is one way to
// make it true; GitHub then gives a failed step the conclusion success. The
// checker cannot evaluate an arbitrary expression there, so a policy that
// says "no continue-on-error" holds on the key, not on a spelling of its
// value: an absent key is the only shape the checker can read as no.
func hasContinueOnError(node *yaml.Node) bool {
	return mappingValue(node, "continue-on-error") != nil
}
