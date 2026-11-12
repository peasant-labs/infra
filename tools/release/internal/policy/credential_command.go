package policy

import (
	"strconv"
	"strings"
)

// This file reads the run block of a step that receives a credential. Such a
// step runs exactly one command, so the reader does not approximate a shell:
// it splits one command into words and refuses everything else. A run block
// is accepted only when every character belongs to a form listed here:
//
//   - unquoted literal characters from a small allowlist (letters, digits,
//     and _ - . / : = @ + , %);
//   - single-quoted text, which the shell never expands;
//   - double-quoted text, whose only expansions are a plain parameter ($NAME
//     or ${NAME}) and a GitHub expression (${{ ... }});
//   - an unquoted GitHub expression;
//   - a backslash before a newline, which joins two lines;
//   - one plain '>' redirection, which a template may allow for its output.
//
// Anything else is refused: a second command, a pipe, a separator, a
// substitution, a here-document, a glob, a brace or tilde expansion, an
// unquoted parameter, any other '$' form, any other escape, a comment. A
// refused block cannot be one template, so it never receives a credential.

// partKind is what one part of a command word is.
type partKind int

const (
	// partLiteral is text the shell passes as written.
	partLiteral partKind = iota + 1
	// partParam is a plain parameter expansion inside double quotes.
	partParam
	// partExpr is a GitHub expression, substituted before the shell runs.
	partExpr
)

// wordPart is one part of a command word: its kind and its text (the
// literal text, the parameter name, or the expression body trimmed of
// spaces).
type wordPart struct {
	kind partKind
	text string
}

// templateWord is one word of the command, or the '>' redirection operator.
type templateWord struct {
	raw      string
	parts    []wordPart
	redirect bool
}

// literal returns the word's text when every part is literal.
func (w templateWord) literal() (string, bool) {
	if w.redirect {
		return "", false
	}
	var b strings.Builder
	for _, p := range w.parts {
		if p.kind != partLiteral {
			return "", false
		}
		b.WriteString(p.text)
	}
	return b.String(), true
}

// literalChars are the characters a word may hold outside quotes.
const literalChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-./:=@+,%"

// commandTokenizer holds one pass over a run block.
type commandTokenizer struct {
	src   string
	i     int
	words []templateWord
	raw   strings.Builder
	parts []wordPart
	open  bool
}

// tokenizeCommand splits a credential step's run block into the words of
// its one command. It returns why the block is refused, or "" when every
// character belongs to an accepted form.
func tokenizeCommand(run string) ([]templateWord, string) {
	t := &commandTokenizer{src: strings.TrimSpace(run)}
	if why := t.run(); why != "" {
		return nil, why
	}
	if len(t.words) == 0 {
		return nil, "the run block holds no command"
	}
	return t.words, ""
}

func (t *commandTokenizer) run() string {
	src := t.src
	for t.i < len(src) {
		c := src[t.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			t.end()
			t.i++
		case c == '\\':
			if t.i+1 < len(src) && src[t.i+1] == '\n' {
				t.end()
				t.i += 2
				continue
			}
			return "it escapes a character with a backslash, which no template needs"
		case c == '\n':
			return "it runs more than one line, so it is more than one command"
		case c == '\'':
			end := strings.IndexByte(src[t.i+1:], '\'')
			if end < 0 {
				return "it opens a single quote it never closes"
			}
			end += t.i + 1
			t.add(src[t.i:end+1], wordPart{partLiteral, src[t.i+1 : end]})
			t.i = end + 1
		case c == '"':
			if why := t.doubleQuoted(); why != "" {
				return why
			}
		case c == '$':
			if !strings.HasPrefix(src[t.i:], "${{") {
				return "it uses '$' outside double quotes (a parameter, a substitution or arithmetic), which can run a command or split into more words"
			}
			start := t.i
			part, why := t.expression()
			if why != "" {
				return why
			}
			t.add(src[start:t.i], part)
		case c == '>':
			if t.open {
				return "it redirects a numbered file descriptor"
			}
			if t.i+1 < len(src) && strings.IndexByte(">&|", src[t.i+1]) >= 0 {
				return "it uses a redirection other than one plain '>'"
			}
			t.words = append(t.words, templateWord{raw: ">", redirect: true})
			t.i++
		case strings.IndexByte(literalChars, c) >= 0:
			t.add(src[t.i:t.i+1], wordPart{partLiteral, src[t.i : t.i+1]})
			t.i++
		default:
			return "it holds " + quote(string(c)) + " outside quotes, which is shell syntax no template uses (a separator, a pipe, a substitution, a glob, a brace, a comment)"
		}
	}
	t.end()
	return ""
}

