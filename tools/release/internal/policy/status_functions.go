package policy

import (
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	regexp "github.com/wasilibs/go-re2"
)

// StatusFunction is one of the four status check functions a GitHub
// conditional may call. GitHub evaluates them against the run's status, and
// "A default status check of success() is applied unless you include one of
// these functions" (GitHub Docs, "Evaluate expressions"). So a conditional
// that calls one carries that status check *instead of* the implicit
// success(), which is how a condition that looks like a conjunction can run
// after a failure. The closed set is the one GitHub names for
// jobs.<job_id>.if and steps.if. The zero value is unknown.
type StatusFunction string

const (
	// StatusFunctionSuccess is true when every previous step succeeded.
	StatusFunctionSuccess StatusFunction = "success"
	// StatusFunctionFailure is true when a previous step failed.
	StatusFunctionFailure StatusFunction = "failure"
	// StatusFunctionAlways is true even after a cancellation or failure.
	StatusFunctionAlways StatusFunction = "always"
	// StatusFunctionCancelled is true when the run was cancelled.
	StatusFunctionCancelled StatusFunction = "cancelled"
)

// AllStatusFunctions is the canonical ordered closed set of status check
// functions GitHub allows in an if conditional.
var AllStatusFunctions = []StatusFunction{
	StatusFunctionSuccess,
	StatusFunctionFailure,
	StatusFunctionAlways,
	StatusFunctionCancelled,
}

func (f StatusFunction) String() string { return string(f) }

// Call renders the function as an expression calls it.
func (f StatusFunction) Call() string { return f.String() + "()" }

// ParseStatusFunction maps a canonical spelling to its member and refuses
// anything else. The matcher below lower-cases a call before parsing, because
// GitHub matches function names case-insensitively (the Actions runner
// registers them in a dictionary built with StringComparer.OrdinalIgnoreCase).
func ParseStatusFunction(raw string) (StatusFunction, error) {
	return closedset.Parse(AllStatusFunctions, raw, closedset.Context{
		Set:   "status check function",
		Where: "policy.ParseStatusFunction",
		When:  "reading a status function call in an if conditional during merge-queue policy validation",
		Means: "a call outside the set is no status function, so the implicit success() check still applies",
	})
}

// statusFunctionCall matches a call to any status function. GitHub ignores
// case in function names and skips whitespace between tokens, so SUCCESS(),
// Success (), and cancelled  ( ) are the same call as success(). Matching the
// whole call, not the bare word, keeps a string value such as "success" from
// being read as one.
var statusFunctionCall = regexp.MustCompile(`(?i)\b(success|failure|always|cancelled)\s*\(`)

// statusFunctionsIn returns the status functions a conditional calls, in
// canonical order, or nil.
func statusFunctionsIn(cond string) []StatusFunction {
	found := map[StatusFunction]bool{}
	for _, m := range statusFunctionCall.FindAllStringSubmatch(cond, -1) {
		if fn, err := ParseStatusFunction(strings.ToLower(m[1])); err == nil {
			found[fn] = true
		}
	}
	var out []StatusFunction
	for _, fn := range AllStatusFunctions {
		if found[fn] {
			out = append(out, fn)
		}
	}
	return out
}

// needsResultRead matches a read of a needed job's result, in either access
// syntax GitHub allows: needs.<job>.result or needs['<job>'].result, with
// whitespace between tokens. The implicit success() check is what skips a job
// after a needed job fails or is skipped, so a result read in that job's if is
// refused even when no status function is present.
var needsResultRead = regexp.MustCompile(`(?i)\bneeds\s*(?:\.\s*[A-Za-z0-9_-]+|\[\s*['"][^'"]+['"]\s*\])\s*\.\s*result\b`)
