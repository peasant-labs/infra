package catalog

import (
	"fmt"
	"strings"

	"github.com/peasant-labs/infra/stringly/internal/rules"
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
	if typed, ok := firstTyped(members, c.Lang); ok {
		msg.Fix = "use the member " + memberReference(typed) + " instead of the literal." + another
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

// firstTyped returns the first member that is a typed value a developer can
// use directly: a closed() member, a record-table key, a Go constant, or a
// schema Go constant.
func firstTyped(members []Member, lang rules.Lang) (Member, bool) {
	for _, m := range members {
		switch m.Source {
		case SourceClosedCall, SourceRecordTable, SourceGoTypedConstant:
			return m, true
		case SourceSchemaModule:
			if isGoMember(m) || lang == rules.LangTS {
				return m, true
			}
		}
	}
	return Member{}, false
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
