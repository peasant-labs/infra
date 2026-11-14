package catalog

import (
	"regexp"
	"strings"
)

// jsdocTag finds a JSDoc tag that carries a type in braces.
var jsdocTag = regexp.MustCompile(`@(typedef|type|param|arg|argument|property|prop|returns|return)\s*\{`)

// jsdocTypedefName reads the name after a @typedef's closing brace.
var jsdocTypedefName = regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)`)

// jsdocMembers returns the union members in one JSDoc comment: inside one {…}
// of a tag, two or more quoted strings joined by `|`. A @typedef's union is
// named by the typedef; any other union has no set name. site is the
// comment's own site; each member gets the line it is written on.
func jsdocMembers(comment string, site DefinitionSite) []Member {
	var out []Member
	for _, loc := range jsdocTag.FindAllStringSubmatchIndex(comment, -1) {
		open := loc[1] - 1
		end := matchingBrace(comment, open)
		if end < 0 {
			continue
		}
		set := ""
		if comment[loc[2]:loc[3]] == "typedef" {
			if m := jsdocTypedefName.FindStringSubmatch(comment[end+1:]); m != nil {
				set = m[1]
			}
		}
		for _, lit := range unionLiterals(comment[open+1 : end]) {
			offset := open + 1 + lit.offset
			out = append(out, Member{
				Value:  lit.value,
				Set:    set,
				Source: SourceJSDocUnion,
				Site: DefinitionSite{
					File: site.File,
					Line: site.Line + strings.Count(comment[:offset], "\n"),
				},
			})
		}
	}
	return out
}

// matchingBrace returns the index of the brace that closes the one at open,
// or -1.
func matchingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

type unionLiteral struct {
	value  string
	offset int
}

// unionLiterals returns the quoted strings in a JSDoc type expression that are
// joined to a neighbour by `|`, when there are two or more of them.
func unionLiterals(typ string) []unionLiteral {
	type token struct {
		quoted bool
		pipe   bool
		value  string
		offset int
	}
	var toks []token
	for i := 0; i < len(typ); i++ {
		c := typ[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(typ) && typ[j] != c && typ[j] != '\n' {
				if typ[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(typ) || typ[j] != c {
				return nil
			}
			value, ok := unquoteJS(typ[i : j+1])
			if !ok {
				return nil
			}
			toks = append(toks, token{quoted: true, value: value, offset: i})
			i = j
		case c == '|':
			toks = append(toks, token{pipe: true})
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '*' || c == '(' || c == ')':
			// Layout, the comment's line prefixes and grouping do not separate
			// a member from its `|`.
		default:
			toks = append(toks, token{})
		}
	}
	var out []unionLiteral
	for i, t := range toks {
		if !t.quoted || t.value == "" {
			continue
		}
		before := i > 0 && toks[i-1].pipe
		after := i+1 < len(toks) && toks[i+1].pipe
		if before || after {
			out = append(out, unionLiteral{value: t.value, offset: t.offset})
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}
