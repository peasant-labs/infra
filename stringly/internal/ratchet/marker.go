package ratchet

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// MarkerPrefix is the word that opens an allow marker after the comment
// opener.
const MarkerPrefix = "stringly-allow:"

// MarkerSeparator is the one separator between a marker's rule id and its
// reason: the em dash.
const MarkerSeparator = "—"

// MinReasonWords is the fewest words a marker's reason may have.
const MinReasonWords = 3

// nativeIgnore is ast-grep's own suppression comment.
const nativeIgnore = "ast-grep-ignore"

// Comment rule ids. They are not rule ids of the tool: their matches are the
// comments that may be markers, found by the same scan as the hits so a file
// outside the scan cannot hide one.
const (
	commentRuleTS = "stringly-comment-ts"
	commentRuleGo = "stringly-comment-go"
)

// commentRuleRegex selects the comments the checker reads.
const commentRuleRegex = `stringly-allow|ast-grep-ignore`

// CommentRule returns the ast-grep rule that finds marker comments in one
// language's files.
func CommentRule(l rules.Lang) astgrep.RuleFile {
	id, lang := commentRuleGo, "go"
	if l == rules.LangTS {
		id, lang = commentRuleTS, "tsx"
	}
	body := fmt.Sprintf("id: %s\nlanguage: %s\nseverity: warning\nrule:\n  kind: comment\n  regex: '%s'\n", id, lang, commentRuleRegex)
	return astgrep.RuleFile{Name: id + ".yml", Body: []byte(body)}
}

// IsCommentRule reports whether an ast-grep record's rule id is one of the
// comment rules.
func IsCommentRule(ruleID string) bool {
	return ruleID == commentRuleTS || ruleID == commentRuleGo
}

// Comment is one comment the comment rule matched.
type Comment struct {
	// File is relative to the config directory, with `/` separators.
	File string `yaml:"file"`
	// Line and EndLine are the comment's 1-based first and last lines.
	Line    int `yaml:"line"`
	EndLine int `yaml:"endLine"`
	// Column is the 1-based character column where the comment starts.
	Column int `yaml:"column"`
	// Text is the comment, opener and closer included.
	Text string `yaml:"text"`
	// LineText is the whole source line the comment starts on.
	LineText string `yaml:"lineText"`
}

// CommentFromMatch converts a comment-rule match.
func CommentFromMatch(m astgrep.Match) Comment {
	end := m.EndLine
	if end < m.Line {
		end = m.Line
	}
	return Comment{File: m.File, Line: m.Line, EndLine: end, Column: m.Column, Text: m.Text, LineText: m.LineText}
}

// LineReader returns the lines of a file relative to the config directory.
// The checker reads a file only when a marker in it stands alone on its line,
// to find the line the marker applies to.
type LineReader func(file string) ([]string, error)

// markerState is what a marker comment says.
type markerState int

const (
	markerValid markerState = iota
	markerNoReason
	markerUnknownRule
	markerNotAccepted
)

// marker is one parsed `stringly-allow` comment.
type marker struct {
	comment  Comment
	state    markerState
	rule     rules.RuleID // set unless state is markerUnknownRule
	ruleText string
	reason   string
	target   int
	// suggestion is the exact marker to paste, for marker-no-reason.
	suggestion string
}

// nativeIgnoreComment is one ast-grep-ignore comment that suppresses a guard
// rule: either it names one, or it names none and suppresses every rule.
type nativeIgnoreComment struct {
	comment Comment
	rule    rules.RuleID // empty for a bare comment
}

var ruleIDToken = regexp.MustCompile(`^[A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)*`)

// commentBody strips the comment's opener and closer and reports whether it
// was a block comment.
func commentBody(text string) (string, bool) {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "//"):
		return strings.TrimSpace(strings.TrimPrefix(t, "//")), false
	case strings.HasPrefix(t, "/*"):
		t = strings.TrimPrefix(t, "/*")
		t = strings.TrimSuffix(t, "*/")
		return strings.TrimSpace(t), true
	}
	return t, false
}

