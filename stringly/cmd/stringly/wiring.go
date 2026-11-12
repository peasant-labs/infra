//go:build !stringly_stub

package main

import (
	"context"
	"errors"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
	"github.com/peasant-labs/infra/stringly/internal/catalog"
	"github.com/peasant-labs/infra/stringly/internal/failure"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// wiredDeps is the one wiring point of the production rule registry, catalog
// builder and closed-set rule generator.
//
// To wire them, return:
//
//	deps{
//		Registry: rules.NewRegistry,
//		Catalog: func(r astgrep.Runner) catalog.Builder {
//			return catalog.NewBuilder(catalog.BuilderDeps{Runner: r, Modules: catalog.GoModDownload{}})
//		},
//		Generator:      catalog.NewRuleGenerator(),
//		DefinitionSite: catalog.Catalog.DefinitionSiteHit,
//		Explain:        catalog.Catalog.Explain,
//		Failure: func(err error) (*failure.Error, bool) {
//			var be *catalog.BuildError
//			if !errors.As(err, &be) {
//				return nil, false
//			}
//			return failure.New(be.What, be.Why, be.Where, be.Fix), true
//		},
//		DocumentRule:  rules.ParseDocumentID,
//		LanguageGlobs: map[string][]string{rules.LangTS.AstGrepLanguage(): rules.LangTS.FileGlobs()},
//	}
//
// Then delete the files built with the `stringly_stub` tag (stub_*.go and
// testdata/stub-rules/), drop the build constraint from this file, and set
// e2eBuildTags in e2e_test.go to "", so the end-to-end test runs the real
// binary with the real rules.
//
// Until they are wired, every command that scans stops with exit 2 and names
// this file.
func wiredDeps() deps {
	return deps{
		Registry:       func(rules.Params) (rules.Registry, error) { return nil, errNotWired },
		Catalog:        func(astgrep.Runner) catalog.Builder { return unwiredBuilder{} },
		Generator:      unwiredGenerator{},
		DefinitionSite: func(catalog.Catalog, rules.Hit) bool { return false },
		Explain:        func(catalog.Catalog, string) rules.Message { return rules.Message{} },
		Failure:        func(error) (*failure.Error, bool) { return nil, false },
		DocumentRule:   rules.ParseRuleID,
		LanguageGlobs:  nil,
	}
}

var errNotWired = errors.New("this build has no rule registry, catalog builder or closed-set rule generator wired " +
	"(cmd/stringly/wiring.go, wiredDeps)")

type unwiredBuilder struct{}

func (unwiredBuilder) Build(context.Context, catalog.BuildRequest) (catalog.Catalog, error) {
	return catalog.Catalog{}, errNotWired
}

type unwiredGenerator struct{}

func (unwiredGenerator) Generate(catalog.Catalog) (rules.Rule, error) {
	return rules.Rule{}, errNotWired
}
