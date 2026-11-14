package catalog

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// goPackageKey identifies one Go package: its directory, relative to the
// harvested base, and its package name.
type goPackageKey struct {
	dir  string
	name string
}

type goConstant struct {
	typ   string
	name  string
	value string
	file  string
	line  int
}

// harvestGo parses the non-test .go files under roots (relative to base) with
// go/parser and returns the members of every Go typed string set: a named type
// whose underlying type is `string`, with two or more distinct non-empty
// constant values in the non-test files of one package. A constant is typed
// by its declaration (`A T = "a"`) or by a conversion (`A = T("a")`).
//
// Directories named vendor, testdata or node_modules, and those starting with
// "." or "_", are skipped, as the go command skips them. With contractOnly,
// packages under an internal/ or cmd/ directory are skipped as well: they are
// not part of an importable contract.
func harvestGo(base string, roots []string, contractOnly bool) ([]Member, error) {
	stringTypes := map[goPackageKey]map[string]bool{}
	constants := map[goPackageKey][]goConstant{}
	fset := token.NewFileSet()

	for _, root := range roots {
		start := filepath.Join(base, filepath.FromSlash(root))
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(base, p)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if p != start && skipGoDir(d.Name(), contractOnly) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if perr != nil {
				return &BuildError{
					What:  "a Go file could not be parsed while building the catalog: " + rel + ".",
					Why:   perr.Error(),
					Where: "catalog build, Go typed-constant step, " + rel + ".",
					Fix:   "fix the syntax error (go vet reports it), or exclude the directory from go.roots in .unstring.yaml.",
					Err:   perr,
				}
			}
			key := goPackageKey{dir: path.Dir(rel), name: file.Name.Name}
			collectGoDecls(fset, file, rel, key, stringTypes, constants)
			return nil
		})
		if err != nil {
			var be *BuildError
			if errors.As(err, &be) {
				return nil, be
			}
			return nil, &BuildError{
				What:  "the Go root " + root + " could not be read while building the catalog.",
				Why:   err.Error(),
				Where: "catalog build, Go typed-constant step, under " + base + ".",
				Fix:   "check that go.roots in .unstring.yaml names existing directories relative to the config, and that they are readable.",
				Err:   err,
			}
		}
	}

	keys := make([]goPackageKey, 0, len(constants))
	for k := range constants {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].dir != keys[j].dir {
			return keys[i].dir < keys[j].dir
		}
		return keys[i].name < keys[j].name
	})
	var members []Member
	for _, k := range keys {
		byType := map[string][]goConstant{}
		for _, c := range constants[k] {
			if stringTypes[k][c.typ] && c.value != "" {
				byType[c.typ] = append(byType[c.typ], c)
			}
		}
		for typ, cs := range byType {
			distinct := map[string]bool{}
			for _, c := range cs {
				distinct[c.value] = true
			}
			if len(distinct) < 2 {
				continue
			}
			for _, c := range cs {
				members = append(members, Member{
					Value:  c.value,
					Set:    k.name + "." + typ,
					Name:   c.name,
					Source: SourceGoTypedConstant,
					Site:   DefinitionSite{File: c.file, Line: c.line},
				})
			}
		}
	}
	return members, nil
}

func skipGoDir(name string, contractOnly bool) bool {
	if name == "vendor" || name == "testdata" || name == "node_modules" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	return contractOnly && (name == "internal" || name == "cmd")
}

func collectGoDecls(fset *token.FileSet, file *ast.File, rel string, key goPackageKey,
	stringTypes map[goPackageKey]map[string]bool, constants map[goPackageKey][]goConstant) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gd.Tok {
		case token.TYPE:
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if ts.Assign.IsValid() || ts.TypeParams != nil {
					continue
				}
				if id, ok := ts.Type.(*ast.Ident); ok && id.Name == "string" {
					if stringTypes[key] == nil {
						stringTypes[key] = map[string]bool{}
					}
					stringTypes[key][ts.Name.Name] = true
				}
			}
		case token.CONST:
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) || name.Name == "_" {
						continue
					}
					typ, lit := typedStringConstant(vs.Type, vs.Values[i])
					if lit == nil {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					constants[key] = append(constants[key], goConstant{
						typ:   typ,
						name:  name.Name,
						value: value,
						file:  rel,
						line:  fset.Position(lit.Pos()).Line,
					})
				}
			}
		}
	}
}

// typedStringConstant returns the constant's type name and its string literal
// for `A T = "a"` and `A = T("a")`, and a nil literal for anything else.
func typedStringConstant(declared ast.Expr, value ast.Expr) (string, *ast.BasicLit) {
	if id, ok := declared.(*ast.Ident); ok {
		if lit, ok := value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return id.Name, lit
		}
		return "", nil
	}
	if declared != nil {
		return "", nil
	}
	call, ok := value.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", nil
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok {
		return "", nil
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", nil
	}
	return fn.Name, lit
}
