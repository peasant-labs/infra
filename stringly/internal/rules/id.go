package rules

import "fmt"

// RuleID is the closed set of rule ids. An id is the same in every repository
// that runs the tool, and it is the key of the baseline, of allow markers and
// of `exempt` entries.
//
// A case that exists in both languages has one id per language (for example
// closed-set-literal and go-closed-set-literal): an ast-grep rule has exactly
// one language, the fixes differ, and a per-language count is what a
// burn-down tracks.
type RuleID string

const (
	// JSXLiteralTestID flags a `data-testid` attribute set from a string.
	JSXLiteralTestID RuleID = "jsx-literal-testid"
	// TestIDQueryLiteral flags a test-id query called with a literal.
	TestIDQueryLiteral RuleID = "testid-query-literal"
	// TestIDSelectorString flags a string that builds a `data-testid=` selector.
	TestIDSelectorString RuleID = "testid-selector-string"
	// TextContentLocator flags an element located by comparing its
	// textContent or innerText with a non-empty literal.
	TextContentLocator RuleID = "textcontent-locator"
	// CopyQueryLiteral flags literal copy in a text, label or role-name query.
	CopyQueryLiteral RuleID = "copy-query-literal"
	// ClosedSetLiteral flags a catalog member written as a literal operand of a
	// comparison or as a case label (TS and JS). Generated from the catalog.
	ClosedSetLiteral RuleID = "closed-set-literal"
	// GoClosedSetLiteral is the Go twin of ClosedSetLiteral, which also flags a
	// key of a map literal whose key type is a set's type. Generated from the
	// catalog.
	GoClosedSetLiteral RuleID = "go-closed-set-literal"
	// BoundaryCast flags `as X` applied directly to a boundary value.
	BoundaryCast RuleID = "boundary-cast"
	// ExplicitAny flags the type keyword `any`.
	ExplicitAny RuleID = "explicit-any"
	// TSEnum flags an `enum` declaration.
	TSEnum RuleID = "ts-enum"
	// GoJSONKeyAssert flags decoded JSON read by string key: `m["key"].(T)`.
	GoJSONKeyAssert RuleID = "go-json-key-assert"
	// EndpointLiteral flags an API path written as a literal (TS and JS).
	EndpointLiteral RuleID = "endpoint-literal"
	// GoEndpointLiteral is the Go twin of EndpointLiteral, which also flags
	// any `/…` argument in a configured router file.
	GoEndpointLiteral RuleID = "go-endpoint-literal"
	// DataPathLiteral flags a relative path through a data directory, or a
	// data file name passed straight to a file read (TS and JS).
	DataPathLiteral RuleID = "data-path-literal"
	// GoDataPathLiteral is the Go twin of DataPathLiteral.
	GoDataPathLiteral RuleID = "go-data-path-literal"
	// ExportedLiteralUnion flags an exported type alias whose whole value is a
	// union of two or more string literals.
	ExportedLiteralUnion RuleID = "exported-literal-union"
	// GoJSONBodyMap flags an outermost `map[string]any` literal with at least
	// one string-literal key: a JSON body written by string key.
	GoJSONBodyMap RuleID = "go-json-body-map"
	// JSONBodyObject flags `JSON.stringify({…})` in a request, response or
	// socket body position.
	JSONBodyObject RuleID = "json-body-object"
)

// AllRuleIDs lists every rule id in the order of the design's rule table.
var AllRuleIDs = []RuleID{
	JSXLiteralTestID,
	TestIDQueryLiteral,
	TestIDSelectorString,
	TextContentLocator,
	CopyQueryLiteral,
	ClosedSetLiteral,
	GoClosedSetLiteral,
	BoundaryCast,
	ExplicitAny,
	TSEnum,
	GoJSONKeyAssert,
	EndpointLiteral,
	GoEndpointLiteral,
	DataPathLiteral,
	GoDataPathLiteral,
	ExportedLiteralUnion,
	GoJSONBodyMap,
	JSONBodyObject,
}

// String returns the wire value used in the baseline, markers and messages.
func (id RuleID) String() string { return string(id) }

// IsValid reports whether id is a member of AllRuleIDs.
func (id RuleID) IsValid() bool { return id.Lang().IsValid() }

// ParseRuleID converts a wire value (a baseline key, a marker's rule id, an
// `exempt` entry) into a RuleID.
func ParseRuleID(s string) (RuleID, error) {
	if id := RuleID(s); id.IsValid() {
		return id, nil
	}
	return "", fmt.Errorf("unknown rule id %q: it is not one of the tool's rules (%s); "+
		"check the spelling, or run `stringly show rules` to list them", s, joinStrings(AllRuleIDs))
}

// Lang returns the one language the rule is written for. It returns the zero
// Lang for an id that is not in AllRuleIDs.
func (id RuleID) Lang() Lang {
	switch id {
	case JSXLiteralTestID, TestIDQueryLiteral, TestIDSelectorString, TextContentLocator,
		CopyQueryLiteral, ClosedSetLiteral, BoundaryCast, ExplicitAny, TSEnum,
		EndpointLiteral, DataPathLiteral, ExportedLiteralUnion, JSONBodyObject:
		return LangTS
	case GoClosedSetLiteral, GoJSONKeyAssert, GoEndpointLiteral, GoDataPathLiteral, GoJSONBodyMap:
		return LangGo
	}
	return ""
}

// Origin reports whether the rule is embedded in the tool or generated from
// the catalog on every run. It returns the zero Origin for an id that is not
// in AllRuleIDs.
func (id RuleID) Origin() Origin {
	switch id {
	case ClosedSetLiteral, GoClosedSetLiteral:
		return OriginGenerated
	}
	if id.IsValid() {
		return OriginStatic
	}
	return ""
}

// AcceptsMarker reports whether a `stringly-allow` marker may suppress a hit
// of this rule. The two closed-set rules take no marker: the fix for a set
// value is always the typed value. It returns false for an id that is not in
// AllRuleIDs.
func (id RuleID) AcceptsMarker() bool {
	switch id {
	case ClosedSetLiteral, GoClosedSetLiteral:
		return false
	}
	return id.IsValid()
}
