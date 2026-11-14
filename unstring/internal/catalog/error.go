package catalog

import (
	"fmt"
	"strings"
)

// BuildError is a failure to build a catalog. It carries the parts of the
// message contract, so the command can print it as an environment failure
// (exit 2): what went wrong, why, where it failed, and how to fix it.
type BuildError struct {
	// What names what went wrong.
	What string
	// Why says why it went wrong and what it means for the run.
	Why string
	// Where names the step and the file or module the failure is in.
	Where string
	// Fix says how to repair it.
	Fix string
	// Err is the underlying error, when there is one.
	Err error
}

// Error renders the four parts on one line each.
func (e *BuildError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n  why    %s\n  where  %s\n  fix    %s", e.What, e.Why, e.Where, e.Fix)
	return b.String()
}

// Unwrap returns the underlying error.
func (e *BuildError) Unwrap() error { return e.Err }
