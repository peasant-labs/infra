package main

import (
	"errors"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
	"github.com/peasant-labs/infra/unstring/internal/catalog"
	"github.com/peasant-labs/infra/unstring/internal/failure"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// wiredDeps is the one wiring point of the production rule registry, catalog
// builder and closed-set rule generator. The end-to-end test builds the same
// binary, so it runs these values.
func wiredDeps() deps {
	return deps{
		Registry: rules.NewRegistry,
		Catalog: func(r astgrep.Runner) catalog.Builder {
			return catalog.NewBuilder(catalog.BuilderDeps{Runner: r, Modules: catalog.GoModDownload{}})
		},
		Generator:      catalog.NewRuleGenerator(),
		DefinitionSite: catalog.Catalog.DefinitionSiteHit,
		Explain:        catalog.Catalog.Explain,
		Failure:        catalogFailure,
		DocumentRule:   rules.ParseDocumentID,
		LanguageGlobs:  astgrep.DefaultLanguageGlobs(),
	}
}

// catalogFailure converts a catalog build error into the four-part failure
// the command prints as an environment failure, and reports false for any
// other error.
func catalogFailure(err error) (*failure.Error, bool) {
	var be *catalog.BuildError
	if !errors.As(err, &be) {
		return nil, false
	}
	return failure.New(be.What, be.Why, be.Where, be.Fix), true
}
