package catalog

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// NewRuleGenerator returns the generator of the two closed-set rules.
func NewRuleGenerator() RuleGenerator { return generator{} }

type generator struct{}

// node is one ast-grep rule object.
type node = map[string]any

// Generate builds the catalog's closed-set rule: a literal whose text is one
// of the members, as an operand of a comparison or as a case label (and, in
// Go, as a key of a map literal keyed by a set's type). The rule is flat: it
// never reads the other operand's name. Its `not` clauses carry every
// rules.ExcludedOperand of the catalog's language, one util per exclusion
// named "excluded-<id>". Definition-site hits are dropped afterwards with
// Catalog.DefinitionSiteHit.
func (generator) Generate(c Catalog) (rules.Rule, error) {
	id := closedSetRule(c.Lang)
	if id == "" {
		return rules.Rule{}, fmt.Errorf("catalog: no closed-set rule for language %q; the catalog languages are %s", c.Lang, langList())
	}
	excluded, utils, err := exclusionUtils(c.Lang)
	if err != nil {
		return rules.Rule{}, err
	}
	msg := ruleMessage(c.Lang)
	doc := node{
		"id":       id.String(),
		"severity": "warning",
		"message":  msg.What,
		// The same document shape as the static rules: message is what,
		// metadata carries why and fix. ast-grep ignores metadata.
		"metadata": node{"why": msg.Why, "fix": msg.Fix},
		"utils":    utils,
	}
	values := c.Values()
	doc["language"] = c.Lang.AstGrepLanguage()
	switch c.Lang {
	case rules.LangTS:
		doc["rule"] = tsClosedSetRule(values, excluded)
	case rules.LangGo:
		doc["rule"] = goClosedSetRule(values, goSetTypeNames(c), excluded)
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return rules.Rule{}, fmt.Errorf("catalog: encode the generated %s rule: %w", id, err)
	}
	return rules.Rule{ID: id, Document: body, Message: msg}, nil
}

// exclusionUtils returns the util names of the language's exclusions, in
// rules.AllExcludedOperands order, and the utils themselves.
func exclusionUtils(lang rules.Lang) ([]string, node, error) {
	utils := node{}
	var names []string
	for _, e := range rules.AllExcludedOperands {
		if e.Lang() != lang {
			continue
		}
		shape, err := excludedOperandShape(e)
		if err != nil {
			return nil, nil, err
		}
		name := ExcludedOperandUtil(e)
		utils[name] = shape
		names = append(names, name)
	}
	return names, utils, nil
}

// ExcludedOperandUtil is the name of the util that matches an excluded
// operand in a generated rule document.
func ExcludedOperandUtil(e rules.ExcludedOperand) string { return "excluded-" + e.String() }

func hasProperty(re string) node {
	return node{"has": node{"field": "property", "regex": re}}
}

// getAttributeCall matches x.getAttribute(<first argument matching arg>).
func getAttributeCall(arg string) node {
	return node{
		"kind": "call_expression",
		"all": []any{
			node{"has": node{"field": "function", "kind": "member_expression", "has": node{"field": "property", "regex": "^getAttribute$"}}},
			node{"has": node{"field": "arguments", "has": node{"nthChild": 1, "kind": "string", "regex": arg}}},
		},
	}
}

const (
	ariaArgument = `^["']aria-`
	roleArgument = `^["']role["']$`
)

// excludedOperandShape is the operand each exclusion names (design section
// 6.3). Every exclusion must have a shape: a new ExcludedOperand without one
// fails generation, so it cannot ship untested.
func excludedOperandShape(e rules.ExcludedOperand) (node, error) {
	member := func(prop string) node {
		return node{"kind": "member_expression", "has": node{"field": "property", "regex": prop}}
	}
	switch e {
	case rules.ExcludedTypeof:
		return node{"kind": "unary_expression", "has": node{"field": "operator", "regex": "^typeof$"}}, nil
	case rules.ExcludedKey:
		return member("^key$"), nil
	case rules.ExcludedCode:
		return member("^code$"), nil
	case rules.ExcludedTagName:
		return member("^tagName$"), nil
	case rules.ExcludedNodeName:
		return member("^nodeName$"), nil
	case rules.ExcludedLocalName:
		return member("^localName$"), nil
	case rules.ExcludedAriaProperty:
		return member("^aria[A-Z]"), nil
	case rules.ExcludedGetAttributeAria:
		return getAttributeCall(ariaArgument), nil
	case rules.ExcludedGetAttributeRole:
		return getAttributeCall(roleArgument), nil
	case rules.ExcludedAwaitGetAttribute:
		awaited := node{
			"kind": "await_expression",
			"has": node{"any": []any{
				getAttributeCall(ariaArgument),
				getAttributeCall(roleArgument),
			}},
		}
		return node{"any": []any{
			awaited,
			node{"kind": "parenthesized_expression", "has": awaited},
		}}, nil
	case rules.ExcludedEventType:
		return node{
			"kind": "member_expression",
			"all": []any{
				node{"has": node{"field": "object", "kind": "identifier", "regex": "^(event|e|ev|evt)$"}},
				hasProperty("^type$"),
			},
		}, nil
	case rules.ExcludedGoRuntimeGOOS:
		return node{"kind": "selector_expression", "regex": `^runtime\.GOOS$`}, nil
	case rules.ExcludedGoRuntimeGOARCH:
		return node{"kind": "selector_expression", "regex": `^runtime\.GOARCH$`}, nil
	}
	return nil, fmt.Errorf("catalog: the excluded operand %q has no operand shape in the rule generator; "+
		"add its shape to excludedOperandShape together with its passes-excluded and flags-neighbour fixture cases", e)
}

