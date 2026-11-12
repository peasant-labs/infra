package rules

import (
	"fmt"
	"strings"
)

// Lang is the closed set of languages a rule is written for. Every RuleID
// belongs to exactly one Lang, so `--lang` partitions the baseline cleanly.
type Lang string

const (
	// LangGo is Go source (.go files under the config's go.roots).
	LangGo Lang = "go"
	// LangTS is TypeScript and JavaScript (.ts .tsx .js .jsx .mjs .cjs under
	// the config's ts.roots), parsed by ast-grep as TSX.
	LangTS Lang = "ts"
)

// AllLangs lists every Lang in a fixed order.
var AllLangs = []Lang{LangGo, LangTS}

// String returns the wire value, as written after `--lang`.
func (l Lang) String() string { return string(l) }

// IsValid reports whether l is a member of AllLangs.
func (l Lang) IsValid() bool {
	switch l {
	case LangGo, LangTS:
		return true
	}
	return false
}

// ParseLang converts a wire value such as a `--lang` argument into a Lang.
func ParseLang(s string) (Lang, error) {
	if l := Lang(s); l.IsValid() {
		return l, nil
	}
	return "", fmt.Errorf("unknown language %q: stringly has rules for exactly these languages: %s; pass one of them",
		s, joinStrings(AllLangs))
}

// AstGrepLanguage returns the ast-grep `language:` of every rule written for
// l: "go", or "tsx" for TypeScript and JavaScript. It returns "" for a value
// that is not in AllLangs.
func (l Lang) AstGrepLanguage() string {
	switch l {
	case LangGo:
		return "go"
	case LangTS:
		return "tsx"
	}
	return ""
}

// FileGlobs returns the file-name globs a scan parses as l. A scan's ast-grep
// config maps them to AstGrepLanguage under `languageGlobs`, so .ts, .js,
// .mjs and .cjs files are parsed as TSX like .tsx files are; without that
// mapping the TS rules match nothing in them. It returns nil for a value that
// is not in AllLangs.
func (l Lang) FileGlobs() []string {
	switch l {
	case LangGo:
		return []string{"*.go"}
	case LangTS:
		return []string{"*.ts", "*.tsx", "*.js", "*.jsx", "*.mjs", "*.cjs"}
	}
	return nil
}

func joinStrings[T fmt.Stringer](values []T) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = v.String()
	}
	return strings.Join(parts, ", ")
}
