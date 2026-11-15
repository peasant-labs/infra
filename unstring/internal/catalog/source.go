package catalog

import (
	"fmt"
	"strings"
)

// SourceKind is the closed set of definition sites the catalog recognises.
// The fix a finding prints depends on it.
type SourceKind string

const (
	// SourceClosedCall is a `closed('<wire>', …)` call whose callee is the
	// identifier `closed` (TS, JS). The member is the first argument.
	SourceClosedCall SourceKind = "closed-call"
	// SourceRecordTable is a module-level frozen object typed
	// `Record<K, …>` with K a set type (TS, JS, JSDoc). The members are its
	// keys.
	SourceRecordTable SourceKind = "record-table"
	// SourceTSUnion is a string literal that is a direct member of a TS union
	// type, wherever the union stands.
	SourceTSUnion SourceKind = "ts-union"
	// SourceJSDocUnion is two or more quoted strings joined by `|` inside one
	// `{…}` of a JSDoc tag.
	SourceJSDocUnion SourceKind = "jsdoc-union"
	// SourceGoTypedConstant is a constant of a named string type that has two
	// or more constants in the non-test files of one package.
	SourceGoTypedConstant SourceKind = "go-typed-constant"
	// SourceSchemaModule is a Go typed constant or a generated TS union of the
	// pinned schema module.
	SourceSchemaModule SourceKind = "schema-module"
)

// AllSourceKinds lists every SourceKind in the order of the design's source
// table.
var AllSourceKinds = []SourceKind{
	SourceClosedCall,
	SourceRecordTable,
	SourceTSUnion,
	SourceJSDocUnion,
	SourceGoTypedConstant,
	SourceSchemaModule,
}

// String returns the wire value, as `unstring show catalog` prints it.
func (k SourceKind) String() string { return string(k) }

// IsValid reports whether k is a member of AllSourceKinds.
func (k SourceKind) IsValid() bool {
	switch k {
	case SourceClosedCall, SourceRecordTable, SourceTSUnion, SourceJSDocUnion,
		SourceGoTypedConstant, SourceSchemaModule:
		return true
	}
	return false
}

// ParseSourceKind converts a wire value into a SourceKind.
func ParseSourceKind(s string) (SourceKind, error) {
	if k := SourceKind(s); k.IsValid() {
		return k, nil
	}
	names := make([]string, len(AllSourceKinds))
	for i, k := range AllSourceKinds {
		names[i] = k.String()
	}
	return "", fmt.Errorf("unknown catalog source kind %q: the catalog recognises exactly %s",
		s, strings.Join(names, ", "))
}
