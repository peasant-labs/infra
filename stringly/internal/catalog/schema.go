package catalog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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

// pinnedSchemaVersion returns the version of the schema module the repository
// pins for req.Lang: the go.mod requirement for Go, and the exact
// SchemaPackage version in package.json, as v<version>, for TS.
func pinnedSchemaVersion(req BuildRequest) (string, error) {
	switch req.Lang {
	case rules.LangGo:
		return goModPin(req.Dir, req.Roots, req.SchemaModule)
	case rules.LangTS:
		return packageJSONPin(req.Dir, req.Roots)
	}
	return "", fmt.Errorf("no schema pin for language %q", req.Lang)
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

// goModPin reads the requirement on module from the go.mod of the Go roots.
func goModPin(dir string, roots []string, module string) (string, error) {
	files := manifestFiles(dir, roots, "go.mod")
	versions := map[string][]string{}
	for _, f := range files {
		v, err := goModRequirement(f, module)
		if err != nil {
			return "", &BuildError{
				What:  "the go.mod at " + relTo(dir, f) + " could not be read.",
				Why:   err.Error(),
				Where: "catalog build, schema version step (Go).",
				Fix:   "fix the go.mod (go mod tidy reports the problem).",
				Err:   err,
			}
		}
		if v != "" {
			versions[v] = append(versions[v], relTo(dir, f))
		}
	}
	return singlePin(versions, "go.mod", module, dir, files,
		"add the requirement with go get "+module+"@<version>, or remove schema.module from .stringly.yaml if the Go code does not use the contract.")
}

// goModRequirement returns the version module is required at, or "".
func goModRequirement(file, module string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	inBlock := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case inBlock && fields[0] == ")":
			inBlock = false
			continue
		case !inBlock && fields[0] == "require" && len(fields) >= 2 && fields[1] == "(":
			inBlock = true
			continue
		case !inBlock && fields[0] == "require":
			fields = fields[1:]
		case !inBlock:
			continue
		}
		if len(fields) >= 2 && strings.Trim(fields[0], `"`) == module {
			return fields[1], nil
		}
	}
	return "", sc.Err()
}

// exactVersion is an npm version with no range operator.
var exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// packageJSONPin reads the exact SchemaPackage version from the package.json
// of the TS roots.
func packageJSONPin(dir string, roots []string) (string, error) {
	files := manifestFiles(dir, roots, "package.json")
	versions := map[string][]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", &BuildError{
				What: "the package.json at " + relTo(dir, f) + " could not be read.", Why: err.Error(),
				Where: "catalog build, schema version step (TS).", Fix: "check the file's permissions.", Err: err,
			}
		}
		var pkg map[string]json.RawMessage
		if err := json.Unmarshal(data, &pkg); err != nil {
			return "", &BuildError{
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
				return "", &BuildError{
					What: fmt.Sprintf("%s is pinned as %q in %s (%s), which is not an exact version.", SchemaPackage, spec, relTo(dir, f), field),
					Why: "the catalog reads the contract's closed sets from one exact schema version; a range or a non-registry " +
						"specifier would let the catalog change without a change in the repository.",
					Where: "catalog build, schema version step (TS).",
					Fix:   fmt.Sprintf("pin an exact version, for example \"%s\": \"%s\".", SchemaPackage, strings.TrimLeft(spec, "^~>=<v ")),
				}
			}
			versions["v"+spec] = append(versions["v"+spec], relTo(dir, f))
			break
		}
	}
	return singlePin(versions, "package.json", SchemaPackage, dir, files,
		"add an exact dependency on "+SchemaPackage+" to the TS root's package.json, or remove schema.module from .stringly.yaml if the TS code does not use the contract.")
}

func singlePin(versions map[string][]string, manifest, dep, dir string, files []string, missingFix string) (string, error) {
	switch len(versions) {
	case 1:
		for v := range versions {
			return v, nil
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
		return "", &BuildError{
			What:  "no pinned version of " + dep + " was found (" + read + ").",
			Why:   "schema.module is set in .stringly.yaml, so the catalog needs the contract version the repository pins.",
			Where: "catalog build, schema version step, " + manifest + ".",
			Fix:   missingFix,
		}
	}
	var parts []string
	for v, fs := range versions {
		parts = append(parts, v+" in "+strings.Join(fs, ", "))
	}
	sort.Strings(parts)
	return "", &BuildError{
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
