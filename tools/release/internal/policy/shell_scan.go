package policy

import (
	"path"
	"strings"
)

// This file finds the simple commands of a general run block for the
// tool-calls-match-the-cli rule, which reports a release tool invocation the
// tool would refuse at run time. It is a usability check, not a security
// boundary: the credential rules never read general run blocks, because a
// step without a credential has nothing to write a ref with. So the scanner
// follows only the shell forms a tool invocation is written in: quotes,
// escapes and line continuations, comments, the separators ; & | && || and
// newlines, ( ) grouping, command substitutions $( ) and backticks (whose
// commands are commands too), GitHub expressions, redirections, and
// here-documents, whose bodies it skips.

// shellWord is one word of a simple command: as written, and with its
// quotes removed (expansions left as written).
type shellWord struct {
	raw  string
	bare string
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

// shellScanner holds one pass over a run block, or over one substitution
// inside it.
type shellScanner struct {
	src       string
	i         int
	cmds      []shellCommand
	cur       shellCommand
	raw, bare strings.Builder
	inWord    bool
	dropNext  bool
	heredocs  []string
	delimNext bool
}

// scanCommands returns the simple commands of a run block, the commands of
// its substitutions included, in the order their text closes.
func scanCommands(src string) []shellCommand {
	s := &shellScanner{src: src}
	s.scan(0)
	return s.cmds
}

func (s *shellScanner) add(raw, bare string) {
	s.raw.WriteString(raw)
	s.bare.WriteString(bare)
	s.inWord = true
}

func (s *shellScanner) endWord() {
	if !s.inWord {
		return
	}
	w := shellWord{raw: s.raw.String(), bare: s.bare.String()}
	s.raw.Reset()
	s.bare.Reset()
	s.inWord = false
	switch {
	case s.delimNext:
		s.heredocs = append(s.heredocs, w.bare)
		s.delimNext = false
	case s.dropNext:
		s.dropNext = false
	default:
		s.cur = append(s.cur, w)
	}
}

func (s *shellScanner) endCommand() {
	s.endWord()
	if len(s.cur) > 0 {
		s.cmds = append(s.cmds, s.cur)
	}
	s.cur = nil
}

// scan reads to the end of the source, or past the closing byte of a
// substitution when stop is ')' or '`'.
func (s *shellScanner) scan(stop byte) {
	src := s.src
	for s.i < len(src) {
		c := src[s.i]
		switch {
		case stop != 0 && c == stop:
			s.i++
			s.endCommand()
			return
		case c == '\\':
			if s.i+1 < len(src) && src[s.i+1] != '\n' {
				s.add(src[s.i:s.i+2], src[s.i+1:s.i+2])
			}
			s.i += 2
		case c == ' ' || c == '\t' || c == '\r':
			s.endWord()
			s.i++
		case c == '\n':
			s.endCommand()
			s.i++
			s.skipHeredocs()
		case c == '#' && !s.inWord:
			for s.i < len(src) && src[s.i] != '\n' {
				s.i++
			}
		case c == '\'':
			end := strings.IndexByte(src[s.i+1:], '\'')
			if end < 0 {
				end = len(src) - s.i - 1
			}
			stopAt := s.i + 1 + end
			s.add(src[s.i:min(stopAt+1, len(src))], src[s.i+1:stopAt])
			s.i = stopAt + 1
		case c == '"':
			s.doubleQuoted()
		case c == '$' || c == '`':
			s.expansion()
		case c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			s.endCommand()
			s.i++
		case c == '<' || c == '>':
			s.endWord()
			if strings.HasPrefix(src[s.i:], "<<") && !strings.HasPrefix(src[s.i:], "<<<") {
				s.i += 2
				if s.i < len(src) && src[s.i] == '-' {
					s.i++
				}
				s.delimNext = true
				continue
			}
			for s.i < len(src) && strings.IndexByte("<>&|0123456789-", src[s.i]) >= 0 {
				s.i++
			}
			s.dropNext = true
		default:
			s.add(src[s.i:s.i+1], src[s.i:s.i+1])
			s.i++
		}
	}
	s.endCommand()
}

// doubleQuoted reads a double-quoted span at s.i into the current word.
func (s *shellScanner) doubleQuoted() {
	src := s.src
	start := s.i
	s.i++
	var bare strings.Builder
	for s.i < len(src) && src[s.i] != '"' {
		switch c := src[s.i]; {
		case c == '\\' && s.i+1 < len(src):
			bare.WriteByte(src[s.i+1])
			s.i += 2
		case c == '$' || c == '`':
			s.expansionInto(&bare)
		default:
			bare.WriteByte(c)
			s.i++
		}
	}
	s.i++
	s.add(src[start:min(s.i, len(src))], bare.String())
}

// expansion reads one expansion at s.i into the current word.
func (s *shellScanner) expansion() {
	var bare strings.Builder
	start := s.i
	s.expansionInto(&bare)
	s.add(s.src[start:min(s.i, len(s.src))], bare.String())
}

// expansionInto reads one expansion at s.i: a GitHub expression or a braced
// parameter kept whole, a command substitution whose commands join the
// scanner's own, or a plain '$'. It writes the expansion as written.
func (s *shellScanner) expansionInto(bare *strings.Builder) {
	src := s.src
	start := s.i
	switch {
	case strings.HasPrefix(src[s.i:], "${{"):
		end := strings.Index(src[s.i:], "}}")
		if end < 0 {
			s.i = len(src)
		} else {
			s.i += end + 2
		}
	case strings.HasPrefix(src[s.i:], "$(") || src[s.i] == '`':
		stop := byte(')')
		s.i += 2
		if src[start] == '`' {
			stop = '`'
			s.i = start + 1
		}
		sub := &shellScanner{src: src, i: s.i}
		sub.scan(stop)
		s.cmds = append(s.cmds, sub.cmds...)
		s.i = sub.i
	case strings.HasPrefix(src[s.i:], "${"):
		end := strings.IndexByte(src[s.i:], '}')
		if end < 0 {
			s.i = len(src)
		} else {
			s.i += end + 1
		}
	default:
		s.i++
	}
	bare.WriteString(src[start:min(s.i, len(src))])
}

// skipHeredocs skips the bodies of the here-documents opened on the line
// that just ended: they are data, not commands.
func (s *shellScanner) skipHeredocs() {
	for _, delim := range s.heredocs {
		for s.i < len(s.src) {
			end := strings.IndexByte(s.src[s.i:], '\n')
			line := s.src[s.i:]
			next := len(s.src)
			if end >= 0 {
				line = s.src[s.i : s.i+end]
				next = s.i + end + 1
			}
			s.i = next
			if strings.TrimLeft(line, "\t") == delim {
				break
			}
		}
	}
	s.heredocs = nil
}

// commandWord returns the index of the word a command runs: the first word
// after leading reserved words, variable assignments, and the wrappers that
// run their arguments as a command (with their flags). It returns -1 for a
// command that only assigns.
func commandWord(cmd shellCommand) int {
	for i := 0; i < len(cmd); i++ {
		w := cmd[i].bare
		switch {
		case containsString(reservedWords, cmd[i].raw):
			continue
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

// reservedWords open or continue a compound command; the command they lead
// into is the next word.
var reservedWords = []string{"{", "}", "!", "if", "then", "else", "elif", "fi", "do", "done", "while", "until"}

// commandWrappers run their remaining arguments as a command.
var commandWrappers = []string{"env", "command", "exec", "time", "nohup", "nice", "sudo"}

// isAssignment reports whether a word is a shell variable assignment.
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isNameChar(name[i], i == 0) {
			return false
		}
	}
	return true
}

func containsString(set []string, s string) bool {
	for _, member := range set {
		if member == s {
			return true
		}
	}
	return false
}
