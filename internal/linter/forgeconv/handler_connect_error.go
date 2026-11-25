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
// # connect-go v2
//
// v2's NewError takes a message string — `connect.NewError(code, msg)` —
// and serializes it verbatim; `.WithCause(err)` attaches a local cause that
// never reaches the wire, and `connect.Errorf(code, format, args...)` formats
// the message. So under v2 the leak is a message built from an error's text
// (`err.Error()`, `connect.Errorf(code, "x: %v", err)`); a fixed message is
// the same harmless hand mapping as in v1. A `connect` the file binds to
// connectrpc.com/connect/v2 (or an alias of it) is read as v2; every other
// `connect` is read as v1, exactly as before.
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
// handler-tree file — and, where the file imports connect-go v2, each v2
// NewError and Errorf (see lintConnectV2ErrorCall).
func lintHandlerConnectErrorFile(relPath string, fset *token.FileSet, file *ast.File) []Finding {
	if ast.IsGenerated(file) {
		return nil
	}
	names := fileConnectNames(file)
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, fn := qualifier(call.Fun)
		if names.isV2(ident) {
			if f, ok := lintConnectV2ErrorCall(relPath, fset, call, ident, fn, names); ok {
				findings = append(findings, f)
			}
			return true
		}
		if !names.v1(ident) || fn != "NewError" || len(call.Args) != 2 {
			return true
		}
		kind := connectErrorKind(call.Args[0], names)
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

// connectErrorKind names the svcerr kind matching a connect code argument
// (`connect.CodeNotFound` → "NotFound"), or "<Kind>" for a computed code.
func connectErrorKind(code ast.Expr, names connectNames) string {
	if names.isCode(code) {
		_, name := qualifier(code)
		return strings.TrimPrefix(name, "Code")
	}
	return "<Kind>"
}

// lintConnectV2ErrorCall reports one connect-go v2 error constructor:
// `connect.NewError(code, message string)` or `connect.Errorf(code, format,
// args...)`. v2 serializes the message verbatim, and `.WithCause(err)`
// attaches a cause that never reaches the wire — so the leak is the
// MESSAGE carrying an error's text (`err.Error()`, a %v of err), not an
// error argument, which v2 does not take. `forge fix connect-errors`
// rewrites connect-go v1 only, so no v2 remediation points at it.
func lintConnectV2ErrorCall(relPath string, fset *token.FileSet, call *ast.CallExpr, ident, fn string, names connectNames) (Finding, bool) {
	var shape connectErrorArg
	switch {
	case fn == "NewError" && len(call.Args) == 2:
		shape = connectV2MessageShape(call.Args[1])
	case fn == "Errorf" && len(call.Args) >= 2:
		shape = connectV2FormatShape(call.Args[1], call.Args[2:])
	default:
		return Finding{}, false
	}
	callee := ident + "." + fn
	kind := connectErrorKind(call.Args[0], names)
	f := Finding{
		Rule:     handlerConnectErrorRule,
		Severity: SeverityWarning,
		File:     relPath,
		Line:     fset.Position(call.Pos()).Line,
	}
	switch shape {
	case argFixedMessage:
		f.Message = callee + " with a fixed message: the hand-built connect error svcerr replaces"
		f.Remediation = fmt.Sprintf("`return nil, svcerr.%s(\"<message>\")` (NotFound takes the entity: svcerr.NotFound(\"chat\") reads \"chat not found\"; other NotFound prose is svcerr.WithMessage(svcerr.ErrNotFound, \"…\")). "+
			"A `.WithCause(err)` on it is a local cause only — keep it with svcerr.WithCause. `forge fix connect-errors` rewrites connect-go v1 code only: edit this v2 site by hand. See skill: api.", kind)
	case argNil:
		f.Message = callee + " with an empty message: the client reads only the code"
		f.Remediation = fmt.Sprintf("return the sentinel `svcerr.Err%s`, or a constructor with the message the caller should read. See skill: api.", kind)
	case argComputedMessage:
		f.Message = callee + " with a computed message: whatever the expression holds reaches the client verbatim, and a *connect.Error is never redacted"
		f.Remediation = fmt.Sprintf("text the USER should read: `return nil, svcerr.%s(msg)`; anything that can carry an error's text: `return nil, svcerr.WrapOr(err, svcerr.%s(\"<what failed>\"))`. See skill: api.", kind, kind)
	default:
		f.Message = callee + " with an error's text as its message publishes it to the client — connect-go v2 serializes the message verbatim and a *connect.Error is never redacted — and overrides any code the error already carries"
		f.Remediation = fmt.Sprintf("`return nil, svcerr.WrapOr(err, svcerr.%s(\"<what failed>\"))`: a classified err (a store's NotFound, a deadline) keeps its own code, anything else reads your summary and is logged as the cause. "+
			"To keep a cause without publishing it, `.WithCause(err)` on a fixed message, never err.Error() in the message; a remote Connect client's error is wrapped in your own verdict with svcerr.WithCause, never WrapOr. See skill: api.", kind)
	}
	return f, true
}

// connectV2MessageShape classifies v2 NewError's message string: an empty
// literal; a literal or an fmt.Sprintf of a literal format whose arguments
// read no error (fixed); anything that reads an error's text (error
// value); any other expression (computed — a variable, a helper's result).
func connectV2MessageShape(e ast.Expr) connectErrorArg {
	e = ast.Unparen(e)
	if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		if s, err := strconv.Unquote(lit.Value); err == nil && s == "" {
			return argNil
		}
		return argFixedMessage
	}
	if call, ok := e.(*ast.CallExpr); ok && isQualifiedCall(call.Fun, "fmt", "Sprintf") && len(call.Args) > 0 {
		return connectV2FormatShape(call.Args[0], call.Args[1:])
	}
	if looksLikeError(e) {
		return argErrorValue
	}
	return argComputedMessage
}

// connectV2FormatShape classifies a format and its arguments, as v2's
// Errorf and fmt.Sprintf take them.
func connectV2FormatShape(format ast.Expr, args []ast.Expr) connectErrorArg {
	for _, a := range args {
		if looksLikeError(a) {
			return argErrorValue
		}
	}
	lit, ok := ast.Unparen(format).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		if looksLikeError(format) {
			return argErrorValue
		}
		return argComputedMessage
	}
	if s, err := strconv.Unquote(lit.Value); err == nil && s == "" && len(args) == 0 {
		return argNil
	}
	return argFixedMessage
}

type connectErrorArg int

const (
	argErrorValue connectErrorArg = iota
	argFixedMessage
	argNil
	// argComputedMessage is a connect-go v2 message the rule cannot read:
	// not a literal, and not visibly an error's text.
	argComputedMessage
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
