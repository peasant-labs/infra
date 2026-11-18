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
}

// ScanGo loads each configured root and checks its Go packages, including tests.
func ScanGo(ctx context.Context, dir string, roots, env []string) ([]Finding, error) {
	var result []Finding
	seen := map[string]bool{}
	for _, root := range roots {
		rootDir := filepath.Join(dir, filepath.FromSlash(root))
		cfg := &packages.Config{Context: ctx, Dir: rootDir, Env: env, Tests: true, Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo}
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
			if pkg.TypesInfo == nil {
				continue
			}
			check := func(receiver, literal ast.Expr) {
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
				member := ""
				count := 0
				for _, name := range scope.Names() {
					c, ok := scope.Lookup(name).(*types.Const)
					if !ok || !types.Identical(types.Unalias(c.Type()), named) || c.Val().Kind() != constant.String || constant.StringVal(c.Val()) != value {
						continue
					}
					count++
					member = name
				}
				if count != 1 {
					return
				}
				pos := pkg.Fset.Position(lit.Pos())
				rel, err := filepath.Rel(dir, pos.Filename)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return
				}
				rel = filepath.ToSlash(rel)
				key := fmt.Sprintf("%s:%d:%d", rel, pos.Line, pos.Column)
				if seen[key] {
					return
				}
				seen[key] = true
				result = append(result, Finding{Hit: rules.Hit{Rule: GoClosedSetLiteral, File: rel, Line: pos.Line, Text: lit.Value}, Set: obj.Name(), Member: member, Literal: value})
			}
			for _, file := range pkg.Syntax {
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
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].Hit, result[j].Hit
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Text < b.Text
	})
	return result, nil
}
