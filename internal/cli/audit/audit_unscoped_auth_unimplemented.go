package audit

import (
	"fmt"
	"go/ast"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// An rpc whose handler only answers CodeUnimplemented reads no caller
// because it reads nothing at all. Reporting it as "every signed-in caller
// is treated identically" is true and useless: there is no data behind it
// to scope. Worse, the only way to silence it was `forge:auth-unscoped-ok`,
// and a marker put on a stub stays there when the rpc is implemented, so
// it then hides the real gap it was never meant to cover. So such a
// handler is listed apart (unimplemented_rpcs) and is not a finding. When
// its body changes it is judged like any other handler again.
//
// The recognized bodies are exactly one return statement of:
//
//   - the embedded Connect handler: `return s.Unimplemented<Svc>Handler.<Rpc>(…)`,
//     where the receiver directly embeds this service's generated Connect
//     type (resolved through its declaration's imports), and the method
//     called is the handler's own name;
//   - `return nil, svcerr.Unimplemented(…)` (or `return svcerr.Unimplemented(…)`
//     for a streaming rpc), svcerr being forge/pkg/svcerr in that file,
//     optionally wrapped in svcerr.ToConnect;
//   - `return nil, connect.NewError(connect.CodeUnimplemented, …)` (or the
//     streaming form), connect being connectrpc.com/connect in that file.
//
// Anything else, including a body that does work first and then returns
// Unimplemented on one branch, is judged as usual: it may touch data.

const (
	svcerrImportPath  = "github.com/reliant-labs/forge/pkg/svcerr"
	connectImportPath = "connectrpc.com/connect"
)

// importIdents maps each import path f imports to the identifier that
// binds it in f ("" for a blank or dot import, which a selector cannot
// name). The default identifier is the last path element, which is what
// both packages recognized here are named.
func importIdents(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		ident := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			ident = imp.Name.Name
			if ident == "_" || ident == "." {
				ident = ""
			}
		}
		out[p] = ident
	}
	return out
}

// bodyServesUnimplemented reports whether fn's body is exactly one return
// of one of the Unimplemented shapes above.
func bodyServesUnimplemented(fn *ast.FuncDecl, imports map[string]string, embeddedReceivers map[string]bool, service string) bool {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return false
	}
	switch len(ret.Results) {
	case 1:
		// A streaming rpc returns only an error; a unary one may return the
		// embedded handler's (response, error) pair as one call.
		return isUnimplementedError(ret.Results[0], imports) ||
			isEmbeddedUnimplementedCall(ret.Results[0], fn, embeddedReceivers, service)
	case 2:
		nilIdent, ok := ret.Results[0].(*ast.Ident)
		return ok && nilIdent.Name == "nil" && isUnimplementedError(ret.Results[1], imports)
	}
	return false
}

// embeddedUnimplementedReceivers proves the field's identity from the receiver
// declaration and that file's imports, not from the spelling at the call site.
// Only direct anonymous embeddings of this service's generated Connect type
// qualify; aliases and named fields are deliberately left for normal auditing.
func embeddedUnimplementedReceivers(files []*ast.File, svc codegen.ServiceDef) map[string]bool {
	out := map[string]bool{}
	if svc.GoPackage == "" || svc.PkgName == "" {
		return out
	}
	importPath := svc.GoPackage + "/" + svc.PkgName + "connect"
	for _, f := range files {
		pkg := importIdents(f)[importPath]
		if pkg == "" {
			continue
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				typ, ok := spec.(*ast.TypeSpec)
				if !ok || typ.Assign.IsValid() {
					continue
				}
				st, ok := typ.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					if len(field.Names) == 0 && isQualified(field.Type, pkg, "Unimplemented"+svc.Name+"Handler") {
						out[typ.Name.Name] = true
					}
				}
			}
		}
	}
	return out
}

// isEmbeddedUnimplementedCall requires the exact generated embedding on fn's
// receiver, and a call to this RPC's own method.
func isEmbeddedUnimplementedCall(e ast.Expr, fn *ast.FuncDecl, embeddedReceivers map[string]bool, service string) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || !embeddedReceivers[receiverTypeName(fn.Recv.List[0].Type)] {
		return false
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	method, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || method.Sel.Name != fn.Name.Name {
		return false
	}
	field, ok := method.X.(*ast.SelectorExpr)
	if !ok || field.Sel.Name != "Unimplemented"+service+"Handler" {
		return false
	}
	recv, ok := field.X.(*ast.Ident)
	return ok && recv.Name == receiverName(fn)
}

// receiverName is the name fn's receiver is bound to, or "" when it is
// unnamed (then no expression can refer to it).
func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

// isUnimplementedError matches svcerr.Unimplemented(…), the same wrapped in
// svcerr.ToConnect(…), and connect.NewError(connect.CodeUnimplemented, …).
func isUnimplementedError(e ast.Expr, imports map[string]string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	svcerr, connect := imports[svcerrImportPath], imports[connectImportPath]
	switch {
	case svcerr != "" && isQualified(call.Fun, svcerr, "Unimplemented"):
		return true
	case svcerr != "" && isQualified(call.Fun, svcerr, "ToConnect") && len(call.Args) == 1:
		return isUnimplementedError(call.Args[0], imports)
	case connect != "" && isQualified(call.Fun, connect, "NewError") && len(call.Args) >= 1:
		return isQualified(call.Args[0], connect, "CodeUnimplemented")
	}
	return false
}

// isQualified reports whether e is the selector pkg.name.
func isQualified(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// unimplementedNote is the summary's clause naming the handlers left out
// because they answer CodeUnimplemented, or "" when there are none.
func unimplementedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; %d more answer CodeUnimplemented and are not judged (unimplemented_rpcs)", n)
}
