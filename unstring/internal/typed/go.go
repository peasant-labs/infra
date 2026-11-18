// Package typed implements type-aware checks independent of structural baselines.
package typed

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/unstring/internal/rules"
	"golang.org/x/tools/go/packages"
)

// GoClosedSetLiteral is intentionally not a structural rule or baseline key.
const GoClosedSetLiteral rules.RuleID = "go-typed-closed-set-literal"

// Finding associates a source hit with the matching type and constant.
type Finding struct {
	Hit                  rules.Hit
	Set, Member, Literal string
	Replacement          string
	InaccessibleReason   string
}

// Message provides a legal replacement when one is available at the site.
func (f Finding) Message() string {
	if f.Replacement != "" {
		return fmt.Sprintf("use %s instead of '%s'", f.Replacement, f.Literal)
	}
	return fmt.Sprintf("constant %s for '%s' is %s", f.Member, f.Literal, f.InaccessibleReason)
}

func replacement(pkg *packages.Package, file *ast.File, pos token.Pos, member *types.Const) (string, string) {
	scope := pkg.TypesInfo.Scopes[file]
	if scope != nil {
		scope = scope.Innermost(pos)
	}
	if scope == nil {
		scope = pkg.Types.Scope()
	}
	resolves := func(name string, want types.Object) bool {
		_, found := scope.LookupParent(name, pos)
		return found == want
	}
	if member.Pkg() == pkg.Types {
		if resolves(member.Name(), member) {
			return member.Name(), ""
		}
		return "", "shadowed at this site; rename the shadowing identifier or move the comparison"
	}
	if !member.Exported() {
		return "", fmt.Sprintf("in package %s and not accessible here; export it or move the comparison", member.Pkg().Path())
	}
	for _, imp := range file.Imports {
		var binding *types.PkgName
		if imp.Name != nil {
			binding, _ = pkg.TypesInfo.Defs[imp.Name].(*types.PkgName)
		} else {
			binding, _ = pkg.TypesInfo.Implicits[imp].(*types.PkgName)
		}
		if binding == nil || binding.Imported().Path() != member.Pkg().Path() {
			continue
		}
		if binding.Name() == "." {
			if resolves(member.Name(), member) {
				return member.Name(), ""
			}
			return "", "shadowed at this site; rename the shadowing identifier or move the comparison"
		}
		if binding.Name() != "_" && resolves(binding.Name(), binding) {
			return binding.Name() + "." + member.Name(), ""
		}
	}
	return "", fmt.Sprintf("in package %s and not accessible here; import it under an unshadowed name or move the comparison", member.Pkg().Path())
}

// Program is the Go packages under the configured roots, loaded once with
// type information and shared by every typed pass.
type Program struct {
	dir  string
	pkgs []*packages.Package
}

// LoadGo loads each configured root with its test packages. A package error
// fails the load: a pass over a tree that does not type-check would be silent
// exactly where the types are missing.
func LoadGo(ctx context.Context, dir string, roots, env []string) (*Program, error) {
	prog := &Program{dir: dir}
	for _, root := range roots {
		rootDir := filepath.Join(dir, filepath.FromSlash(root))
		cfg := &packages.Config{Context: ctx, Dir: rootDir, Env: env, Tests: true, Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedForTest | packages.NeedImports | packages.NeedDeps}
		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			return nil, fmt.Errorf("load Go packages under %s: %w", root, err)
		}
		if len(pkgs) == 0 {
			return nil, fmt.Errorf("no Go packages found under %s; check the root and Go module", root)
		}
		for _, pkg := range pkgs {
			if len(pkg.Errors) > 0 {
				return nil, fmt.Errorf("load Go packages under %s: %s; fix the package errors before enabling the typed pass", root, pkg.Errors[0])
			}
		}
		prog.pkgs = append(prog.pkgs, pkgs...)
	}
	return prog, nil
}