// parseComment classifies a comment. It returns a marker, a native ignore, or
// neither (a comment that merely mentions one of the words).
func parseComment(c Comment) (*marker, *nativeIgnoreComment) {
	body, block := commentBody(c.Text)
	switch {
	case strings.HasPrefix(body, nativeIgnore):
		return nil, parseNativeIgnore(c, strings.TrimPrefix(body, nativeIgnore))
	case strings.HasPrefix(body, "stringly-allow"):
		return parseMarker(c, body, block), nil
	}
	return nil, nil
}

func parseNativeIgnore(c Comment, rest string) *nativeIgnoreComment {
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, ":") {
		if rest == "" || !ruleIDToken.MatchString(rest) {
			// A bare ast-grep-ignore suppresses every rule, the guard's included.
			return &nativeIgnoreComment{comment: c}
		}
		return nil
	}
	ids := strings.TrimPrefix(rest, ":")
	if i := strings.Index(ids, " --"); i >= 0 {
		ids = ids[:i]
	}
	for _, part := range strings.Split(ids, ",") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if id, err := rules.ParseRuleID(fields[0]); err == nil {
			return &nativeIgnoreComment{comment: c, rule: id}
		}
	}
	return nil
}

func parseMarker(c Comment, body string, block bool) *marker {
	m := &marker{comment: c}
	rest := strings.TrimPrefix(body, "stringly-allow")
	hasColon := strings.HasPrefix(rest, ":")
	rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
	m.ruleText = ruleIDToken.FindString(rest)
	rest = strings.TrimSpace(strings.TrimPrefix(rest, m.ruleText))

	id, err := rules.ParseRuleID(m.ruleText)
	if err != nil {
		m.state = markerUnknownRule
		return m
	}
	m.rule = id
	if !id.AcceptsMarker() {
		m.state = markerNotAccepted
		return m
	}

	sepOK := strings.HasPrefix(rest, MarkerSeparator)
	reason := rest
	if sepOK {
		reason = strings.TrimPrefix(rest, MarkerSeparator)
	} else {
		for _, wrong := range []string{"--", "–", "-", ":"} {
			if strings.HasPrefix(rest, wrong) {
				reason = strings.TrimPrefix(rest, wrong)
				break
			}
		}
	}
	reason = strings.TrimSpace(reason)
	m.reason = reason
	if !hasColon || !sepOK || len(strings.Fields(reason)) < MinReasonWords {
		m.state = markerNoReason
		shown := reason
		if len(strings.Fields(shown)) < MinReasonWords {
			shown = "<a reason of three or more words>"
		}
		m.suggestion = formatMarker(id, shown, block)
		return m
	}
	m.state = markerValid
	return m
}

// formatMarker renders a marker in the comment form the developer used.
func formatMarker(id rules.RuleID, reason string, block bool) string {
	if block {
		return fmt.Sprintf("/* %s %s %s %s */", MarkerPrefix, id, MarkerSeparator, reason)
	}
	return fmt.Sprintf("// %s %s %s %s", MarkerPrefix, id, MarkerSeparator, reason)
}

// standsAlone reports whether nothing but whitespace (or the `{` of a JSX
// comment) precedes the comment on its line.
func standsAlone(c Comment) bool {
	runes := []rune(c.LineText)
	col := c.Column - 1
	if col < 0 {
		col = 0
	}
	if col > len(runes) {
		col = len(runes)
	}
	prefix := strings.TrimSpace(string(runes[:col]))
	return prefix == "" || prefix == "{"
}

// commentOnly reports whether a source line holds nothing but a comment.
func commentOnly(line string) bool {
	t := strings.TrimSpace(line)
	for _, p := range []string{"//", "/*", "{/*", "*"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// resolveTarget sets the line a marker applies to: its own line when it
// trails code, otherwise the first line after it that is not itself a
// comment line, so markers and other comments may stack above one line.
func resolveTarget(c Comment, read LineReader, cache map[string][]string) (int, error) {
	if !standsAlone(c) {
		return c.Line, nil
	}
	lines, ok := cache[c.File]
	if !ok {
		var err error
		lines, err = read(c.File)
		if err != nil {
			return 0, err
		}
		cache[c.File] = lines
	}
	for n := c.EndLine + 1; n <= len(lines); n++ {
		if !commentOnly(lines[n-1]) {
			return n, nil
		}
	}
	return c.EndLine + 1, nil
}
