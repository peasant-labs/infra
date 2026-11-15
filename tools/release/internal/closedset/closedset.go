// Package closedset holds the one rule for a string-backed closed set: how a
// raw value becomes a member, and how the set is listed in a message.
//
// A closed set is a named string type with one constant per member and an
// All list in canonical order. Membership is slices.Contains over that list;
// this package adds the two decisions the standard library does not make:
// Parse refuses every value outside the list, never substituting a default,
// and Join renders the list for a message in canonical order.
package closedset

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Member is a string-backed closed-set member that renders itself for a
// message.
type Member interface {
	~string
	fmt.Stringer
}

// Context is the part of a refusal only the caller knows. Every field is
// required: an empty one leaves a part of the refusal blank.
type Context struct {
	// Set names the closed set in a message, as in "plan action".
	Set string
	// Where names the parser, field or file that read the value.
	Where string
	// When names the step under way when the value was read.
	When string
	// Means says what the refusal means for the caller.
	Means string
}

// Error is the six-part refusal of a value outside a closed set. Parse fills
// What, Why and Fix; the caller's Context fills Where, When and Means.
type Error struct {
	What  string
	Why   string
	Where string
	When  string
	Means string
	Fix   string
}

// Error renders the refusal with every part labelled.
func (e *Error) Error() string {
	return fmt.Sprintf("%s [why: %s; where: %s; when: %s; means: %s; fix: %s]",
		e.What, e.Why, e.Where, e.When, e.Means, e.Fix)
}

// Parse returns the member of all spelled raw. Any other value, the empty
// string included unless it is listed, is refused with an *Error naming every
// member; no value is ever mapped to a nearby member or a default.
func Parse[T Member](all []T, raw string, ctx Context) (T, error) {
	if i := slices.Index(all, T(raw)); i >= 0 {
		return all[i], nil
	}
	var zero T
	return zero, &Error{
		What:  "unknown " + ctx.Set + " " + strconv.Quote(raw),
		Why:   "the " + ctx.Set + " set is closed, and an unknown value is never read as a member",
		Where: ctx.Where,
		When:  ctx.When,
		Means: ctx.Means,
		Fix:   "use one of " + Join(all, ", "),
	}
}

// Join renders the members for a message in the given order, separated by
// sep.
func Join[T fmt.Stringer](members []T, sep string) string {
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.String())
	}
	return strings.Join(names, sep)
}
