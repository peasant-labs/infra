package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// SchemaPackage is the npm package whose exact version in a TS root's
// package.json pins the schema module for the TS catalog.
const SchemaPackage = "@peasant-labs/schema"

// ModuleResolver turns a module path and an exact version into the directory
// that holds the module's source.
type ModuleResolver interface {
	// Resolve returns the module's directory. env is the environment of any
	// process it starts, in os.Environ form.
	Resolve(ctx context.Context, module, version string, env []string) (string, error)
}

// GoModDownload resolves a module with `go mod download -json
// <module>@<version>`, so the module comes from the module cache or the
// configured proxy exactly as the go command would fetch it.
type GoModDownload struct {
	// GoCommand is the go binary; empty means "go" on PATH.
	GoCommand string
}

// Resolve runs `go mod download -json` in an empty temporary directory, so no
// go.mod or go.work of the scanned repository takes part.
func (g GoModDownload) Resolve(ctx context.Context, module, version string, env []string) (string, error) {
	goCmd := g.GoCommand
	if goCmd == "" {
		goCmd = "go"
	}
	if env == nil {
		env = os.Environ()
	}
	target := module + "@" + version
	where := "catalog build, schema module step: go mod download -json " + target + "."
	tmp, err := os.MkdirTemp("", "stringly-schema-")
	if err != nil {
		return "", &BuildError{
			What:  "a temporary directory for the schema download could not be created.",
			Why:   err.Error(),
			Where: where,
			Fix:   "check that the system temporary directory exists and is writable.",
			Err:   err,
		}
	}
	defer os.RemoveAll(tmp)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, goCmd, "mod", "download", "-json", target)
	cmd.Dir = tmp
	cmd.Env = append(append([]string(nil), env...), "GOWORK=off")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	var out struct {
		Path, Version, Dir, Error string
	}
	_ = json.Unmarshal(stdout.Bytes(), &out)
	if runErr != nil || out.Error != "" || out.Dir == "" {
		reason := strings.TrimSpace(out.Error)
		if reason == "" {
			reason = strings.TrimSpace(stderr.String())
		}
		if reason == "" && runErr != nil {
			reason = runErr.Error()
		}
		return "", &BuildError{
			What: "the schema module " + target + " could not be downloaded.",
			Why: "the closed sets of the pinned contract are part of the catalog, so the run cannot compare without them. " +
				"go reported: " + reason,
			Where: where,
			Fix: "check that " + target + " is a published version, that the module proxy is reachable (GOPROXY=" +
				envValue(env, "GOPROXY") + "), and that the go command works offline from the module cache when the network is down.",
			Err: runErr,
		}
	}
	return out.Dir, nil
}

func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	if v == "" {
		return "the go default"
	}
	return v
}

// schemaPin is the module and version a repository builds the schema module
// from: the required version, or the replacement go.mod names for it.
type schemaPin struct {
	Module, Version string
}

// String returns the pin as module@version.
func (p schemaPin) String() string { return p.Module + "@" + p.Version }

// pinnedSchema returns the schema module version the repository pins for
// req.Lang: the go.mod requirement, after any replace of it, for Go, and the
// exact SchemaPackage version in package.json, as v<version>, for TS.
func pinnedSchema(req BuildRequest) (schemaPin, error) {
	switch req.Lang {
	case rules.LangGo:
		return goModPin(req.Dir, req.Roots, req.SchemaModule)
	case rules.LangTS:
		return packageJSONPin(req.Dir, req.Roots, req.SchemaModule)
	}
	return schemaPin{}, fmt.Errorf("no schema pin for language %q", req.Lang)
}

// manifestFiles returns, for each root, the nearest file named name in the
// root or one of its parents up to dir, without duplicates.
func manifestFiles(dir string, roots []string, name string) []string {
	seen := map[string]bool{}
	var found []string
	for _, root := range roots {
		rel := path.Clean(root)
		for {
			p := filepath.Join(dir, filepath.FromSlash(rel), name)
			if _, err := os.Stat(p); err == nil {
				if !seen[p] {
					seen[p] = true
					found = append(found, p)
				}
				break
			}
			if rel == "." || rel == "/" {
				break
			}
			rel = path.Dir(rel)
		}
	}
	return found
}

// goModPin reads the requirement on module from the go.mod of the Go roots,
// with any replace of it applied.
func goModPin(dir string, roots []string, module string) (schemaPin, error) {
	files := manifestFiles(dir, roots, "go.mod")
	pins := map[schemaPin][]string{}
	for _, f := range files {
		pin, err := goModRequirement(f, relTo(dir, f), module)
		if err != nil {
			var be *BuildError
			if errors.As(err, &be) {
				return schemaPin{}, err
			}
			return schemaPin{}, &BuildError{
				What:  "the go.mod at " + relTo(dir, f) + " could not be read.",
				Why:   err.Error(),
				Where: "catalog build, schema version step (Go).",
				Fix:   "fix the go.mod (go mod tidy reports the problem).",
				Err:   err,
			}
		}
		if pin != (schemaPin{}) {
			pins[pin] = append(pins[pin], relTo(dir, f))
		}
	}
	return singlePin(pins, "go.mod", module, dir, files,
		"add the requirement with go get "+module+"@<version>, or remove schema.module from .stringly.yaml if the Go code does not use the contract.")
}

