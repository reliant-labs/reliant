// File: internal/codegen/middleware_name.go
//
// The SINGLE source of truth for the generated in-process middleware
// decorator's identity (middleware_gen.go): the exact constructor + struct
// names and the op-namespace segment its wrapper methods stamp on spans and
// metrics. Both the generator (via generate_middleware, which passes the
// resolved names into contract.WriteObservedDecorator) and the compose
// assembler (which emits `<alias>.<Constructor>(<alias>.New(...))`) resolve the
// name HERE, so the emitted call is byte-identical to the generated declaration
// — a mismatch would be `undefined: <alias>.<Constructor>` at
// generate-validation. The contract generator lives in a package that must not
// import codegen (import cycle), so it consumes this resolver's output through
// the CLI rather than calling it directly; there is still exactly ONE
// implementation, so the two can never drift.
//
// THE NAME IS A PUBLIC API, SO IT MUST BE STABLE. Hand-owned code calls the
// wrapper constructor directly — an owned compose.go, a providers.go that
// builds an adapter onto Infra, a hand-written wire file. So the name may only
// be derived from what the package DECLARES, never from how its code happens to
// be written. It used to be keyed off `New`'s return EXPRESSION (`return
// &service{…}` → NewServiceWithForgeMiddleware); refactoring New to return via
// a helper (`return newService(deps), nil`) silently renamed the wrapper to
// NewWithForgeMiddleware and broke every caller on the next generate.
//
// Resolution, in order:
//
//  1. PINNED — the name middleware_gen.go ALREADY declares for this contract.
//     Once generated, a wrapper keeps its name for the life of the package,
//     whatever it was derived from, so no refactor and no forge upgrade can
//     rename a symbol someone calls.
//  2. DECLARED — `New<Iface>WithForgeMiddleware`, from the contract interface
//     the component's constructor returns (`Service` → the canonical
//     NewServiceWithForgeMiddleware).

package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MiddlewareWrapper is the resolved identity of one component's generated
// middleware decorator.
type MiddlewareWrapper struct {
	// Constructor is the exported wrapper constructor, e.g.
	// "NewServiceWithForgeMiddleware". Compose emits `<alias>.<Constructor>(…)`;
	// the generator declares `func <Constructor>(inner <Iface>) <Iface>`.
	Constructor string
	// Struct is the unexported decorator struct, e.g. "forgeMiddlewareService".
	Struct string
}

// middlewareFileName is the generated decorator's file (contract.observedFileName).
const middlewareFileName = "middleware_gen.go"

// wrapperCtorRE matches a generated wrapper constructor name.
var wrapperCtorRE = regexp.MustCompile(`^New\w*WithForgeMiddleware$`)

// ResolveMiddlewareWrapper returns the generated middleware decorator's identity
// for the component package at dir whose constructor returns the wrapped
// interface ifaceName. This is what BOTH the generate walk (which passes the
// result into contract.WriteObservedDecorator) and the compose assembler call,
// so the emitted constructor call matches the generated declaration exactly.
func ResolveMiddlewareWrapper(dir, ifaceName string) MiddlewareWrapper {
	if ifaceName == "" {
		ifaceName = "Service"
	}
	if pinned, ok := pinnedMiddlewareWrapper(dir, ifaceName); ok {
		return pinned
	}
	seg := upperFirst(ifaceName)
	return MiddlewareWrapper{
		Constructor: "New" + seg + "WithForgeMiddleware",
		Struct:      "forgeMiddleware" + seg,
	}
}

// pinnedMiddlewareWrapper reads the wrapper identity an existing
// middleware_gen.go in dir already declares for ifaceName: an exported
// `New…WithForgeMiddleware(inner <ifaceName>) <ifaceName>` func, and the
// decorator struct its body constructs. ok is false when the file is absent,
// unparseable, or declares no wrapper over ifaceName (the contract was
// renamed) — the caller then derives the declared name.
func pinnedMiddlewareWrapper(dir, ifaceName string) (MiddlewareWrapper, bool) {
	src, err := os.ReadFile(filepath.Join(dir, middlewareFileName))
	if err != nil {
		return MiddlewareWrapper{}, false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, middlewareFileName, src, parser.SkipObjectResolution)
	if err != nil {
		return MiddlewareWrapper{}, false
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil || !wrapperCtorRE.MatchString(fn.Name.Name) {
			continue
		}
		params := fn.Type.Params
		if params == nil || len(params.List) != 1 || printType(fset, params.List[0].Type) != ifaceName {
			continue
		}
		structName := wrapperStructFromBody(fn.Body)
		if structName == "" {
			structName = "forgeMiddleware" + strings.TrimSuffix(strings.TrimPrefix(fn.Name.Name, "New"), "WithForgeMiddleware")
		}
		return MiddlewareWrapper{Constructor: fn.Name.Name, Struct: structName}, true
	}
	return MiddlewareWrapper{}, false
}

// wrapperStructFromBody returns the decorator struct a generated wrapper
// constructor returns (`return &forgeMiddlewareX{…}` → "forgeMiddlewareX").
func wrapperStructFromBody(body *ast.BlockStmt) string {
	if body == nil {
		return ""
	}
	for _, stmt := range body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		expr := ret.Results[0]
		if u, ok := expr.(*ast.UnaryExpr); ok && u.Op == token.AND {
			expr = u.X
		}
		if cl, ok := expr.(*ast.CompositeLit); ok {
			if id, ok := cl.Type.(*ast.Ident); ok {
				return id.Name
			}
		}
	}
	return ""
}
