package ratchet

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/stringly/internal/failure"
)

// Format is the closed set of report formats.
type Format string

const (
	// FormatText is the human report: a location line and the four parts.
	FormatText Format = "text"
	// FormatGitHub prints one GitHub Actions `::error` annotation per finding.
	FormatGitHub Format = "github"
)

// AllFormats lists every Format.
var AllFormats = []Format{FormatText, FormatGitHub}

// String returns the wire value, as written after `--format`.
func (f Format) String() string { return string(f) }

// IsValid reports whether f is a member of AllFormats.
func (f Format) IsValid() bool {
	switch f {
	case FormatText, FormatGitHub:
		return true
	}
	return false
}

// ParseFormat converts a `--format` value into a Format.
func ParseFormat(s string) (Format, error) {
	if f := Format(s); f.IsValid() {
		return f, nil
	}
	names := make([]string, len(AllFormats))
	for i, f := range AllFormats {
		names[i] = f.String()
	}
	return "", fmt.Errorf("unknown report format %q: check prints exactly %s", s, strings.Join(names, " or "))
}

// maxTextWidth bounds the source text printed on a finding's first line.
const maxTextWidth = 120

// head is the first word of a finding's location line: its rule, or its kind
// when it concerns no rule.
func (f Finding) head() string {
	if f.Rule != "" {
		return f.Rule.String()
	}
	return f.Kind.String()
}

func (f Finding) location() string {
	switch {
	case f.File == "":
		return "-"
	case f.Line > 0:
		return f.File + ":" + strconv.Itoa(f.Line)
	}
	return f.File
}

func shortText(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if r := []rune(s); len(r) > maxTextWidth {
		s = string(r[:maxTextWidth]) + "…"
	}
	return s
}

// WriteReport writes the findings and a closing summary line.
//
// Text: for each finding, a location line `<rule>  <file>:<line>  <text>
// [<kind>]` and the four parts what, why, where and fix, then a blank line.
// GitHub: one `::error file=…,line=…,title=…::…` annotation per finding, the
// four parts in the message.
func WriteReport(w io.Writer, format Format, cmd Command, findings []Finding) {
	for _, f := range findings {
		switch format {
		case FormatGitHub:
			writeGitHub(w, f)
		default:
			writeText(w, f)
		}
	}
	switch {
	case len(findings) == 0:
		fmt.Fprintf(w, "stringly %s: no findings.\n", cmd)
	case len(findings) == 1:
		fmt.Fprintf(w, "stringly %s: 1 finding.\n", cmd)
	default:
		fmt.Fprintf(w, "stringly %s: %d findings.\n", cmd, len(findings))
	}
}

func writeText(w io.Writer, f Finding) {
	line := f.head() + "  " + f.location()
	if t := shortText(f.Text); t != "" {
		line += "  " + t
	}
	fmt.Fprintf(w, "%s  [%s]\n", line, f.Kind)
	failure.WritePart(w, "what", f.What)
	failure.WritePart(w, "why", f.Why)
	failure.WritePart(w, "where", f.Where)
	failure.WritePart(w, "fix", f.Fix)
	fmt.Fprintln(w)
}

func writeGitHub(w io.Writer, f Finding) {
	var props []string
	if f.File != "" {
		props = append(props, "file="+escapeProperty(f.File))
		if f.Line > 0 {
			props = append(props, "line="+strconv.Itoa(f.Line))
		}
	}
	props = append(props, "title="+escapeProperty(fmt.Sprintf("%s (%s)", f.head(), f.Kind)))
	msg := fmt.Sprintf("%s\nwhy: %s\nwhere: %s\nfix: %s", f.What, f.Why, f.Where, f.Fix)
	if t := shortText(f.Text); t != "" {
		msg = t + ": " + msg
	}
	fmt.Fprintf(w, "::error %s::%s\n", strings.Join(props, ","), escapeData(msg))
}

// escapeData escapes an annotation message as the Actions runner reads it.
func escapeData(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return strings.ReplaceAll(s, "\n", "%0A")
}

// escapeProperty escapes an annotation property value.
func escapeProperty(s string) string {
	s = escapeData(s)
	s = strings.ReplaceAll(s, ":", "%3A")
	return strings.ReplaceAll(s, ",", "%2C")
}
