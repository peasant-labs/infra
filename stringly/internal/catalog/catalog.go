package catalog

import (
	"context"

	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// Catalog is every member value one language's rule compares against.
type Catalog struct {
	// Lang is the language of the closed-set rule this catalog feeds: TS and
	// JS members for closed-set-literal, Go members for go-closed-set-literal.
	Lang rules.Lang
	// Members are the harvested members. One value may appear several times,
	// once per declaring site; an empty-string value never appears.
	Members []Member
}

// Member is one value declared by one definition site.
type Member struct {
	// Value is the wire value, as the literal it would be compared with.
	Value string
	// Set is the declaring name: the object that holds the closed() call, the
	// record table, the union's type alias or the Go type. It is empty when the
	// source has no name (an inline union).
	Set string
	// Source is how the value was declared.
	Source SourceKind
	// Site is where the value was declared.
	Site DefinitionSite
}

// DefinitionSite is where a member is declared.
type DefinitionSite struct {
	// Module is "<module path>@<version>" when the member comes from the
	// pinned schema module, and empty when it comes from the scanned
	// repository.
	Module string
	// File is relative to the config directory for a repository member, and to
	// the module root for a schema member, with `/` separators.
	File string
	// Line is the 1-based line of the declaration.
	Line int
}

// BuildRequest is what a Builder needs to harvest one language's catalog.
type BuildRequest struct {
	// Dir is the directory holding the repository's config. Roots and the
	// version files are read relative to it.
	Dir string
	// Lang selects the sources: TS and JS discovery for LangTS, Go parsing for
	// LangGo, and the schema module's members for both.
	Lang rules.Lang
	// Roots are the config's roots for Lang, relative to Dir.
	Roots []string
	// SchemaModule is the config's schema module path, for example
	// "github.com/peasant-labs/schema". Its version comes from the
	// repository's go.mod (LangGo) or the exact `@peasant-labs/schema`
	// version in the TS root's package.json (LangTS).
	SchemaModule string
	// Env is the environment for `go mod download`, in os.Environ form. Tests
	// pass a file proxy here; production passes the process environment.
	Env []string
}

// Builder harvests a catalog from a repository and its pinned schema module.
type Builder interface {
	// Build returns the catalog for req.Lang. A range schema version, a failed
	// module download, or a schema module with zero sets is an error.
	Build(ctx context.Context, req BuildRequest) (Catalog, error)
}

// RuleGenerator turns a catalog into its closed-set rule: closed-set-literal
// for a LangTS catalog, go-closed-set-literal for a LangGo catalog. The rule's
// `not` clauses carry every rules.ExcludedOperand of its language.
type RuleGenerator interface {
	Generate(c Catalog) (rules.Rule, error)
}