// goModRequirement returns the module and version the go.mod at file builds
// module from: the required version, or the target of the replace directive
// that applies to it, with the go command's precedence (a replace of the
// required version wins over a replace of every version). It returns the zero
// pin when module is not required, and a *BuildError when module is replaced
// by a directory, which has no version to download. rel names file in
// messages.
func goModRequirement(file, rel, module string) (schemaPin, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return schemaPin{}, err
	}
	mf, err := modfile.Parse(file, data, nil)
	if err != nil {
		return schemaPin{}, err
	}
	var pin schemaPin
	for _, r := range mf.Require {
		if r.Mod.Path == module {
			pin = schemaPin{Module: module, Version: r.Mod.Version}
		}
	}
	if pin == (schemaPin{}) {
		return pin, nil
	}
	var everyVersion, exact *modfile.Replace
	for _, r := range mf.Replace {
		switch {
		case r.Old.Path != module:
		case r.Old.Version == "":
			everyVersion = r
		case r.Old.Version == pin.Version:
			exact = r
		}
	}
	rep := exact
	if rep == nil {
		rep = everyVersion
	}
	if rep == nil {
		return pin, nil
	}
	if rep.New.Version == "" {
		return schemaPin{}, &BuildError{
			What: fmt.Sprintf("the go.mod at %s replaces %s with the directory %s.", rel, module, rep.New.Path),
			Why: "the catalog reads the contract's closed sets from one published module version, so the result is the same on " +
				"every machine; a directory replacement has no version to download, and its contents can differ from checkout to checkout.",
			Where: fmt.Sprintf("catalog build, schema version step (Go), reading the replace directive in %s.", rel),
			Fix: fmt.Sprintf("replace %s with a published version (replace %s => %s vX.Y.Z), or drop the replace and require the "+
				"version you need; to check unpublished contract changes, tag a pre-release of the module and replace with that.", module, module, module),
		}
	}
	return schemaPin{Module: rep.New.Path, Version: rep.New.Version}, nil
}

// exactVersion is an npm version with no range operator.
var exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// packageJSONPin reads the exact SchemaPackage version from the package.json
// of the TS roots.
func packageJSONPin(dir string, roots []string, module string) (schemaPin, error) {
	files := manifestFiles(dir, roots, "package.json")
	pins := map[schemaPin][]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return schemaPin{}, &BuildError{
				What: "the package.json at " + relTo(dir, f) + " could not be read.", Why: err.Error(),
				Where: "catalog build, schema version step (TS).", Fix: "check the file's permissions.", Err: err,
			}
		}
		var pkg map[string]json.RawMessage
		if err := json.Unmarshal(data, &pkg); err != nil {
			return schemaPin{}, &BuildError{
				What: "the package.json at " + relTo(dir, f) + " is not valid JSON.", Why: err.Error(),
				Where: "catalog build, schema version step (TS).", Fix: "fix the JSON syntax.", Err: err,
			}
		}
		for _, field := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
			var deps map[string]string
			if raw, ok := pkg[field]; !ok || json.Unmarshal(raw, &deps) != nil {
				continue
			}
			spec, ok := deps[SchemaPackage]
			if !ok {
				continue
			}
			if !exactVersion.MatchString(spec) {
				return schemaPin{}, &BuildError{
					What: fmt.Sprintf("%s is pinned as %q in %s (%s), which is not an exact version.", SchemaPackage, spec, relTo(dir, f), field),
					Why: "the catalog reads the contract's closed sets from one exact schema version; a range or a non-registry " +
						"specifier would let the catalog change without a change in the repository.",
					Where: "catalog build, schema version step (TS).",
					Fix:   fmt.Sprintf("pin an exact version, for example \"%s\": \"%s\".", SchemaPackage, strings.TrimLeft(spec, "^~>=<v ")),
				}
			}
			pin := schemaPin{Module: module, Version: "v" + spec}
			pins[pin] = append(pins[pin], relTo(dir, f))
			break
		}
	}
	return singlePin(pins, "package.json", SchemaPackage, dir, files,
		"add an exact dependency on "+SchemaPackage+" to the TS root's package.json, or remove schema.module from .stringly.yaml if the TS code does not use the contract.")
}

// singlePin returns the one pin the manifests agree on, or the failure of none
// or of two.
func singlePin(pins map[schemaPin][]string, manifest, dep, dir string, files []string, missingFix string) (schemaPin, error) {
	switch len(pins) {
	case 1:
		for p := range pins {
			return p, nil
		}
	case 0:
		read := "no " + manifest + " was found in the roots or above them up to the config directory"
		if len(files) > 0 {
			rels := make([]string, len(files))
			for i, f := range files {
				rels[i] = relTo(dir, f)
			}
			read = "read " + strings.Join(rels, ", ")
		}
		return schemaPin{}, &BuildError{
			What:  "no pinned version of " + dep + " was found (" + read + ").",
			Why:   "schema.module is set in .stringly.yaml, so the catalog needs the contract version the repository pins.",
			Where: "catalog build, schema version step, " + manifest + ".",
			Fix:   missingFix,
		}
	}
	var parts []string
	for p, fs := range pins {
		parts = append(parts, p.String()+" in "+strings.Join(fs, ", "))
	}
	sort.Strings(parts)
	return schemaPin{}, &BuildError{
		What:  "the roots pin different versions of " + dep + ": " + strings.Join(parts, "; ") + ".",
		Why:   "one catalog is built from one contract version; two pins would make the result depend on which root is read.",
		Where: "catalog build, schema version step, " + manifest + ".",
		Fix:   "pin the same version in every " + manifest + " under the roots.",
	}
}

func relTo(dir, p string) string {
	if r, err := filepath.Rel(dir, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}
