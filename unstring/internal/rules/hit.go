package rules

// Hit is one distinct match of a rule: the unit the baseline counts. Two
// matches of one rule on one line are one hit.
type Hit struct {
	// Rule is the rule that matched.
	Rule RuleID
	// File is the path relative to the directory holding the config, with `/`
	// separators.
	File string
	// Line is the 1-based start line of the matched literal.
	Line int
	// Text is the matched source text, printed in the finding's first line.
	Text string
}

// HitKey identifies a hit for deduplication: rule, file and line.
type HitKey struct {
	Rule RuleID
	File string
	Line int
}

// Key returns the identity of the hit, without its text.
func (h Hit) Key() HitKey { return HitKey{Rule: h.Rule, File: h.File, Line: h.Line} }
