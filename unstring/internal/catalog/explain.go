package catalog

import (
	"fmt"
	"path"
	"strings"

	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// maxNamedSites is how many declaring sites a message names before it
// summarises the rest.
const maxNamedSites = 3

// Explain returns the message parts for a hit of the catalog's closed-set rule
// whose literal is value: why names every set that declares the value, and
// fix depends on where it is declared (design section 6.6). It falls back to
// the rule-level message for a value the catalog does not hold.
func (c Catalog) Explain(value string) rules.Message {
	msg := ruleMessage(c.Lang)
	members := c.Lookup(value)
	if len(members) == 0 {
		return msg
	}
	var sites []string
	for i, m := range members {
		if i == maxNamedSites {
			sites = append(sites, fmt.Sprintf("%d more", len(members)-maxNamedSites))
			break
		}
		sites = append(sites, describeMember(m))
	}
	msg.Why = fmt.Sprintf("%q is a member of %s. A renamed or removed member would leave this branch dead without a build failure.",
		value, strings.Join(sites, ", "))

	const another = " If this value belongs to another vocabulary, define that vocabulary's own typed set next to its owner and use its member. This rule accepts no allow marker."
	if ref, ok := c.firstTypedReference(members); ok {
		msg.Fix = "use the member " + ref + " instead of the literal." + another
		return msg
	}
	for _, m := range members {
		if m.Source != SourceSchemaModule {
			continue
		}
		set := m.Set
		if set == "" {
			set = "the unnamed schema set"
		}
		if c.Lang == rules.LangTS {
			msg.Fix = "no TypeScript member was found for " + set + " from " + m.Site.Module +
				"; use or define a typed set for this vocabulary next to its owner." + another
		} else {
			msg.Fix = "no Go constant was found for " + set + " from " + m.Site.Module +
				"; use or define a typed set for this vocabulary next to its owner." + another
		}
		return msg
	}
	switch members[0].Source {
	case SourceJSDocUnion:
		msg.Fix = "declare the set with closed() from src/ui/closed-value.js, or as a typed frozen table, next to its owner, and compare with its members." + another
	default:
		msg.Fix = "convert the union to a closed() set next to its owner and compare with its members; the type becomes (typeof X)[keyof typeof X]." + another
	}
	return msg
}

// firstTypedReference only suggests values available in the language being
// scanned. A schema Go name is never a TypeScript import, and a TS union is
// only a type, not a runtime member.
func (c Catalog) firstTypedReference(members []Member) (string, bool) {
	for _, m := range members {
		switch m.Source {
		case SourceClosedCall, SourceRecordTable:
			if c.Lang == rules.LangTS {
				return memberReference(m), true
			}
		case SourceGoTypedConstant:
			if c.Lang == rules.LangGo {
				return memberReference(m), true
			}
		case SourceSchemaModule:
			if c.Lang == rules.LangGo && isGoMember(m) {
				return memberReference(m), true
			}
			if c.Lang == rules.LangTS {
				set := m.Set
				if isGoMember(m) {
					// Subpackage type names need not identify the same TS set.
					if path.Dir(m.Site.File) != "." {
						continue
					}
					if i := strings.LastIndexByte(set, '.'); i >= 0 {
						set = set[i+1:]
					}
				}
				if ref := c.schemaTSRefs[schemaTSKey{set: set, value: m.Value}]; ref != "" {
					return ref + " from \"" + SchemaPackage + "\"", true
				}
			}
		}
	}
	return "", false
}

func describeMember(m Member) string {
	where := m.Site.File + ":" + fmt.Sprint(m.Site.Line)
	if m.Site.Module != "" {
		where = m.Site.Module
	}
	set := m.Set
	switch m.Source {
	case SourceTSUnion, SourceJSDocUnion:
		if set == "" {
			return "an inline union (" + where + ")"
		}
		return "the union " + set + " (" + where + ")"
	}
	if set == "" {
		set = "a " + m.Source.String() + " set"
	}
	return set + " (" + where + ")"
}

func memberReference(m Member) string {
	ref := m.Set
	if m.Name != "" {
		if i := strings.LastIndexByte(ref, '.'); i >= 0 && isGoMember(m) {
			ref = ref[:i+1] + m.Name
		} else if ref != "" {
			ref += "." + m.Name
		} else {
			ref = m.Name
		}
	}
	if ref == "" {
		ref = "that declares it"
	}
	switch {
	case m.Site.Module != "":
		return ref + " from " + m.Site.Module
	default:
		return ref + " (" + m.Site.File + ")"
	}
}
