package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// CRUD op demand: which generated ops (and conversion helpers) a handler
// package actually needs.
//
// handlers_crud_ops_gen.go is Tier-1 — regenerated every run — and the
// user-owned code calls into it: the scaffolded shims call
// s.crud<Rpc>Op(), custom RPCs project rows through <entity>ToProto. So
// what the ops file emits has to follow what that code REFERENCES, never
// where it lives.
//
// It used to follow where it lived. CRUD gen asked "is this RPC declared
// outside handlers_crud.go?" and read yes as "implemented by hand — emit no
// op". A shim moved verbatim into a sibling file (roofers: CreatePayment
// into invoice_ops.go, still calling s.crudCreatePaymentOp()) therefore
// lost its op, the validate build failed with `s.crudCreatePaymentOp
// undefined`, and the generate reverted 70 files. Go does not care which
// file of a package declares a method, so forge must not either.
//
// The rule now, per CRUD RPC, over EVERY file of the package:
//
//   - no method declares it anywhere          → forge appends a delegating
//     shim to handlers_crud.go, so the op is demanded;
//   - a method declares it and something in
//     the package references s.crud<Rpc>Op    → demanded (a moved or
//     customized shim, wherever it sits);
//   - a method declares it and nothing
//     references the op                      → implemented by hand. No op,
//     and none of the op's validation applies: that is the documented
//     escape hatch ("implement the RPC by hand") for a list filter with no
//     column, an append-only Update, and the rest.
//
// The entity conversion pair follows the same reasoning one level down: it
// is emitted for every entity a demanded op uses AND for every entity whose
// <entity>ToProto / <entity>FromProto the package's own code calls — a
// custom RPC reusing invoiceToProto, or a custom-read-shape body, must not
// lose its helper because every CRUD RPC of that entity is now hand-written.

// handlerPackageScan is one parse of a handler package's own code: every
// file except the forge-owned handler outputs (handlersGeneratedFiles).
type handlerPackageScan struct {
	// methods are the *Service methods declared in non-test files — any
	// file, handlers_crud.go included.
	methods map[string]bool
	// funcs are the package-level (receiver-less) funcs declared in any
	// file. A user's own `invoiceToProto` must not be read as a call that
	// demands forge's — emitting it would redeclare theirs.
	funcs map[string]bool
	// uses are the identifiers the package's code mentions, tests
	// included (an in-package test calling s.crudGetInvoiceOp() needs it
	// to exist as much as a handler does).
	uses map[string]bool
}

// handlersGeneratedFiles are the forge-owned files in a handler package
// that declare *Service methods or ops. They are forge's output, not the
// package's demand, so the scan never reads them — otherwise the ops file
// would demand itself. handlers_gen.go and handlers_crud_gen.go are
// retired emitters still found on older projects' disks.
var handlersGeneratedFiles = map[string]bool{
	"handlers_gen.go":          true,
	"handlers_crud_gen.go":     true,
	"handlers_crud_ops_gen.go": true,
}

// scanHandlerPackage parses every .go file in dir except
// handlersGeneratedFiles. A file that fails to parse is skipped with a
// warning, for the reason ScanExistingMethods gives: a transient syntax
// error in one file must not unwind the whole package's answer.
func scanHandlerPackage(dir string) (handlerPackageScan, error) {
	scan := handlerPackageScan{methods: map[string]bool{}, funcs: map[string]bool{}, uses: map[string]bool{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return scan, err
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || handlersGeneratedFiles[name] {
			continue
		}
		path := filepath.Join(dir, name)
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "Warning: CRUD op scan skipping %s (parse error): %v\n", path, perr)
			continue
		}
		isTest := strings.HasSuffix(name, "_test.go")
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			switch {
			case fn.Recv == nil:
				scan.funcs[fn.Name.Name] = true
			case !isTest && isServiceReceiver(fn.Recv):
				// Only a non-test declaration implements the RPC in the
				// build the Connect handler interface is checked against.
				scan.methods[fn.Name.Name] = true
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				scan.uses[id.Name] = true
			}
			return true
		})
	}
	return scan, nil
}

