package catalog

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// literalValue returns the value of a string literal as written in source of
// the given language: a TS or JS string or template without substitution, or
// a Go interpreted or raw string. It reports false for anything else.
func literalValue(lang rules.Lang, text string) (string, bool) {
	switch lang {
	case rules.LangGo:
		v, err := strconv.Unquote(text)
		return v, err == nil
	case rules.LangTS:
		return unquoteJS(text)
	}
	return "", false
}

// unquoteJS decodes a JS string literal ('…' or "…") or a template literal
// with no substitution (`…`).
func unquoteJS(text string) (string, bool) {
	if len(text) < 2 {
		return "", false
	}
	q := text[0]
	if (q != '\'' && q != '"' && q != '`') || text[len(text)-1] != q {
		return "", false
	}
	body := text[1 : len(text)-1]
	if q == '`' && strings.Contains(body, "${") {
		return "", false
	}
	if !strings.ContainsRune(body, '\\') {
		return body, true
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(body) {
			return "", false
		}
		switch e := body[i]; e {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '0':
			b.WriteByte(0)
		case '\n':
			// A line continuation adds nothing.
		case 'x':
			if i+3 > len(body) {
				return "", false
			}
			n, err := strconv.ParseUint(body[i+1:i+3], 16, 8)
			if err != nil {
				return "", false
			}
			b.WriteRune(rune(n))
			i += 2
		case 'u':
			var hex string
			if i+1 < len(body) && body[i+1] == '{' {
				end := strings.IndexByte(body[i+2:], '}')
				if end < 0 {
					return "", false
				}
				hex = body[i+2 : i+2+end]
				i += 2 + end
			} else {
				if i+5 > len(body) {
					return "", false
				}
				hex = body[i+1 : i+5]
				i += 4
			}
			n, err := strconv.ParseUint(hex, 16, 32)
			if err != nil || !utf8.ValidRune(rune(n)) {
				return "", false
			}
			b.WriteRune(rune(n))
		default:
			b.WriteByte(e)
		}
	}
	return b.String(), true
}
