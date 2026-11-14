package rules

import "fmt"

// ExcludedOperand is the closed list of operand shapes the two closed-set
// rules do not flag. An exclusion is made by the shape of the other operand,
// never by the literal's value: `error` and `open` are DOM event names and
// also members of our sets.
//
// Every exclusion applies in two forms: the operand of a comparison, and the
// value of a `switch` whose `case` label is the literal. The generated rules'
// `not` clauses are built from this list, and the rule fixtures are checked
// against it, so an exclusion cannot exist without its cases.
type ExcludedOperand string

const (
	// ExcludedTypeof is `typeof x`: the JavaScript type names.
	ExcludedTypeof ExcludedOperand = "typeof"
	// ExcludedGoRuntimeGOOS is Go's `runtime.GOOS`: the platform names.
	ExcludedGoRuntimeGOOS ExcludedOperand = "go-runtime-goos"
	// ExcludedGoRuntimeGOARCH is Go's `runtime.GOARCH`: the architecture names.
	ExcludedGoRuntimeGOARCH ExcludedOperand = "go-runtime-goarch"
	// ExcludedKey is `x.key`: keyboard keys.
	ExcludedKey ExcludedOperand = "key"
	// ExcludedCode is `x.code`: keyboard codes.
	ExcludedCode ExcludedOperand = "code"
	// ExcludedTagName is `x.tagName`: HTML tag names.
	ExcludedTagName ExcludedOperand = "tag-name"
	// ExcludedNodeName is `x.nodeName`: HTML tag names.
	ExcludedNodeName ExcludedOperand = "node-name"
	// ExcludedLocalName is `x.localName`: HTML tag names.
	ExcludedLocalName ExcludedOperand = "local-name"
	// ExcludedAriaProperty is `x.aria…`, a property whose name starts with
	// `aria`: ARIA.
	ExcludedAriaProperty ExcludedOperand = "aria-property"
	// ExcludedGetAttributeAria is `x.getAttribute('aria-…')`: ARIA.
	ExcludedGetAttributeAria ExcludedOperand = "get-attribute-aria"
	// ExcludedGetAttributeRole is `x.getAttribute('role')`: ARIA roles.
	ExcludedGetAttributeRole ExcludedOperand = "get-attribute-role"
	// ExcludedAwaitGetAttribute is `await x.getAttribute('aria-…' or 'role')`
	// (Playwright): ARIA.
	ExcludedAwaitGetAttribute ExcludedOperand = "await-get-attribute"
	// ExcludedEventType is `event.type`, `e.type`, `ev.type` or `evt.type`: DOM
	// event types. It is the one exclusion that reads a variable name, because
	// an event's `type` has no other shape.
	ExcludedEventType ExcludedOperand = "event-type"
)

// AllExcludedOperands lists every exclusion, language vocabulary first.
var AllExcludedOperands = []ExcludedOperand{
	ExcludedTypeof,
	ExcludedGoRuntimeGOOS,
	ExcludedGoRuntimeGOARCH,
	ExcludedKey,
	ExcludedCode,
	ExcludedTagName,
	ExcludedNodeName,
	ExcludedLocalName,
	ExcludedAriaProperty,
	ExcludedGetAttributeAria,
	ExcludedGetAttributeRole,
	ExcludedAwaitGetAttribute,
	ExcludedEventType,
}

// String returns the wire value, used in fixture case names such as
// `passes-excluded-<id>-comparison`.
func (e ExcludedOperand) String() string { return string(e) }

// IsValid reports whether e is a member of AllExcludedOperands.
func (e ExcludedOperand) IsValid() bool { return e.Group().IsValid() }

// ParseExcludedOperand converts a wire value into an ExcludedOperand.
func ParseExcludedOperand(s string) (ExcludedOperand, error) {
	if e := ExcludedOperand(s); e.IsValid() {
		return e, nil
	}
	return "", fmt.Errorf("unknown excluded operand %q: the closed-set rules exclude exactly %s",
		s, joinStrings(AllExcludedOperands))
}

// Group returns which kind of vocabulary the operand carries. It returns the
// zero ExclusionGroup for a value that is not in AllExcludedOperands.
func (e ExcludedOperand) Group() ExclusionGroup {
	switch e {
	case ExcludedTypeof, ExcludedGoRuntimeGOOS, ExcludedGoRuntimeGOARCH:
		return GroupLanguageVocabulary
	case ExcludedKey, ExcludedCode, ExcludedTagName, ExcludedNodeName, ExcludedLocalName,
		ExcludedAriaProperty, ExcludedGetAttributeAria, ExcludedGetAttributeRole,
		ExcludedAwaitGetAttribute, ExcludedEventType:
		return GroupExternalVocabulary
	}
	return ""
}

// Rule returns the closed-set rule whose generated pattern carries this
// exclusion. It returns the zero RuleID for a value that is not in
// AllExcludedOperands.
func (e ExcludedOperand) Rule() RuleID {
	switch e {
	case ExcludedGoRuntimeGOOS, ExcludedGoRuntimeGOARCH:
		return GoClosedSetLiteral
	}
	if e.IsValid() {
		return ClosedSetLiteral
	}
	return ""
}

// Lang returns the language of the rule the exclusion belongs to.
func (e ExcludedOperand) Lang() Lang { return e.Rule().Lang() }

// ExclusionGroup is the closed set of reasons an operand is excluded. The two
// groups have different authority: language vocabulary is the tool's design,
// external vocabulary is a user decision (ARIA, tag names, events, keys).
type ExclusionGroup string

const (
	// GroupLanguageVocabulary is a word of the language or its runtime that no
	// set of ours could own.
	GroupLanguageVocabulary ExclusionGroup = "language-vocabulary"
	// GroupExternalVocabulary is a vocabulary another standard publishes.
	GroupExternalVocabulary ExclusionGroup = "external-vocabulary"
)

// AllExclusionGroups lists every ExclusionGroup in a fixed order.
var AllExclusionGroups = []ExclusionGroup{GroupLanguageVocabulary, GroupExternalVocabulary}

// String returns the wire value.
func (g ExclusionGroup) String() string { return string(g) }

// IsValid reports whether g is a member of AllExclusionGroups.
func (g ExclusionGroup) IsValid() bool {
	switch g {
	case GroupLanguageVocabulary, GroupExternalVocabulary:
		return true
	}
	return false
}

// ParseExclusionGroup converts a wire value into an ExclusionGroup.
func ParseExclusionGroup(s string) (ExclusionGroup, error) {
	if g := ExclusionGroup(s); g.IsValid() {
		return g, nil
	}
	return "", fmt.Errorf("unknown exclusion group %q: an exclusion is one of %s", s, joinStrings(AllExclusionGroups))
}