// isServiceReceiver reports whether a method receiver is `*Service`.
func isServiceReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	star, ok := recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Service"
}

// CRUDProjectionReference is one place a handler package still uses
// something forge generates for an rpc only while that rpc is CRUD.
type CRUDProjectionReference struct {
	// At is `file:line` within the handler package.
	At string
	// Symbol is what it uses: the op (`crudCreateTaskOp`) or, for a Create
	// rpc, its create-request test factory (`NewCreateTaskRequest`).
	Symbol string
}

// CRUDProjectionReferences reports, per method (keyed by Go method name), the
// first place the handler package at dir uses a symbol forge generates for
// that rpc only while it is CRUD: its op (crud<Method>Op), and for a
// Create-named rpc its create-request factory (New<Request>), which the
// scaffolded lifecycle test calls. Tests are read too — a test calling either
// breaks `go test` as surely as a handler breaks the build. Forge's own
// generated files (handlersGeneratedFiles, *_gen.go, *_gen_test.go) are not
// read: they DECLARE these symbols, they are not demand for them. Methods
// nothing references are absent. A missing dir is no references.
//
// It answers the question a `// forge:custom` rpc raises when it was CRUD
// before: forge stops generating both, so code still using them fails after
// the whole generate ran.
func CRUDProjectionReferences(dir string, methods []Method) (map[string]CRUDProjectionReference, error) {
	want := map[string]string{} // symbol → Go method name
	for _, m := range methods {
		want[crudOpName(m.GoName())] = m.GoName()
		if op, _ := parseCRUDOperation(m.Name); op == "create" {
			want["New"+naming.GoCamelCase(m.InputType)] = m.GoName() // createRequestFactoryName
		}
	}
	out := map[string]CRUDProjectionReference{}
	if len(want) == 0 {
		return out, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || handlersGeneratedFiles[name] ||
			strings.HasSuffix(name, "_gen.go") || strings.HasSuffix(name, "_gen_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue // the validate build reports a broken file far better
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if m, hit := want[id.Name]; hit {
				if _, seen := out[m]; !seen {
					out[m] = CRUDProjectionReference{At: fmt.Sprintf("%s:%d", name, fset.Position(id.Pos()).Line), Symbol: id.Name}
				}
			}
			return true
		})
	}
	return out, nil
}

// crudOpName is the generated op constructor for a CRUD RPC, given the
// RPC's Go method name — the name handlers_crud_ops_gen.go.tmpl declares
// (`crud{{.MethodName}}Op`, MethodName being the Go name).
func crudOpName(goMethod string) string {
	return "crud" + goMethod + "Op"
}

// calls reports whether the package's code references name without
// declaring it — i.e. it relies on forge to emit it.
func (s handlerPackageScan) calls(name string) bool {
	return s.uses[name] && !s.funcs[name] && !s.methods[name]
}

// demandsOp reports whether the package needs crud<rpc>Op emitted: either
// nothing declares the RPC (forge is about to shim it) or code somewhere
// in the package calls the op. goMethod is the RPC's Go method name — what
// a declaration in the package is spelled as.
func (s handlerPackageScan) demandsOp(goMethod string) bool {
	return !s.methods[goMethod] || s.calls(crudOpName(goMethod))
}

// crudOpsDemand splits a service's CRUD RPCs by what the handler package
// needs: the RPCs whose op is demanded, and the entities NO demanded op
// covers whose conversion helpers the package's code still calls.
func crudOpsDemand(scan handlerPackageScan, crudMethods []CRUDMethod) (opMethods []CRUDMethod, helperEntities []EntityDef) {
	covered := map[string]bool{}
	for _, cm := range crudMethods {
		if scan.demandsOp(cm.Method.GoName()) {
			opMethods = append(opMethods, cm)
			covered[cm.Entity.Name] = true
		}
	}
	for _, cm := range crudMethods {
		e := cm.Entity
		if covered[e.Name] {
			continue
		}
		covered[e.Name] = true // one decision per entity
		lower := entityConvLower(e.Name)
		if scan.calls(lower+"ToProto") || scan.calls(lower+"FromProto") {
			helperEntities = append(helperEntities, e)
		}
	}
	return opMethods, helperEntities
}
