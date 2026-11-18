// File: internal/linter/forgeconv/handler_connect_error.go
//
// The forgeconv-handler-connect-error analyzer flags every
// connect.NewError in a handler package.
//
// # The defect
//
// connect.Error.Message() is literally the wrapped error's Error(), and
// svcerr.Interceptor passes a *connect.Error through exactly as built — it
// is the handler's own verdict, so nothing redacts it. That makes
//
//	return nil, connect.NewError(connect.CodeInternal, err)
//	return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("push failed: %w", err))
//
// publish err.Error() — a driver's SQL, a DSN, an internal host — to the
// caller, and pin a guessed code over a NotFound or a deadline the
// collaborator already classified. Measured in reliant: 246 handler sites
// handed an error value to connect.NewError, 163 of them under
// CodeInternal. svcerr's redaction exists for exactly this and never ran.
//
// A fixed message (`connect.NewError(connect.CodeInvalidArgument,
// errors.New("id is required"))`) is not a leak, but it is the same hand
// mapping svcerr replaces, and it hides the sites that are: a reviewer cannot
// tell 900 harmless calls from 300 leaking ones by reading them. So the rule
// flags every call, says which kind each one is, and `forge fix
// connect-errors` rewrites the harmless kind wire-identically, leaving the
// worklist this rule exists to surface.
//
// # Severity
//
// Warning by default, so a project that has not migrated does not go red on
// a forge bump. A project that has flips it to error in forge.yaml:
//
//	lint:
//	  rules:
//	    forgeconv-handler-connect-error: error
//
// Test files are not linted (a fixture may build a connect error on
// purpose), nor are generated files (fix the generator). The classification
// is syntactic: `forge fix connect-errors` type-checks and is authoritative.

package forgeconv

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

const handlerConnectErrorRule = "forgeconv-handler-connect-error"

// lintHandlerConnectErrorFile reports each connect.NewError in one parsed
// handler-tree file.
func lintHandlerConnectErrorFile(relPath string, fset *token.FileSet, file *ast.File) []Finding {
	if ast.IsGenerated(file) {
		return nil
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isQualifiedCall(call.Fun, "connect", "NewError") || len(call.Args) != 2 {
			return true
		}
		kind := "<Kind>"
		if sel, ok := call.Args[0].(*ast.SelectorExpr); ok && isConnectCode(sel) {
			kind = strings.TrimPrefix(sel.Sel.Name, "Code")
		}
		f := Finding{
			Rule:     handlerConnectErrorRule,
			Severity: SeverityWarning,
			File:     relPath,
			Line:     fset.Position(call.Pos()).Line,
		}
		switch connectErrorArgShape(call.Args[1]) {
		case argFixedMessage:
			f.Message = "connect.NewError with a fixed message: the hand-built connect error svcerr replaces"
			f.Remediation = fmt.Sprintf("`return nil, svcerr.%s(\"<message>\")` (NotFound takes the entity: svcerr.NotFound(\"chat\") reads \"chat not found\"; other NotFound prose is svcerr.WithMessage(svcerr.ErrNotFound, \"…\")). "+
				"`forge fix connect-errors` rewrites this site with the same code and message. See skill: api.", kind)
		case argNil:
			f.Message = "connect.NewError with a nil error: the client reads an empty message"
			f.Remediation = fmt.Sprintf("return the sentinel `svcerr.Err%s`, or a constructor with the message the caller should read. See skill: api.", kind)
		default:
			f.Message = "connect.NewError with an error value publishes its Error() text to the client — a *connect.Error is never redacted — and overrides any code the error already carries"
			f.Remediation = fmt.Sprintf("`return nil, svcerr.WrapOr(err, svcerr.%s(\"<what failed>\"))`: a classified err (a store's NotFound, a deadline) keeps its own code, anything else reads your summary and is logged as the cause. "+
				"Text the USER should read goes in the constructor's argument (svcerr.FailedPrecondition(daemonMsg)); a remote Connect client's error is wrapped in your own verdict with svcerr.WithCause, never WrapOr. See skill: api.", kind)
		}
		findings = append(findings, f)
		return true
	})
	return findings
}

type connectErrorArg int

const (
	argErrorValue connectErrorArg = iota
	argFixedMessage
	argNil
)

// connectErrorArgShape classifies connect.NewError's error argument without
// types: nil; a message the handler wrote (errors.New of a string literal,
// or fmt.Errorf of a literal format with no %w whose arguments call no
// Error() and are not named like an error); or anything else, which is
// treated as an error value.
func connectErrorArgShape(e ast.Expr) connectErrorArg {
	e = ast.Unparen(e)
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return argNil
	}
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return argErrorValue
	}
	lit, ok := ast.Unparen(call.Args[0]).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return argErrorValue
	}
	switch {
	case isQualifiedCall(call.Fun, "errors", "New") && len(call.Args) == 1:
		return argFixedMessage
	case isQualifiedCall(call.Fun, "fmt", "Errorf"):
		format, err := strconv.Unquote(lit.Value)
		if err != nil || strings.Contains(format, "%w") {
			return argErrorValue
		}
		for _, a := range call.Args[1:] {
			if looksLikeError(a) {
				return argErrorValue
			}
		}
		return argFixedMessage
	}
	return argErrorValue
}

// looksLikeError reports an argument that is, or reads the text of, an
// error: an identifier named err/…Err/…Error, or any .Error() call in it.
func looksLikeError(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			name := strings.ToLower(x.Name)
			if name == "err" || strings.HasSuffix(name, "err") || strings.HasSuffix(name, "error") {
				found = true
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" && len(x.Args) == 0 {
				found = true
			}
		}
		return !found
	})
	return found
}
