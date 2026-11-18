package typed

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"regexp"
	"sort"

	"github.com/peasant-labs/infra/unstring/internal/rules"
	"golang.org/x/tools/go/packages"
)

// GoMountLiteral is intentionally not a structural rule or baseline key.
const GoMountLiteral rules.RuleID = "go-typed-mount-literal"

// mountMethods maps each route-mount method to the index of its pattern
// argument. It is chi v5's Router method set that takes a pattern, which also
// covers net/http's ServeMux (Handle, HandleFunc). Use, With, Group, NotFound
// and MethodNotAllowed take no pattern and are not mounts.
var mountMethods = map[string]int{
	"Connect": 0, "Delete": 0, "Get": 0, "Head": 0, "Options": 0,
	"Patch": 0, "Post": 0, "Put": 0, "Query": 0, "Trace": 0,
	"Handle": 0, "HandleFunc": 0, "Route": 0, "Mount": 0,
	"Method": 1, "MethodFunc": 1,
}

// mountPattern is the shape of a route pattern: an absolute path, optionally
// led by an HTTP method as net/http's ServeMux accepts. It keeps a string
// argument of a same-named method that is not a pattern, such as
// http.Header.Get("Accept"), out when a configured package has one.
var mountPattern = regexp.MustCompile(`^([A-Z]+ )?/`)

// MountOptions configures the mount pass.
type MountOptions struct {
	// Routers are the import paths of the router packages. A call is a mount
	// when its method is declared in one of them, on a concrete type or an
	// interface, including a method promoted through an embedded field.
	Routers []string
	// IncludeTests scans `_test.go` files too.
	IncludeTests bool
}

// MountFinding is a route mounted from a literal pattern.
type MountFinding struct {
	Hit rules.Hit
	// Receiver is the static type of the call's receiver, for example
	// "*chi.Mux" or "chi.Router".
	Receiver string
	// Method is the mount method, for example "Get" or "Route".
	Method string
	// Pattern is the literal's value, exactly as mounted at this call. A
	// pattern inside a Route group is relative to that group's prefix; the
	// pass never composes the full path.
	Pattern string
}

// Message names the call and the literal.
func (f MountFinding) Message() string {
	return fmt.Sprintf("(%s).%s mounts the route pattern %s as a literal", f.Receiver, f.Method, f.Hit.Text)
}

// MountLiterals reports calls of a route-mount method on a configured router
// whose pattern argument is a string literal, or a concatenation of string
// literals, shaped like a route pattern. A pattern built from a constant,
// variable or call is not reported: that is the fixed form, or a wrapper
// forwarding its parameter, and the literal is reported where it is written.
func (p *Program) MountLiterals(opts MountOptions) ([]MountFinding, error) {
	routers := map[string]bool{}
	for _, r := range opts.Routers {
		routers[r] = true
	}
	loaded := map[string]bool{}
	packages.Visit(p.pkgs, nil, func(pkg *packages.Package) {
		if routers[pkg.PkgPath] {
			loaded[pkg.PkgPath] = true
		}
	})
	for _, r := range opts.Routers {
		if !loaded[r] {
			return nil, fmt.Errorf("the router package %s is not imported by any Go package under the roots, so the mount pass would match nothing; correct the router's import path in the config, or remove it", r)
		}
	}
	qualifier := func(pkg *types.Package) string { return pkg.Name() }
	var result []MountFinding
	seen := map[string]bool{}
	p.eachFile(opts.IncludeTests, func(pkg *packages.Package, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			index, ok := mountMethods[sel.Sel.Name]
			if !ok || index >= len(call.Args) {
				return true
			}
			selection := pkg.TypesInfo.Selections[sel]
			if selection == nil || selection.Kind() != types.MethodVal {
				return true
			}
			fn, ok := selection.Obj().(*types.Func)
			if !ok || fn.Pkg() == nil || !routers[fn.Pkg().Path()] {
				return true
			}
			// A mount takes its pattern as a string followed by the handler or
			// group callback, which keeps a same-named one-argument method such as
			// (*http.Client).Get(url) out.
			params := fn.Type().(*types.Signature).Params()
			if params.Len() <= index+1 {
				return true
			}
			if basic, ok := params.At(index).Type().Underlying().(*types.Basic); !ok || basic.Kind() != types.String {
				return true
			}
			arg := call.Args[index]
			if !literalOnly(arg) {
				return true
			}
			tv, ok := pkg.TypesInfo.Types[arg]
			if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
				return true
			}
			value := constant.StringVal(tv.Value)
			if !mountPattern.MatchString(value) {
				return true
			}
			rel, pos, ok := p.site(pkg, arg.Pos())
			if !ok {
				return true
			}
			key := fmt.Sprintf("%s:%d:%d", rel, pos.Line, pos.Column)
			if seen[key] {
				return true
			}
			seen[key] = true
			result = append(result, MountFinding{
				Hit:      rules.Hit{Rule: GoMountLiteral, File: rel, Line: pos.Line, Text: types.ExprString(arg)},
				Receiver: types.TypeString(pkg.TypesInfo.TypeOf(sel.X), qualifier),
				Method:   sel.Sel.Name,
				Pattern:  value,
			})
			return true
		})
	})
	sort.Slice(result, func(i, j int) bool {
		if result[i].Hit != result[j].Hit {
			return hitLess(result[i].Hit, result[j].Hit)
		}
		return result[i].Method < result[j].Method
	})
	return result, nil
}

// literalOnly reports whether expr is a string literal, or parenthesized
// literals joined with +, with no identifier in it.
func literalOnly(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.ParenExpr:
		return literalOnly(e.X)
	case *ast.BinaryExpr:
		return e.Op == token.ADD && literalOnly(e.X) && literalOnly(e.Y)
	}
	return false
}
