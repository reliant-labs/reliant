package codegen

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// composeSyncWrapping brings each component construction already in compose.go
// in line with the instrument decision (ShouldInstrumentComponent), which the
// additive reconcile passes never revisit once a component is wired:
//
//   - an instrumented component assigned unwrapped — `c.X = pkg.New(...)`, or
//     `c.X = xInst` in the fallible shape — is wrapped in its decorator:
//     `c.X = pkg.NewServiceWithForgeMiddleware(pkg.New(...))`;
//   - a component that is no longer instrumented (it gained
//     `// forge:no-observe`, so its middleware_gen.go is gone) has the wrapper
//     call removed, keeping its argument.
//
// Without the first, making instrumentation the default would have left every
// existing project's components exactly as untraced as before: the decision
// changed, the file did not. Without the second, opting out broke the build on
// the removed wrapper.
//
// Only `c.<Field> = …` assignments inside NewComponents are edited, located by
// parsing the file; a file that does not parse, or has no NewComponents, is
// returned unchanged. The result must gofmt, or the input is returned.
func composeSyncWrapping(src string, data InjectGenData) (string, bool) {
	byField := make(map[string]InjectComponentData, len(data.Order))
	for _, c := range data.Order {
		byField[c.FieldName] = c
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "compose.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return src, false
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "NewComponents" {
			body = fn.Body
		}
	}
	if body == nil {
		return src, false
	}

	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	text := func(n ast.Node) string {
		return src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset]
	}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || as.Tok != token.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		sel, ok := as.Lhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "c" {
			return true
		}
		c, known := byField[sel.Sel.Name]
		if !known {
			return true
		}
		rhs := as.Rhs[0]
		inner, wrappedNow := wrapperArg(rhs, c.Alias)
		switch {
		case c.Wrapped && c.MiddlewareCtor != "" && !wrappedNow:
			edits = append(edits, edit{
				start: fset.Position(rhs.Pos()).Offset,
				end:   fset.Position(rhs.End()).Offset,
				text:  c.Alias + "." + c.MiddlewareCtor + "(" + text(rhs) + ")",
			})
		case !c.Wrapped && wrappedNow:
			edits = append(edits, edit{
				start: fset.Position(rhs.Pos()).Offset,
				end:   fset.Position(rhs.End()).Offset,
				text:  text(inner),
			})
		}
		return true
	})
	if len(edits) == 0 {
		return src, false
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := src
	for _, e := range edits {
		out = out[:e.start] + e.text + out[e.end:]
	}
	formatted, err := format.Source([]byte(out))
	if err != nil {
		return src, false
	}
	return string(formatted), true
}

// wrapperArg reports whether expr is a call to a generated decorator
// constructor of package alias — `alias.New…WithForgeMiddleware(x)` — and
// returns its argument x.
func wrapperArg(expr ast.Expr, alias string) (ast.Expr, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != alias {
		return nil, false
	}
	if !strings.HasSuffix(sel.Sel.Name, "WithForgeMiddleware") {
		return nil, false
	}
	return call.Args[0], true
}
