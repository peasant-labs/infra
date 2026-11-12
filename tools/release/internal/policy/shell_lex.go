package policy

import (
	"path"
	"strings"
)

// This file is the checker's shell lexer: it turns a run block into the
// simple commands bash would run, so the rules that look for a command (a
// ref write, a release tool invocation) find it in every form a workflow can
// spell it. It follows bash's quoting closely enough for that purpose:
//
//   - single quotes, double quotes, backslash escapes, and line
//     continuations;
//   - comments;
//   - the separators ; & | && || and newlines, and ( ) grouping;
//   - redirections, whose targets are not arguments;
//   - here-documents, whose bodies are data (an unquoted body still runs its
//     command substitutions);
//   - command substitutions $( ) and backticks, whose commands are commands
//     too;
//   - GitHub expressions ${{ }}, kept as one span;
//   - bash -c, sh -c, and eval, whose script argument is lexed again.
//
// A run block it cannot finish (an unterminated quote, substitution, or
// here-document) is reported as incomplete, so a caller can fail closed.

// shellWord is one word of a simple command: as written with its quotes, with
// the quotes removed (expansions left as written), and whether any part of it
// expands when the step runs. A ${{ }} expression, a $ expansion outside
// single quotes, and a command substitution expand.
type shellWord struct {
	raw     string
	bare    string
	expands bool
}

// shellCommand is one simple command: its words in order, redirections
// removed.
type shellCommand []shellWord

// bareWords returns the command's words with their quotes removed.
func (c shellCommand) bareWords() []string {
	out := make([]string, 0, len(c))
	for _, w := range c {
		out = append(out, w.bare)
	}
	return out
}

// maxShellNesting bounds how deep the lexer follows substitutions and
// re-lexed scripts, so a hostile run block cannot recurse without end. A
// deeper script is reported as incomplete.
const maxShellNesting = 8

// shellCommands lexes a run block into its simple commands, including the
// commands inside substitutions and inside bash -c, sh -c, and eval scripts.
// complete is false when some part of the text could not be lexed; the
// commands found so far are still returned.
func shellCommands(script string) ([]shellCommand, bool) {
	l := &shellLexer{src: script, complete: true}
	l.lex(0)
	cmds := l.cmds
	complete := l.complete
	for _, cmd := range l.cmds {
		inner, ok := scriptArguments(cmd, 0)
		cmds = append(cmds, inner...)
		complete = complete && ok
	}
	return cmds, complete
}

// shellInterpreters are the shells whose -c argument is a script.
var shellInterpreters = []string{"bash", "sh", "dash", "zsh", "ksh"}

// scriptArguments lexes the script a command hands to a shell: the argument
// after -c (alone or in a flag cluster) of a shell interpreter, or the joined
// arguments of eval.
func scriptArguments(cmd shellCommand, depth int) ([]shellCommand, bool) {
	if depth >= maxShellNesting {
		return nil, false
	}
	for i, w := range cmd {
		name := path.Base(w.bare)
		var script string
		found := false
		switch {
		case name == "eval":
			script = strings.Join(shellCommand(cmd[i+1:]).bareWords(), " ")
			found = true
		case containsString(shellInterpreters, name):
			for j := i + 1; j < len(cmd); j++ {
				flag := cmd[j].bare
				if !strings.HasPrefix(flag, "-") || strings.HasPrefix(flag, "--") {
					break
				}
				if strings.Contains(flag, "c") && j+1 < len(cmd) {
					script = cmd[j+1].bare
					found = true
					break
				}
			}
		}
		if !found {
			continue
		}
		l := &shellLexer{src: script, complete: true, depth: depth + 1}
		l.lex(0)
		out := l.cmds
		ok := l.complete
		for _, inner := range l.cmds {
			nested, nestedOK := scriptArguments(inner, depth+1)
			out = append(out, nested...)
			ok = ok && nestedOK
		}
		return out, ok
	}
	return nil, true
}

