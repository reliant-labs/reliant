// File: internal/linter/forgeconv/connect_names.go
//
// Which identifiers in one file name connect-go's runtime, per major.
//
// The handler rules began as syntax matches on the identifier `connect`
// (`connect.NewError`, `connect.CodeNotFound`), which is how connect-go v1 is
// imported. connect-go v2 (connectrpc.com/connect/v2) is ALSO package
// `connect`, but its API differs where these rules look: NewError takes a
// message string, not an error, and Errorf exists. So a rule must know which
// major a `connect` in front of it is.
//
// v1 behaviour is kept exactly: a `connect` that is not bound to the v2
// import is read as v1, as before, whatever the file imports. Only names the
// file binds to connectrpc.com/connect/v2 — `connect` itself, or an alias
// such as `connectv2` — read as v2. A v1 project therefore sees the same
// findings it always did.

package forgeconv

import (
	"go/ast"
	"strconv"

	"github.com/reliant-labs/forge/internal/connectmajor"
)

// connectNames is what one file binds to the connect-go v2 runtime.
type connectNames struct {
	v2 map[string]bool
}

// fileConnectNames resolves the file's connect-go v2 import names.
func fileConnectNames(file *ast.File) connectNames {
	names := connectNames{v2: map[string]bool{}}
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != connectmajor.ModuleV2 {
			continue
		}
		local := "connect"
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == "_" || local == "." {
			continue
		}
		names.v2[local] = true
	}
	return names
}

// importsV2 reports whether the file imports connect-go v2 at all.
func (c connectNames) importsV2() bool { return len(c.v2) > 0 }

// v1 reports whether ident is read as connect-go v1: the historic
// `connect`, unless this file binds it to v2.
func (c connectNames) v1(ident string) bool { return ident == "connect" && !c.v2[ident] }

// isV2 reports whether ident names connect-go v2 in this file.
func (c connectNames) isV2(ident string) bool { return c.v2[ident] }

// qualifier returns the identifier qualifying a selector expression, "" when
// the selector is not `ident.Name`.
func qualifier(e ast.Expr) (ident, name string) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return "", ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	return id.Name, sel.Sel.Name
}

// isNewError matches a connect error constructor call: v1's (and any
// unbound `connect`'s) NewError, and v2's NewError and Errorf.
func (c connectNames) isNewError(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, name := qualifier(call.Fun)
	switch {
	case c.isV2(ident):
		return name == "NewError" || name == "Errorf"
	case ident == "connect":
		return name == "NewError"
	}
	return false
}

// isCode matches a `connect.Code<Name>` constant of either major.
func (c connectNames) isCode(e ast.Expr) bool {
	ident, name := qualifier(e)
	return (ident == "connect" || c.isV2(ident)) && isCodeConstName(name)
}

// isCodeConstName reports `Code<Name>` (not the type `Code` itself).
func isCodeConstName(name string) bool {
	return len(name) > len("Code") && name[:len("Code")] == "Code"
}
