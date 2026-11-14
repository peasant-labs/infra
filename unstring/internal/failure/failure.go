// Package failure is the one shape every "the tool could not run" message
// takes: what went wrong, why, where it failed, and how to fix it. The
// command prints it and exits 2.
package failure

import (
	"fmt"
	"io"
	"strings"
)

// Error is an environment failure in the message contract's four parts.
// Every part is a complete sentence and none is empty.
type Error struct {
	// What says what went wrong, for example "ast-grep 0.45.0 found at …".
	What string
	// Why says why that stops the run.
	Why string
	// Where names the step that failed and what was or was not touched.
	Where string
	// Fix says what to do.
	Fix string
}

// New returns an Error with the four parts.
func New(what, why, where, fix string) *Error {
	return &Error{What: what, Why: why, Where: where, Fix: fix}
}

// Error joins the four parts on one line, for logs and wrapped errors.
func (e *Error) Error() string {
	return e.What + " why: " + e.Why + " where: " + e.Where + " fix: " + e.Fix
}

// Render writes the failure as the command prints it:
//
//	unstring: <what>
//	  why    <why>
//	  where  <where>
//	  fix    <fix>
//
// A part that spans several lines keeps its continuation lines aligned.
func Render(w io.Writer, e *Error) {
	fmt.Fprintf(w, "unstring: %s\n", e.What)
	WritePart(w, "why", e.Why)
	WritePart(w, "where", e.Where)
	WritePart(w, "fix", e.Fix)
}

// WritePart writes one labelled part with the contract's indentation: two
// spaces, the label padded to five, two spaces, the text. Continuation lines
// are indented to the text column.
func WritePart(w io.Writer, label, text string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	fmt.Fprintf(w, "  %-5s  %s\n", label, lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintf(w, "         %s\n", l)
	}
}