// eachFile calls fn for every syntax file a pass scans, once per package
// variant that owns it. A file is classified by its physical name, so a
// `//line` directive cannot move it between variants: production files are
// scanned only in the normal package and `_test.go` files only in test
// variants. With includeTests false, `_test.go` files are skipped.
func (p *Program) eachFile(includeTests bool, fn func(pkg *packages.Package, file *ast.File)) {
	for _, pkg := range p.pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			isTest := strings.HasSuffix(filepath.Base(pkg.Fset.PositionFor(file.Pos(), false).Filename), "_test.go")
			if isTest != (pkg.ForTest != "") || (isTest && !includeTests) {
				continue
			}
			fn(pkg, file)
		}
	}
}

// site returns the physical location of pos relative to the config
// directory, and false when it lies outside it. Directives cannot hide or
// merge distinct sites or point outside the scanned tree.
func (p *Program) site(pkg *packages.Package, pos token.Pos) (string, token.Position, bool) {
	at := pkg.Fset.PositionFor(pos, false)
	rel, err := filepath.Rel(p.dir, at.Filename)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", at, false
	}
	return filepath.ToSlash(rel), at, true
}

// ScanGo loads each configured root and runs the closed-set-literal pass.
func ScanGo(ctx context.Context, dir string, roots, env []string) ([]Finding, error) {
	prog, err := LoadGo(ctx, dir, roots, env)
	if err != nil {
		return nil, err
	}
	return prog.ClosedSetLiterals(), nil
}

// ClosedSetLiterals reports comparisons and switch cases whose string
// literal names exactly one constant of the operand's named string type.
func (p *Program) ClosedSetLiterals() []Finding {
	var result []Finding
	seen := map[string]bool{}
	p.eachFile(true, func(pkg *packages.Package, file *ast.File) {
		check := func(receiver, literal ast.Expr) {
			for {
				paren, ok := literal.(*ast.ParenExpr)
				if !ok {
					break
				}
				literal = paren.X
			}
			lit, ok := literal.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return
			}
			typ := pkg.TypesInfo.TypeOf(receiver)
			if typ == nil {
				return
			}
			named, ok := types.Unalias(typ).(*types.Named)
			if !ok {
				return
			}
			basic, ok := named.Underlying().(*types.Basic)
			if !ok || basic.Kind() != types.String {
				return
			}
			obj := named.Obj()
			if obj.Pkg() == nil {
				return
			}
			scope := obj.Pkg().Scope()
			var member *types.Const
			count := 0
			for _, name := range scope.Names() {
				c, ok := scope.Lookup(name).(*types.Const)
				if !ok || !types.Identical(types.Unalias(c.Type()), named) || c.Val().Kind() != constant.String || constant.StringVal(c.Val()) != value {
					continue
				}
				count++
				member = c
			}
			if count != 1 {
				return
			}
			rel, pos, ok := p.site(pkg, lit.Pos())
			if !ok {
				return
			}
			key := fmt.Sprintf("%s:%d:%d", rel, pos.Line, pos.Column)
			if seen[key] {
				return
			}
			seen[key] = true
			repl, reason := replacement(pkg, file, lit.Pos(), member)
			result = append(result, Finding{Hit: rules.Hit{Rule: GoClosedSetLiteral, File: rel, Line: pos.Line, Text: lit.Value}, Set: obj.Name(), Member: member.Name(), Literal: value, Replacement: repl, InaccessibleReason: reason})
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BinaryExpr:
				if n.Op == token.EQL || n.Op == token.NEQ {
					check(n.X, n.Y)
					check(n.Y, n.X)
				}
			case *ast.SwitchStmt:
				if n.Tag != nil {
					for _, stmt := range n.Body.List {
						for _, expr := range stmt.(*ast.CaseClause).List {
							check(n.Tag, expr)
						}
					}
				}
			}
			return true
		})
	})
	sort.Slice(result, func(i, j int) bool { return hitLess(result[i].Hit, result[j].Hit) })
	return result
}

func hitLess(a, b rules.Hit) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Text < b.Text
}