// valueRegex matches the full text of a literal whose value is one of values,
// between any of the given quote characters.
func valueRegex(values []string, quotes string) string {
	if len(values) == 0 {
		// A literal's text always has its quotes, so this never matches.
		return "^$"
	}
	alts := make([]string, len(values))
	for i, v := range values {
		alts[i] = regexp.QuoteMeta(v)
	}
	q := "[" + quotes + "]"
	return "^" + q + "(?:" + strings.Join(alts, "|") + ")" + q + "$"
}

func notHas(names []string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = node{"not": node{"has": node{"matches": n}}}
	}
	return out
}

func notValueHas(names []string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = node{"not": node{"has": node{"field": "value", "has": node{"matches": n}}}}
	}
	return out
}

func notValueIs(names []string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = node{"not": node{"has": node{"field": "value", "matches": n}}}
	}
	return out
}

func tsClosedSetRule(values, excluded []string) node {
	comparison := append([]any{
		node{"has": node{"field": "operator", "regex": "^(===|!==|==|!=)$"}},
	}, notHas(excluded)...)
	switchStatement := node{"kind": "switch_statement"}
	if len(excluded) > 0 {
		switchStatement["all"] = notValueHas(excluded)
	}
	return node{
		"all": []any{
			node{"any": []any{
				node{"kind": "string"},
				node{"kind": "template_string", "not": node{"has": node{"kind": "template_substitution"}}},
			}},
			node{"regex": valueRegex(values, "\"'`")},
			node{"any": []any{
				node{"inside": node{"kind": "binary_expression", "all": comparison}},
				node{"inside": node{
					"kind":  "switch_case",
					"field": "value",
					"inside": node{
						"kind":   "switch_body",
						"inside": switchStatement,
					},
				}},
			}},
		},
	}
}

func goClosedSetRule(values, setTypes, excluded []string) node {
	comparison := append([]any{
		node{"has": node{"field": "operator", "regex": "^(==|!=)$"}},
	}, notHas(excluded)...)
	switchStatement := node{"kind": "expression_switch_statement"}
	if len(excluded) > 0 {
		switchStatement["all"] = notValueIs(excluded)
	}
	positions := []any{
		node{"inside": node{"kind": "binary_expression", "all": comparison}},
		// The literal is one of a case's expressions: the expression_list
		// fills the case's `value` field.
		node{"inside": node{
			"kind": "expression_list",
			"inside": node{
				"kind":   "expression_case",
				"field":  "value",
				"inside": switchStatement,
			},
		}},
	}
	if len(setTypes) > 0 {
		typeRe := "^(?:" + strings.Join(quoteAll(setTypes), "|") + ")$"
		// The literal is a key: its literal_element fills the keyed_element's
		// `key` field, inside a map literal keyed by a set's type.
		positions = append(positions, node{"inside": node{
			"kind": "literal_element",
			"inside": node{
				"kind":  "keyed_element",
				"field": "key",
				"inside": node{
					"kind": "literal_value",
					"inside": node{
						"kind": "composite_literal",
						"has": node{
							"field": "type",
							"kind":  "map_type",
							"has": node{
								"field": "key",
								"any": []any{
									node{"kind": "type_identifier", "regex": typeRe},
									node{"kind": "qualified_type", "has": node{"field": "name", "regex": typeRe}},
								},
							},
						},
					},
				},
			},
		}})
	}
	return node{
		"all": []any{
			node{"any": []any{
				node{"kind": "interpreted_string_literal"},
				node{"kind": "raw_string_literal"},
			}},
			node{"regex": valueRegex(values, "\"`")},
			node{"any": positions},
		},
	}
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = regexp.QuoteMeta(v)
	}
	return out
}

// goSetTypeNames returns the type names of the catalog's Go typed string
// sets, repository and schema, without the package: a map literal keyed by
// one of them is checked whatever import name qualifies it.
func goSetTypeNames(c Catalog) []string {
	seen := map[string]bool{}
	for _, m := range c.Members {
		if !isGoMember(m) {
			continue
		}
		if i := strings.LastIndexByte(m.Set, '.'); i >= 0 {
			seen[m.Set[i+1:]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// isGoMember reports whether the member is a Go typed constant, of the
// repository or of the schema module.
func isGoMember(m Member) bool {
	switch m.Source {
	case SourceGoTypedConstant:
		return true
	case SourceSchemaModule:
		return strings.HasSuffix(m.Site.File, ".go")
	}
	return false
}

// ruleMessage is the rule-level message; Catalog.Explain gives the
// per-value why and fix.
func ruleMessage(lang rules.Lang) rules.Message {
	const another = " If the value belongs to another vocabulary, define that vocabulary's own typed set next to its owner and use its member. This rule accepts no allow marker."
	if lang == rules.LangGo {
		return rules.Message{
			What: "a closed-set member's wire value is written as a literal in a comparison, a switch case or a key of a map keyed by the set's type.",
			Why:  "the literal equals a member that a Go typed string constant of this repository or of the pinned schema declares. A renamed or removed member would leave this branch dead without a build failure.",
			Fix:  "use the typed constant instead of its wire value." + another,
		}
	}
	return rules.Message{
		What: "a closed-set member's wire value is written as a literal in a comparison or a case label.",
		Why:  "the literal equals a member that a closed() set, a record table, a union type, a JSDoc union or the pinned schema declares. A renamed or removed member would leave this branch dead without a build failure.",
		Fix:  "compare with the typed member instead of its wire value; for a value only a union declares, convert the union to a closed() set next to its owner first." + another,
	}
}