func containsString(set []string, s string) bool {
	for _, member := range set {
		if member == s {
			return true
		}
	}
	return false
}

// heredoc is one here-document waiting for the end of its command line.
type heredoc struct {
	delimiter string
	stripTabs bool
	literal   bool
}

// shellLexer holds one lexing pass over a script.
type shellLexer struct {
	src      string
	i        int
	depth    int
	cmds     []shellCommand
	cur      shellCommand
	raw      strings.Builder
	bare     strings.Builder
	inWord   bool
	expands  bool
	dropNext bool
	// delimNext marks that the next word is a here-document delimiter.
	delimNext  *heredoc
	pending    []heredoc
	complete   bool
	parenDepth int
}

func (l *shellLexer) endWord() {
	if !l.inWord {
		return
	}
	w := shellWord{raw: l.raw.String(), bare: l.bare.String(), expands: l.expands}
	l.raw.Reset()
	l.bare.Reset()
	l.inWord = false
	l.expands = false
	switch {
	case l.delimNext != nil:
		doc := *l.delimNext
		doc.delimiter = w.bare
		doc.literal = strings.ContainsAny(w.raw, `'"\`)
		l.pending = append(l.pending, doc)
		l.delimNext = nil
	case l.dropNext:
		l.dropNext = false
	default:
		l.cur = append(l.cur, w)
	}
}

func (l *shellLexer) endCommand() {
	l.endWord()
	if len(l.cur) > 0 {
		l.cmds = append(l.cmds, l.cur)
	}
	l.cur = nil
}

func (l *shellLexer) add(raw, bare string) {
	l.raw.WriteString(raw)
	l.bare.WriteString(bare)
	l.inWord = true
}

// lex runs from l.i to the end of the source. With stopAtParen set it stops
// after the ) that closes a command substitution and returns true; reaching
// the end first marks the lex incomplete.
func (l *shellLexer) lex(stopAtParen int) bool {
	src := l.src
	for l.i < len(src) {
		c := src[l.i]
		switch {
		case c == '\\':
			if l.i+1 >= len(src) {
				l.add(`\`, "")
				l.i++
				continue
			}
			if src[l.i+1] == '\n' {
				l.i += 2
				continue
			}
			l.add(src[l.i:l.i+2], src[l.i+1:l.i+2])
			l.i += 2
		case c == ' ' || c == '\t' || c == '\r':
			l.endWord()
			l.i++
		case c == '\n':
			l.endCommand()
			l.i++
			l.readHeredocs()
		case c == '#' && !l.inWord:
			for l.i < len(src) && src[l.i] != '\n' {
				l.i++
			}
		case c == '\'':
			end := strings.IndexByte(src[l.i+1:], '\'')
			if end < 0 {
				l.complete = false
				l.add(src[l.i:], src[l.i+1:])
				l.i = len(src)
				continue
			}
			end += l.i + 1
			l.add(src[l.i:end+1], src[l.i+1:end])
			l.i = end + 1
		case c == '"':
			start := l.i
			l.i++
			bare, ok := l.doubleQuoted(true)
			if !ok {
				l.complete = false
			}
			l.add(src[start:l.i], bare)
		case c == '$':
			start := l.i
			bare := l.dollar()
			l.add(src[start:l.i], bare)
		case c == '`':
			start := l.i
			l.backtick()
			l.add(src[start:l.i], src[start:l.i])
			l.expands = true
		case c == ';' || c == '&' || c == '|':
			l.endCommand()
			l.i++
			if l.i < len(src) && (src[l.i] == c || (c == '|' && src[l.i] == '&') || (c == ';' && src[l.i] == '&')) {
				l.i++
			}
		case c == '(':
			l.endCommand()
			l.parenDepth++
			l.i++
		case c == ')':
			l.endCommand()
			l.i++
			if l.parenDepth == 0 && stopAtParen > 0 {
				return true
			}
			if l.parenDepth > 0 {
				l.parenDepth--
			}
		case c == '<' || c == '>':
			l.redirect()
		default:
			l.add(src[l.i:l.i+1], src[l.i:l.i+1])
			l.i++
		}
	}
	l.endCommand()
	if len(l.pending) > 0 {
		l.complete = false
	}
	if stopAtParen > 0 {
		l.complete = false
	}
	return false
}

