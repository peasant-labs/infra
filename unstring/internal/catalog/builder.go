package catalog

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// SchemaGeneratedTSDir is the directory of the schema module, relative to its
// root, whose string-literal unions join the catalog.
const SchemaGeneratedTSDir = "typescript/src/internal/generated"

// BuilderDeps are the boundaries a Builder reads through.
type BuilderDeps struct {
	// Runner runs the embedded discovery rules over TS and JS sources.
	Runner astgrep.Runner
	// Modules resolves the pinned schema module to a directory.
	Modules ModuleResolver
}

// NewBuilder returns the Builder that harvests the six recognised sources.
func NewBuilder(deps BuilderDeps) Builder {
	return &builder{deps: deps}
}

type builder struct {
	deps BuilderDeps
}

// Build harvests the repository's own members for req.Lang, then the pinned
// schema module's members when req.SchemaModule is set.
func (b *builder) Build(ctx context.Context, req BuildRequest) (Catalog, error) {
	if !req.Lang.IsValid() {
		return Catalog{}, &BuildError{
			What:  fmt.Sprintf("the catalog was asked for language %q.", req.Lang),
			Why:   "a catalog feeds one closed-set rule, and there is one per language: " + langList() + ".",
			Where: "catalog build, before any file was read.",
			Fix:   "pass one of " + langList() + "; this is a defect in the caller, report it to the infra maintainers.",
		}
	}
	var members []Member
	switch req.Lang {
	case rules.LangTS:
		found, err := harvestTS(ctx, b.deps.Runner, req.Dir, req.Roots, allTSDiscovery)
		if err != nil {
			return Catalog{}, err
		}
		members = append(members, found...)
	case rules.LangGo:
		found, err := harvestGo(req.Dir, req.Roots, false)
		if err != nil {
			return Catalog{}, err
		}
		members = append(members, found...)
	}
	var schemaTSRefs map[schemaTSKey]string
	if req.SchemaModule != "" {
		found, refs, err := b.schemaMembers(ctx, req)
		if err != nil {
			return Catalog{}, err
		}
		members = append(members, found...)
		schemaTSRefs = refs
	}
	sortMembers(members)
	return Catalog{Lang: req.Lang, Members: members, schemaTSRefs: schemaTSRefs}, nil
}

// schemaMembers resolves the pinned schema module and harvests its Go typed
// constants (outside internal/ and cmd/) and the string-literal unions of its
// generated TS. Both join the catalog of either language: they are the one
// contract both sides compare against.
func (b *builder) schemaMembers(ctx context.Context, req BuildRequest) ([]Member, map[schemaTSKey]string, error) {
	pin, err := pinnedSchema(req)
	if err != nil {
		return nil, nil, err
	}
	if b.deps.Modules == nil {
		return nil, nil, &BuildError{
			What:  "no module resolver is wired into the catalog builder.",
			Why:   "the schema module " + req.SchemaModule + " cannot be read without one.",
			Where: "catalog build, schema module step.",
			Fix:   "this is a defect in the command's wiring; report it to the infra maintainers.",
		}
	}
	dir, err := b.deps.Modules.Resolve(ctx, pin.Module, pin.Version, req.Env)
	if err != nil {
		return nil, nil, err
	}
	module := pin.String()

	goMembers, err := harvestGo(dir, []string{"."}, true)
	if err != nil {
		return nil, nil, err
	}
	var tsMembers []Member
	if info, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(SchemaGeneratedTSDir))); statErr == nil && info.IsDir() {
		tsMembers, err = harvestTS(ctx, b.deps.Runner, dir, []string{SchemaGeneratedTSDir}, unionDiscovery)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(goMembers) == 0 && len(tsMembers) == 0 {
		return nil, nil, &BuildError{
			What: fmt.Sprintf("the schema module %s declares no closed set.", module),
			Why: "the catalog reads the contract's Go typed string constants (outside internal/ and cmd/) and the string-literal unions under " +
				SchemaGeneratedTSDir + "; finding none means the module path or version names something that is not the contract, " +
				"and every comparison with a contract value would pass unseen.",
			Where: "catalog build, schema module step, reading " + dir + ".",
			Fix:   "set schema.module in .unstring.yaml to the contract module, and pin a version of it that declares its closed sets.",
		}
	}
	members := make([]Member, 0, len(goMembers)+len(tsMembers))
	for _, m := range append(goMembers, tsMembers...) {
		m.Source = SourceSchemaModule
		m.Site.Module = module
		members = append(members, m)
	}
	var refs map[schemaTSKey]string
	if req.Lang == rules.LangTS {
		refs, err = harvestSchemaTSRefs(ctx, b.deps.Runner, dir)
		if err != nil {
			return nil, nil, err
		}
	}
	return members, refs, nil
}

func sortMembers(ms []Member) {
	slices.SortFunc(ms, func(a, b Member) int {
		return cmp.Or(
			cmp.Compare(a.Value, b.Value),
			cmp.Compare(a.Site.Module, b.Site.Module),
			cmp.Compare(a.Site.File, b.Site.File),
			cmp.Compare(a.Site.Line, b.Site.Line),
			cmp.Compare(a.Source, b.Source),
			cmp.Compare(a.Set, b.Set),
			cmp.Compare(a.Name, b.Name),
		)
	})
}

func langList() string {
	return joinStringers(rules.AllLangs)
}
