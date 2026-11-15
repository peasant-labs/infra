package main

import (
	"github.com/peasant-labs/infra/unstring/internal/astgrep"
	"github.com/peasant-labs/infra/unstring/internal/catalog"
	"github.com/peasant-labs/infra/unstring/internal/failure"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// deps are the parts of the tool that the rules and catalog packages
// provide: the static rule registry for a repository's settings, the catalog
// builder, the generator of the two closed-set rules, how a scan maps
// ast-grep's ids and file types to the tool's rules, and what the catalog
// says about a closed-set hit. The command wires the production values in
// wiredDeps (wiring.go); everything else in the command is the same for every
// build.
type deps struct {
	// Registry builds the static rules for one repository's settings.
	Registry func(rules.Params) (rules.Registry, error)
	// Catalog returns the builder that harvests one language's catalog from
	// the repository and its pinned schema module. It scans with the run's
	// ast-grep, whose version was already asserted.
	Catalog func(astgrep.Runner) catalog.Builder
	// Generator turns a catalog into its closed-set rule.
	Generator catalog.RuleGenerator
	// DefinitionSite reports whether a closed-set hit sits in the file that
	// defines its value; such a hit is dropped before counting.
	DefinitionSite func(catalog.Catalog, rules.Hit) bool
	// Explain returns the message of a closed-set hit whose literal is value:
	// the sets that declare it and the fix for their source kind.
	Explain func(c catalog.Catalog, value string) rules.Message
	// Failure converts a catalog build error into the four-part failure the
	// command prints, and reports false for any other error.
	Failure func(error) (*failure.Error, bool)
	// DocumentRule maps an ast-grep record's rule id to the tool rule whose
	// document produced it. A rule may have several documents (ast-grep
	// refuses duplicate ids), so this is not ParseRuleID. An error means the
	// record is not the tool's, for example unused-suppression.
	DocumentRule func(documentID string) (rules.RuleID, error)
	// LanguageGlobs is the scan config's languageGlobs: the file globs each
	// ast-grep language parses. Nil means astgrep.DefaultLanguageGlobs.
	LanguageGlobs map[string][]string
}