// doubleQuoted reads a double-quoted span at t.i through its closing quote.
func (t *commandTokenizer) doubleQuoted() string {
	src := t.src
	start := t.i
	t.i++
	for t.i < len(src) {
		c := src[t.i]
		switch {
		case c == '"':
			t.i++
			t.raw.WriteString(src[start:t.i])
			t.open = true
			return ""
		case c == '\\':
			if t.i+1 >= len(src) {
				return "it ends inside a double quote"
			}
			next := src[t.i+1]
			switch next {
			case '\n':
			case '$', '`', '"', '\\':
				t.addPart(wordPart{partLiteral, string(next)})
			default:
				t.addPart(wordPart{partLiteral, src[t.i : t.i+2]})
			}
			t.i += 2
		case c == '`':
			return "it runs a backtick substitution"
		case c == '$':
			if strings.HasPrefix(src[t.i:], "${{") {
				part, why := t.expression()
				if why != "" {
					return why
				}
				t.addPart(part)
				continue
			}
			name, why := t.parameter()
			if why != "" {
				return why
			}
			t.addPart(wordPart{partParam, name})
		default:
			t.addPart(wordPart{partLiteral, src[t.i : t.i+1]})
			t.i++
		}
	}
	return "it opens a double quote it never closes"
}

// expression reads a GitHub expression at t.i.
func (t *commandTokenizer) expression() (wordPart, string) {
	end := strings.Index(t.src[t.i:], "}}")
	if end < 0 {
		return wordPart{}, "it opens a GitHub expression it never closes"
	}
	body := strings.TrimSpace(t.src[t.i+3 : t.i+end])
	t.i += end + 2
	return wordPart{partExpr, body}, ""
}

// parameter reads $NAME or ${NAME} at t.i. Every other form after '$' runs a
// command or rewrites the value, so it is refused.
func (t *commandTokenizer) parameter() (string, string) {
	src := t.src
	j := t.i + 1
	braced := j < len(src) && src[j] == '{'
	if braced {
		j++
	}
	k := j
	for k < len(src) && isNameChar(src[k], k == j) {
		k++
	}
	if k == j {
		return "", "it uses a '$' form other than $NAME, ${NAME} or a GitHub expression (a substitution, arithmetic, or a special parameter)"
	}
	name := src[j:k]
	if braced {
		if k >= len(src) || src[k] != '}' {
			return "", "it rewrites parameter " + name + " with an operator inside ${...}"
		}
		k++
	}
	t.i = k
	return name, ""
}

func isNameChar(c byte, first bool) bool {
	switch {
	case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return !first
	}
	return false
}

// add appends raw text and one part to the current word.
func (t *commandTokenizer) add(raw string, part wordPart) {
	t.raw.WriteString(raw)
	t.addPart(part)
}

// addPart appends one part, merging adjacent literal parts.
func (t *commandTokenizer) addPart(part wordPart) {
	t.open = true
	if n := len(t.parts); n > 0 && part.kind == partLiteral && t.parts[n-1].kind == partLiteral {
		t.parts[n-1].text += part.text
		return
	}
	t.parts = append(t.parts, part)
}

// end closes the current word.
func (t *commandTokenizer) end() {
	if !t.open {
		return
	}
	t.words = append(t.words, templateWord{raw: t.raw.String(), parts: t.parts})
	t.raw.Reset()
	t.parts = nil
	t.open = false
}

// quote renders a value for a message, escaped so a quote inside it stays
// readable.
func quote(s string) string { return strconv.Quote(s) }