// doubleQuoted reads a double-quoted span from l.i (after the opening quote)
// through its closing quote and returns the span's text with the quoting
// removed. With quoted false it reads a here-document body to the end of the
// source instead, where only expansions are special.
func (l *shellLexer) doubleQuoted(quoted bool) (string, bool) {
	src := l.src
	var bare strings.Builder
	for l.i < len(src) {
		c := src[l.i]
		switch {
		case quoted && c == '"':
			l.i++
			return bare.String(), true
		case c == '\\' && l.i+1 < len(src):
			next := src[l.i+1]
			switch {
			case next == '\n':
			case next == '$' || next == '`' || next == '\\' || (quoted && next == '"'):
				bare.WriteByte(next)
			default:
				bare.WriteByte('\\')
				bare.WriteByte(next)
			}
			l.i += 2
		case c == '$':
			bare.WriteString(l.dollar())
			l.expands = true
		case c == '`':
			start := l.i
			l.backtick()
			bare.WriteString(src[start:l.i])
			l.expands = true
		default:
			bare.WriteByte(c)
			l.i++
		}
	}
	return bare.String(), !quoted
}

// dollar reads one expansion at l.i: a GitHub expression, an arithmetic
// expansion, a command substitution (whose commands join the lexer's own), a
// braced parameter, or a plain $. It returns the expansion as written.
func (l *shellLexer) dollar() string {
	src := l.src
	start := l.i
	l.expands = true
	switch {
	case strings.HasPrefix(src[l.i:], "${{"):
		end := strings.Index(src[l.i:], "}}")
		if end < 0 {
			l.complete = false
			l.i = len(src)
		} else {
			l.i += end + 2
		}
	case strings.HasPrefix(src[l.i:], "$(("):
		depth := 0
		l.i++
		for l.i < len(src) {
			switch src[l.i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			l.i++
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			l.complete = false
		}
	case strings.HasPrefix(src[l.i:], "$("):
		l.substitute(l.i+2, func(sub *shellLexer) { sub.lex(1) })
	case strings.HasPrefix(src[l.i:], "${"):
		depth := 0
		l.i++
		for l.i < len(src) {
			switch src[l.i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			l.i++
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			l.complete = false
		}
		// A default or alternate value can hold a substitution; its
		// commands are commands too.
		if inner := src[start:l.i]; depth == 0 && len(inner) >= 3 && (strings.Contains(inner, "$(") || strings.Contains(inner, "`")) {
			l.relex(inner[2 : len(inner)-1])
		}
	default:
		l.i++
	}
	return src[start:l.i]
}

// substitute lexes a command substitution body starting at from with a
// nested lexer that shares this lexer's command list, and moves l.i past it.
func (l *shellLexer) substitute(from int, run func(sub *shellLexer)) {
	if l.depth+1 >= maxShellNesting {
		l.complete = false
		l.i = len(l.src)
		return
	}
	sub := &shellLexer{src: l.src, i: from, complete: true, depth: l.depth + 1}
	run(sub)
	l.cmds = append(l.cmds, sub.cmds...)
	l.complete = l.complete && sub.complete
	l.i = sub.i
}

// relex lexes text as a separate script and adds its commands.
func (l *shellLexer) relex(text string) {
	if l.depth+1 >= maxShellNesting {
		l.complete = false
		return
	}
	sub := &shellLexer{src: text, complete: true, depth: l.depth + 1}
	sub.lex(0)
	l.cmds = append(l.cmds, sub.cmds...)
	l.complete = l.complete && sub.complete
}

// backtick reads a backtick substitution at l.i, lexes its body, and moves
// l.i past the closing backtick.
func (l *shellLexer) backtick() {
	src := l.src
	var body strings.Builder
	j := l.i + 1
	for j < len(src) && src[j] != '`' {
		if src[j] == '\\' && j+1 < len(src) {
			body.WriteByte(src[j+1])
			j += 2
			continue
		}
		body.WriteByte(src[j])
		j++
	}
	if j >= len(src) {
		l.complete = false
		l.i = len(src)
	} else {
		l.i = j + 1
	}
	l.relex(body.String())
}

// redirect reads one redirection operator at l.i. A file-descriptor number
// written just before it is part of the operator, and the word after it is
// its target, not an argument; a here-document's word is its delimiter.
func (l *shellLexer) redirect() {
	src := l.src
	if l.inWord && isDigits(l.bare.String()) && !l.expands {
		l.raw.Reset()
		l.bare.Reset()
		l.inWord = false
	} else {
		l.endWord()
	}
	switch {
	case strings.HasPrefix(src[l.i:], "<<<"):
		l.i += 3
		l.dropNext = true
		return
	case strings.HasPrefix(src[l.i:], "<<"):
		l.i += 2
		doc := heredoc{}
		if l.i < len(src) && src[l.i] == '-' {
			doc.stripTabs = true
			l.i++
		}
		l.delimNext = &doc
		return
	}
	for l.i < len(src) && (src[l.i] == '<' || src[l.i] == '>' || src[l.i] == '|') {
		l.i++
	}
	if l.i < len(src) && src[l.i] == '&' {
		l.i++
		target := l.i
		for l.i < len(src) && (src[l.i] == '-' || (src[l.i] >= '0' && src[l.i] <= '9')) {
			l.i++
		}
		if l.i > target {
			return
		}
	}
	l.dropNext = true
}

// readHeredocs skips the bodies of the here-documents opened on the line
// that just ended. An unquoted body still expands, so its command
// substitutions are lexed; the rest of the body is data.
func (l *shellLexer) readHeredocs() {
	for _, doc := range l.pending {
		start := l.i
		end := -1
		next := len(l.src)
		for pos := l.i; pos < len(l.src); {
			lineEnd := strings.IndexByte(l.src[pos:], '\n')
			line := l.src[pos:]
			after := len(l.src)
			if lineEnd >= 0 {
				line = l.src[pos : pos+lineEnd]
				after = pos + lineEnd + 1
			}
			check := line
			if doc.stripTabs {
				check = strings.TrimLeft(check, "\t")
			}
			if check == doc.delimiter {
				end = pos
				next = after
				break
			}
			pos = after
		}
		if end < 0 {
			l.complete = false
			end = len(l.src)
		}
		if !doc.literal {
			body := &shellLexer{src: l.src[start:end], complete: true, depth: l.depth + 1}
			body.doubleQuoted(false)
			l.cmds = append(l.cmds, body.cmds...)
			l.complete = l.complete && body.complete
		}
		l.i = next
	}
	l.pending = nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// commandWord returns the index of the word a command runs: the first word
// after leading variable assignments and the wrappers that run their
// arguments as a command (env, command, exec, time, nohup, nice, sudo, and
// their flags). It returns -1 for a command that only assigns.
func commandWord(cmd shellCommand) int {
	for i := 0; i < len(cmd); i++ {
		w := cmd[i].bare
		switch {
		case isAssignment(w):
			continue
		case containsString(commandWrappers, path.Base(w)):
			for i+1 < len(cmd) && strings.HasPrefix(cmd[i+1].bare, "-") {
				i++
			}
			continue
		}
		return i
	}
	return -1
}

// commandWrappers run their remaining arguments as a command.
var commandWrappers = []string{"env", "command", "exec", "time", "nohup", "nice", "sudo"}

// isAssignment reports whether a word is a shell variable assignment.
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	return ok && shellName.MatchString(name)
}
