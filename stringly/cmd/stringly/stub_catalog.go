//go:build stringly_stub

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/peasant-labs/infra/stringly/internal/catalog"
	"github.com/peasant-labs/infra/stringly/internal/rules"
)

// stubBuilder harvests a small catalog with regular expressions: closed()
// calls and type-alias unions in TS, typed string constants in Go, and the
// same from the pinned schema module, resolved with `go mod download`.
type stubBuilder struct{}

var (
	stubClosedCall = regexp.MustCompile(`closed\(\s*["']([^"']+)["']`)
	stubTypeAlias  = regexp.MustCompile(`type\s+(\w+)\s*=\s*(.*)`)
	stubQuoted     = regexp.MustCompile(`["']([^"']+)["']`)
	stubGoType     = regexp.MustCompile(`(?m)^type\s+(\w+)\s+string\s*$`)
	stubGoConst    = regexp.MustCompile(`(?m)^\s*(\w+)\s+(\w+)\s*=\s*"([^"]*)"`)
	stubTSVersion  = regexp.MustCompile(`"@peasant-labs/schema"\s*:\s*"([^"]+)"`)
)

func (stubBuilder) Build(ctx context.Context, req catalog.BuildRequest) (catalog.Catalog, error) {
	cat := catalog.Catalog{Lang: req.Lang}
	for _, root := range req.Roots {
		ms, err := stubHarvest(req.Dir, root, req.Lang, "")
		if err != nil {
			return cat, err
		}
		cat.Members = append(cat.Members, ms...)
	}
	if req.SchemaModule == "" {
		return cat, nil
	}
	version, err := stubSchemaVersion(req)
	if err != nil {
		return cat, err
	}
	cmd := exec.CommandContext(ctx, "go", "mod", "download", "-json", req.SchemaModule+"@"+version)
	cmd.Dir, cmd.Env = req.Dir, req.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var info struct{ Dir, Error string }
	_ = json.Unmarshal(stdout.Bytes(), &info)
	if runErr != nil || info.Dir == "" {
		return cat, fmt.Errorf("go mod download -json %s@%s failed: %v %s %s", req.SchemaModule, version, runErr, info.Error, strings.TrimSpace(stderr.String()))
	}
	module := req.SchemaModule + "@" + version
	var schema []catalog.Member
	ms, err := stubHarvest(info.Dir, ".", rules.LangGo, module)
	if err != nil {
		return cat, err
	}
	schema = append(schema, ms...)
	if req.Lang == rules.LangTS {
		ms, err := stubHarvest(info.Dir, "typescript/src/internal/generated", rules.LangTS, module)
		if err != nil {
			return cat, err
		}
		schema = append(schema, ms...)
	}
	if len(schema) == 0 {
		return cat, fmt.Errorf("the schema module %s declares no set", module)
	}
	cat.Members = append(cat.Members, schema...)
	return cat, nil
}

func stubSchemaVersion(req catalog.BuildRequest) (string, error) {
	if req.Lang == rules.LangGo {
		data, err := os.ReadFile(filepath.Join(req.Dir, "go.mod"))
		if err != nil {
			return "", err
		}
		m := regexp.MustCompile(regexp.QuoteMeta(req.SchemaModule) + `\s+(v\S+)`).FindSubmatch(data)
		if m == nil {
			return "", fmt.Errorf("go.mod does not require %s", req.SchemaModule)
		}
		return string(m[1]), nil
	}
	for _, root := range req.Roots {
		data, err := os.ReadFile(filepath.Join(req.Dir, root, "package.json"))
		if err != nil {
			continue
		}
		if m := stubTSVersion.FindSubmatch(data); m != nil {
			v := string(m[1])
			if strings.ContainsAny(v, "^~<>*| ") {
				return "", fmt.Errorf("@peasant-labs/schema is pinned to the range %q", v)
			}
			return "v" + v, nil
		}
	}
	return "", fmt.Errorf("no package.json under the TS roots pins @peasant-labs/schema")
}

func stubHarvest(base, root string, lang rules.Lang, module string) ([]catalog.Member, error) {
	var out []catalog.Member
	err := filepath.WalkDir(filepath.Join(base, root), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" || (module != "" && (d.Name() == "internal" && lang == rules.LangGo || d.Name() == "cmd")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		switch {
		case lang == rules.LangGo && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go"):
			out = append(out, stubGoMembers(string(data), rel, module)...)
		case lang == rules.LangTS && regexp.MustCompile(`\.(ts|tsx|js|jsx|mjs|cjs)$`).MatchString(p):
			out = append(out, stubTSMembers(string(data), rel, module)...)
		}
		return nil
	})
	return out, err
}

func stubSource(module string, k catalog.SourceKind) catalog.SourceKind {
	if module != "" {
		return catalog.SourceSchemaModule
	}
	return k
}

func stubTSMembers(src, file, module string) []catalog.Member {
	var out []catalog.Member
	for i, line := range strings.Split(src, "\n") {
		site := catalog.DefinitionSite{Module: module, File: file, Line: i + 1}
		for _, m := range stubClosedCall.FindAllStringSubmatch(line, -1) {
			out = append(out, catalog.Member{Value: m[1], Source: stubSource(module, catalog.SourceClosedCall), Site: site})
		}
		if m := stubTypeAlias.FindStringSubmatch(line); m != nil && strings.Contains(m[2], "|") {
			for _, q := range stubQuoted.FindAllStringSubmatch(m[2], -1) {
				out = append(out, catalog.Member{Value: q[1], Set: m[1], Source: stubSource(module, catalog.SourceTSUnion), Site: site})
			}
		}
	}
	return out
}

func stubGoMembers(src, file, module string) []catalog.Member {
	types := map[string]bool{}
	for _, m := range stubGoType.FindAllStringSubmatch(src, -1) {
		types[m[1]] = true
	}
	byType := map[string][]catalog.Member{}
	for i, line := range strings.Split(src, "\n") {
		if m := stubGoConst.FindStringSubmatch(line); m != nil && types[m[2]] && m[3] != "" {
			byType[m[2]] = append(byType[m[2]], catalog.Member{
				Value: m[3], Set: m[2], Source: stubSource(module, catalog.SourceGoTypedConstant),
				Site: catalog.DefinitionSite{Module: module, File: file, Line: i + 1},
			})
		}
	}
	var out []catalog.Member
	for _, ms := range byType {
		if len(ms) >= 2 {
			out = append(out, ms...)
		}
	}
	return out
}
