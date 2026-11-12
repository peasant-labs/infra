package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
)

const (
	schemaTSEntry    = "typescript/src/index.ts"
	schemaTSEnums    = SchemaGeneratedTSDir + "/enums.gen.ts"
	schemaTSReexport = "./internal/generated/enums.gen.js"
)

// harvestSchemaTSRefs reads only runtime objects that the package root really
// re-exports. Unknown or older source layouts simply have no known reference;
// they must not result in a fabricated import. These references annotate the
// catalog but never add values to the generated matching rule.
func harvestSchemaTSRefs(ctx context.Context, runner astgrep.Runner, dir string) (map[schemaTSKey]string, error) {
	for _, file := range []string{schemaTSEntry, schemaTSEnums} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(file))); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, schemaTSRefError("the schema TypeScript source "+file+" could not be read", err)
		}
	}
	files := make([]astgrep.RuleFile, 0, 2)
	for _, name := range []string{"schema-export.yml", "schema-root-export.yml"} {
		body, err := discoveryFS.ReadFile("discovery/" + name)
		if err != nil {
			return nil, schemaTSRefError("the embedded schema export discovery rule "+name+" is missing", err)
		}
		files = append(files, astgrep.RuleFile{Name: name, Body: body})
	}
	if runner == nil {
		return nil, schemaTSRefError("no ast-grep runner is wired into schema export discovery", nil)
	}
	matches, err := runner.Scan(ctx, astgrep.ScanRequest{Dir: dir, Roots: []string{schemaTSEntry, schemaTSEnums}, Rules: files})
	if err != nil {
		return nil, schemaTSRefError("ast-grep failed while discovering schema TypeScript exports", err)
	}
	var exported bool
	refs := map[schemaTSKey]string{}
	for _, m := range matches {
		switch {
		case m.RuleID == "stringly-discover-schema-root-export" && m.File == schemaTSEntry:
			source, ok := unquoteJS(m.Vars["MODULE"])
			exported = exported || (ok && source == schemaTSReexport)
		case m.RuleID == "stringly-discover-schema-export" && m.File == schemaTSEnums:
			value, ok := unquoteJS(m.Text)
			set, name := m.Vars["SET"], m.Vars["NAME"]
			if !ok || value == "" || set == "" || name == "" {
				continue
			}
			key, ref := schemaTSKey{set: set, value: value}, set+"."+name
			if prior := refs[key]; prior == "" || ref < prior {
				refs[key] = ref
			}
		}
	}
	if !exported {
		return nil, nil
	}
	return refs, nil
}

func schemaTSRefError(what string, err error) error {
	why := "the schema package's actual runtime exports are needed to name an importable TypeScript member."
	if err != nil {
		why += " " + err.Error()
	}
	return &BuildError{
		What: what + ".", Why: why,
		Where: "catalog build, schema TypeScript export discovery.",
		Fix:   fmt.Sprintf("check the schema module's files and rerun with ast-grep %s; rebuild the tool if an embedded rule is missing.", astgrep.RequiredVersion),
		Err:   err,
	}
}
