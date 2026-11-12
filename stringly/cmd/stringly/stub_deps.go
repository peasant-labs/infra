//go:build stringly_stub

// This file and stub_catalog.go stand in for the rules and catalog packages
// in the end-to-end test until those are wired in wiring.go. They are built
// only with the `stringly_stub` tag, never into a release.

package main

import (
	"embed"
	"fmt"
	"strings"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
	"github.com/peasant-labs/infra/stringly/internal/catalog"
	"github.com/peasant-labs/infra/stringly/internal/failure"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

//go:embed testdata/stub-rules/*.yml
var stubRuleFS embed.FS

func wiredDeps() deps {
	return deps{
		Registry:       newStubRegistry,
		Catalog:        func(astgrep.Runner) catalog.Builder { return stubBuilder{} },
		Generator:      stubGenerator{},
		DefinitionSite: stubDefinitionSite,
		Explain:        stubExplain,
		Failure:        func(error) (*failure.Error, bool) { return nil, false },
		DocumentRule:   rules.ParseRuleID,
		LanguageGlobs:  nil,
	}
}

type stubRegistry struct{ rules []rules.Rule }

func newStubRegistry(rules.Params) (rules.Registry, error) {
	entries, err := stubRuleFS.ReadDir("testdata/stub-rules")
	if err != nil {
		return nil, err
	}
	var reg stubRegistry
	for _, id := range rules.AllRuleIDs {
		for _, e := range entries {
			if e.Name() != id.String()+".yml" {
				continue
			}
			doc, err := stubRuleFS.ReadFile("testdata/stub-rules/" + e.Name())
			if err != nil {
				return nil, err
			}
			reg.rules = append(reg.rules, rules.Rule{ID: id, Document: doc, Message: rules.Message{
				What: fmt.Sprintf("a hit of %s.", id),
				Why:  "the literal restates a value a typed definition owns.",
				Fix:  "use the typed value.",
			}})
		}
	}
	return reg, nil
}

func (r stubRegistry) Rules() []rules.Rule { return r.rules }

func (r stubRegistry) Rule(id rules.RuleID) (rules.Rule, bool) {
	for _, x := range r.rules {
		if x.ID == id {
			return x, true
		}
	}
	return rules.Rule{}, false
}

type stubGenerator struct{}

// Generate builds a flat closed-set rule: a member literal as an operand of
// an equality comparison or as a case label.
func (stubGenerator) Generate(c catalog.Catalog) (rules.Rule, error) {
	var alts []string
	seen := map[string]bool{}
	for _, m := range c.Members {
		if !seen[m.Value] {
			seen[m.Value] = true
			alts = append(alts, regexpQuote(m.Value))
		}
	}
	values := strings.Join(alts, "|")
	if values == "" {
		values = `[^\s\S]`
	}
	var doc string
	id := rules.ClosedSetLiteral
	if c.Lang == rules.LangGo {
		id = rules.GoClosedSetLiteral
		doc = fmt.Sprintf(`id: %s
language: go
severity: warning
rule:
  kind: interpreted_string_literal
  regex: '^"(%s)"$'
  any:
    - inside: { kind: binary_expression, has: { field: operator, regex: '^(==|!=)$' } }
    - inside: { kind: expression_list, inside: { kind: expression_case } }
`, id, values)
	} else {
		doc = fmt.Sprintf(`id: %s
language: tsx
severity: warning
rule:
  kind: string
  regex: '^["'']+(%s)["'']+$'
  any:
    - inside: { kind: binary_expression, has: { field: operator, regex: '^(===|!==|==|!=)$' } }
    - inside: { kind: switch_case }
`, id, values)
	}
	return rules.Rule{ID: id, Document: []byte(doc), Message: rules.Message{
		What: "a closed-set member's wire value is written as a literal.",
		Why:  "a renamed or removed member would leave this comparison dead without a build failure.",
		Fix:  "compare with the set's member.",
	}}, nil
}

func regexpQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteRune('\\')
		}
		if r == '\'' {
			b.WriteString("''")
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func stubDefinitionSite(c catalog.Catalog, h rules.Hit) bool {
	v := literalValue(h.Text)
	for _, m := range c.Members {
		if m.Value == v && m.Site.Module == "" && m.Site.File == h.File {
			switch m.Source {
			case catalog.SourceClosedCall, catalog.SourceRecordTable, catalog.SourceGoTypedConstant:
				return true
			}
		}
	}
	return false
}

func stubExplain(c catalog.Catalog, value string) rules.Message {
	var sites []string
	for _, m := range c.Members {
		if m.Value == value {
			sites = append(sites, fmt.Sprintf("%s (%s, %s:%d)", m.Set, m.Source, m.Site.File, m.Site.Line))
		}
	}
	return rules.Message{
		What: "a closed-set member's wire value is written as a literal.",
		Why:  fmt.Sprintf("%q is a member of %s.", value, strings.Join(sites, " and of ")),
		Fix:  "compare with the set's member. This rule accepts no allow marker.",
	}
}
