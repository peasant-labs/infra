// Package schema is a miniature contract module for the catalog tests. Its
// module path is example.test/schema, which proves the tool does not hard-code
// the real contract's path.
package schema

// TranscriptVisibility is a closed set: two constants of a string type.
type TranscriptVisibility string

const (
	VisibilityPublic  TranscriptVisibility = "public"
	VisibilityPrivate TranscriptVisibility = "private"
)

// RepositoryRole is declared with conversions instead of typed constants.
type RepositoryRole string

const (
	RoleOwner  = RepositoryRole("owner")
	RoleReader = RepositoryRole("reader")
)

// Lone has a single constant, so it is not a set.
type Lone string

const OnlyLone Lone = "lonely"

// Blank's empty member is dropped; one member is left, so it is not a set.
type Blank string

const (
	BlankNone Blank = ""
	BlankOne  Blank = "blank-one"
)

// Untyped constants are not members.
const Untyped = "untyped-value"
