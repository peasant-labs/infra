package ratchet

import (
	"fmt"
	"strings"

	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// FindingKind is the closed set of reasons a run fails.
type FindingKind string

const (
	// KindNewFile is a hit in a file that has no baseline entry for its rule.
	KindNewFile FindingKind = "new-file"
	// KindRaised is a file whose count for a rule is above its baseline entry.
	KindRaised FindingKind = "raised"
	// KindStaleLower is a file whose count is above zero and below its entry.
	KindStaleLower FindingKind = "stale-lower"
	// KindStaleMissingFile is an entry whose file has no hit left or is gone.
	KindStaleMissingFile FindingKind = "stale-missing-file"
	// KindMarkerNoReason is a marker with no reason, a reason under three
	// words, or a separator other than the em dash.
	KindMarkerNoReason FindingKind = "marker-no-reason"
	// KindMarkerUnused is a marker whose target line has no hit of its rule.
	KindMarkerUnused FindingKind = "marker-unused"
	// KindMarkerUnknownRule is a marker that names an id that is not a rule.
	KindMarkerUnknownRule FindingKind = "marker-unknown-rule"
	// KindMarkerNotAccepted is a marker that names a rule that takes no marker.
	// The hit it sits on is still counted.
	KindMarkerNotAccepted FindingKind = "marker-not-accepted"
	// KindNativeIgnoreOnGuardRule is an `ast-grep-ignore` comment that names a
	// guard rule id.
	KindNativeIgnoreOnGuardRule FindingKind = "native-ignore-on-guard-rule"
	// KindBaselineHeader is a baseline whose `tool` or `ast-grep` line differs
	// from the running tool.
	KindBaselineHeader FindingKind = "baseline-header"
	// KindEnvironment is a failure to run at all: config, ast-grep version,
	// schema module, unreadable baseline.
	KindEnvironment FindingKind = "environment"
)

// AllFindingKinds lists every FindingKind in the order of the message
// contract.
var AllFindingKinds = []FindingKind{
	KindNewFile,
	KindRaised,
	KindStaleLower,
	KindStaleMissingFile,
	KindMarkerNoReason,
	KindMarkerUnused,
	KindMarkerUnknownRule,
	KindMarkerNotAccepted,
	KindNativeIgnoreOnGuardRule,
	KindBaselineHeader,
	KindEnvironment,
}

// String returns the wire value printed in the report.
func (k FindingKind) String() string { return string(k) }

// IsValid reports whether k is a member of AllFindingKinds.
func (k FindingKind) IsValid() bool {
	switch k {
	case KindNewFile, KindRaised, KindStaleLower, KindStaleMissingFile,
		KindMarkerNoReason, KindMarkerUnused, KindMarkerUnknownRule, KindMarkerNotAccepted,
		KindNativeIgnoreOnGuardRule, KindBaselineHeader, KindEnvironment:
		return true
	}
	return false
}

// ParseFindingKind converts a wire value into a FindingKind.
func ParseFindingKind(s string) (FindingKind, error) {
	if k := FindingKind(s); k.IsValid() {
		return k, nil
	}
	names := make([]string, len(AllFindingKinds))
	for i, k := range AllFindingKinds {
		names[i] = k.String()
	}
	return "", fmt.Errorf("unknown finding kind %q: a finding is one of %s", s, strings.Join(names, ", "))
}

// Finding is one reported failure. Every finding prints its location line and
// then four non-empty parts: what, why, where and fix.
type Finding struct {
	// Kind is why the run fails.
	Kind FindingKind
	// Rule is the rule the finding concerns. It is empty for findings that
	// concern no single rule (KindBaselineHeader, KindEnvironment).
	Rule rules.RuleID
	// File is relative to the config directory, with `/` separators. It is
	// empty when the finding has no file.
	File string
	// Line is 1-based, and 0 when the finding concerns a whole file or none.
	Line int
	// Text is the matched source text, when there is one.
	Text string
	// What says what was found.
	What string
	// Why says why it is wrong.
	Why string
	// Where names the step that found it, for example the baseline comparison
	// with its allowed and found counts.
	Where string
	// Fix says what to do: the typed value to use, or the command to run.
	Fix string
}
