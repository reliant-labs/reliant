// File: internal/linter/forgeconv/unwrapped_domain_error.go
//
// The forgeconv-unwrapped-domain-error analyzer catches the inverse of
// its sibling in no_handler_error_mapping.go. That rule punishes a
// handler package for re-rolling the service-error → connect.Error
// switch. This one punishes the case that actually ships: no mapping at
// all.
//
// # The defect
//
// A Connect handler that hands a collaborator's error straight back —
//
//	res, err := s.deps.Lifecycle.ShipOrder(ctx, in)
//	if err != nil {
//	    return nil, err
//	}
//
// — puts a DOMAIN error on the WIRE. connect-go has no idea what it is,
// so it becomes `500 {"code":"unknown"}`, the svcerr sentinel that said
// "failed precondition" is gone, and any reason code the service layer
// attached via svcerr.WithReason never reaches x-forge-error-reason.
// Every client of that RPC is left matching message text.
//
// This is not hypothetical. peptides-rw1 (real-workflow run 1) shipped
// exactly four of these in handlers_order_lifecycle.go — a file an agent
// hand-wrote over forge's born stub, which had it right. The prescription
// half of the SAME package, same phase, wrote `svcerr.Wrap(err)` and
// returned `400 failed_precondition` with the header. All four order
// lifecycle RPCs were unusable by any client; `forge lint` passed and the
// gate was green.
//
// # What is detected — the defect, not a spelling
//
// forge never forces helper names, so this rule looks for none. It
// encodes exactly one convention, the one the api-handlers skill states:
// a handler is a thin translation that CALLS ITS INJECTED COLLABORATOR
// and wraps what comes back. So the question is structural — when the
// handler reaches through its own receiver into a dependency and takes an
// error, did it apply anything to that error before returning it?
//
//	res, err := s.deps.Lifecycle.ShipOrder(ctx, in)   // collaborator call
//	return nil, err                                   // nothing applied → FLAG
//
//	return nil, anything(err)                         // a call was applied → silent
//
// Whether "anything" is svcerr.Wrap, connect.NewError or the project's
// own wrapper is none of the rule's business.
//
// # Where the boundary was drawn, and why — this was measured, not guessed
//
// Two wider cuts were tried against four real trees (control-plane,
// cp-forge, peptides-dogfood-r2, peptides-rw1) and both were withdrawn on
// the evidence:
//
//  1. "a single-result call through a package qualifier" — 7 hits, all 7
//     `middleware.VerifyAuth(ctx, "admin")`, which returns
//     `connect.NewError(...)`. 7 false positives, 0 true.
//  2. "any (T, error) call" — 5 further hits, all
//     `middleware.RequireAdminOrOperator(ctx) (*Claims, error)`, likewise
//     returning `connect.NewError(...)`. A function returning a value AND
//     an error is NOT reliably a domain-error source; an auth guard that
//     hands back the claims is the counterexample, and agents write them.
//
// What survived both is the receiver-rooted chain of depth ≥ 2 —
// `s.deps.Lifecycle.ShipOrder`, `s.orders.Ship` — i.e. the handler
// reaching into something injected. That is where a domain error is
// produced, and it is the only place forge's own convention says to wrap.
// Zero false positives across the four trees; the four true positives in
// peptides-rw1 all hit.
//
// Deliberately NOT flagged, because the value may already be a wire error
// and the rule refuses to guess: a package-level call
// (`middleware.X(...)`, `db.List(...)`), a same-package function
// (`validateFoo(req)`), a method on the handler's own receiver
// (`s.check(req)`), `err = someWrapper(err)` (the identifier was fed back
// through a call), and anything bound outside the function or by
// something other than a call.
//
// The classification is flow-INSENSITIVE and errs toward silence: a
// finding requires that EVERY binding of the returned identifier inside
// the function is raw. One wrapped or one unclassifiable binding
// suppresses the whole return.
//
// # Why error severity is safe here
//
// Because the remedy is never wrong. svcerr.Wrap passes an
// already-Connect error through untouched (ToConnect's errors.As
// fast-path), so wrapping at the collaborator boundary is idempotent even
// for a service layer that returns connect errors itself. There is no
// project for which the fix is a regression — and the defect's entire
// signature is that it passes every check and ships. A warning in a wall
// of lint output is what it already survived.

package forgeconv

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
)

// unwrappedDomainErrorRule is the rule id reported on every finding.
const unwrappedDomainErrorRule = "forgeconv-unwrapped-domain-error"

// connectStreamTypes are the connect-go parameter types that mark a
// function as sitting on the RPC boundary. Any of them in the parameter
// list, plus a trailing `error` result, is a handler — no receiver name,
// method name or file name is consulted.
var connectBoundaryParamTypes = map[string]bool{
	"Request":      true,
	"ClientStream": true,
	"ServerStream": true,
	"BidiStream":   true,
}

// bindingKind classifies where a returned error identifier came from.
type bindingKind int

const (
	// bindingUnknown — bound by something this rule will not reason
	// about (a same-package call, a non-call expression, a parameter).
	bindingUnknown bindingKind = iota
	// bindingWrapped — a call was applied that produced or transformed
	// this error, so the handler did SOMETHING.
	bindingWrapped
	// bindingRaw — the value is a collaborator's error, verbatim.
	bindingRaw
)

// lintUnwrappedDomainErrorFile reports every Connect handler in the file
// that returns a domain error without applying anything to it.
func lintUnwrappedDomainErrorFile(relPath string, fset *token.FileSet, file *ast.File) []Finding {
	svcerrAlias := svcerrImportAlias(file)

	var findings []Finding
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if !isConnectBoundaryFunc(fn) {
			continue
		}
		for _, ret := range rawErrorReturns(fn, receiverIdent(fn), svcerrAlias) {
			pos := fset.Position(ret.pos)
			findings = append(findings, Finding{
				Rule:     unwrappedDomainErrorRule,
				Severity: SeverityError,
				File:     relPath,
				Line:     pos.Line,
				Message: fmt.Sprintf(
					"%s returns %s straight from %s — a domain error crossing the RPC boundary unwrapped. "+
						"connect-go cannot classify it, so the client gets code=unknown/500 with no "+
						"x-forge-error-reason, whatever sentinel or reason the service layer set.",
					fn.Name.Name, ret.name, ret.origin),
				Remediation: "apply the service-error → wire mapping before returning: " +
					"`return nil, svcerr.Wrap(" + ret.name + ")` from `github.com/reliant-labs/forge/pkg/svcerr` " +
					"maps svcerr sentinels to their Connect code and carries svcerr.WithReason through to the " +
					"x-forge-error-reason header. Any call that produces a wire error satisfies this rule — " +
					"forge does not require a particular helper. See skill: api/handlers.",
			})
		}
	}
	return findings
}

// rawReturn is one flagged return site.
type rawReturn struct {
	pos    token.Pos
	name   string // the identifier returned
	origin string // human-readable description of where it was bound
}

// rawErrorReturns walks fn's own statements — never descending into a
// nested func literal, whose returns do not cross the RPC boundary — and
// collects the returns whose error operand is an unwrapped domain error.
func rawErrorReturns(fn *ast.FuncDecl, recv, svcerrAlias string) []rawReturn {
	bindings := collectBindings(fn.Body)
	shadowed := signatureIdents(fn)

	var out []rawReturn
	walkFuncStmts(fn.Body, func(n ast.Node) {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return
		}
		// The error is always the trailing result: `(*connect.Response[T], error)`
		// for unary/client-streaming, bare `error` for server- and bidi-streaming.
		last := ret.Results[len(ret.Results)-1]
		id, ok := last.(*ast.Ident)
		if !ok || id.Name == "nil" {
			// A call expression, a composite literal, a selector — the
			// handler built or transformed this value. Not our business.
			return
		}
		if shadowed[id.Name] {
			// A parameter or named result: bound outside the body.
			return
		}
		sites := bindings[id.Name]
		if len(sites) == 0 {
			return
		}
		origin := ""
		for _, site := range sites {
			switch classifyBinding(site, id.Name, recv, svcerrAlias) {
			case bindingWrapped, bindingUnknown:
				return
			case bindingRaw:
				if origin == "" {
					origin = site.origin
				}
			}
		}
		out = append(out, rawReturn{pos: ret.Pos(), name: id.Name, origin: origin})
	})
	return out
}

// bindingSite is one assignment of an identifier inside a function body.
type bindingSite struct {
	rhs        ast.Expr // the expression this identifier takes its value from
	multiValue bool     // rhs is a single call yielding several results
	origin     string   // rendered call expression, for the diagnostic
}

// collectBindings maps identifier name → every assignment of it in body,
// including assignments in `if`/`switch` init statements. Nested func
// literals are skipped: an `err` bound inside a closure is a different
// variable as far as the boundary is concerned.
func collectBindings(body *ast.BlockStmt) map[string][]bindingSite {
	out := map[string][]bindingSite{}
	walkFuncStmts(body, func(n ast.Node) {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || (assign.Tok != token.DEFINE && assign.Tok != token.ASSIGN) {
			return
		}
		for i, lhs := range assign.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}
			site := bindingSite{}
			switch {
			case len(assign.Rhs) == len(assign.Lhs):
				site.rhs = assign.Rhs[i]
			case len(assign.Rhs) == 1:
				site.rhs = assign.Rhs[0]
				site.multiValue = len(assign.Lhs) > 1
			default:
				continue
			}
			site.origin = renderExpr(site.rhs)
			out[id.Name] = append(out[id.Name], site)
		}
	})
	return out
}

// classifyBinding decides whether a binding leaves the error raw.
func classifyBinding(site bindingSite, name, recv, svcerrAlias string) bindingKind {
	call, ok := site.rhs.(*ast.CallExpr)
	if !ok {
		// `err := someVar`, `err := <-ch`, a type assertion: not a call,
		// so the rule cannot tell what this value is.
		return bindingUnknown
	}
	if isWireErrorConstructor(call, svcerrAlias) {
		return bindingWrapped
	}
	if callMentionsIdent(call, name) {
		// `err = wrap(err)` — the identifier was fed back through a call.
		return bindingWrapped
	}
	if !site.multiValue {
		// A call whose ONLY result is an error is as likely to be a guard
		// that already built the wire error (middleware.VerifyAuth) as a
		// collaborator returning a domain error. See the file header for
		// the measurement that settled this. Stay silent.
		return bindingUnknown
	}
	// `v, err := <chain>(...)`: raw only when the chain reaches through
	// the handler's own receiver into something injected —
	// `s.deps.Lifecycle.ShipOrder`, `s.orders.Ship`. A package-level call
	// (`middleware.RequireAdminOrOperator(ctx)`) or a method on the
	// receiver itself (`s.load(ctx)`) may already have built a wire error.
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return bindingUnknown // `v, err := productToProto(entity)`
	}
	root, depth := selectorRoot(sel)
	if root != recv || depth < 2 {
		return bindingUnknown
	}
	return bindingRaw
}

// isConnectBoundaryFunc reports whether fn sits on the RPC boundary: a
// connect-go stream/request type among its parameters and `error` as its
// final result. This is a property of the SIGNATURE, so it holds whatever
// the receiver, method or file is called.
func isConnectBoundaryFunc(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || fn.Type == nil {
		return false
	}
	res := fn.Type.Results
	if res == nil || len(res.List) == 0 {
		return false
	}
	lastType := res.List[len(res.List)-1].Type
	if id, ok := lastType.(*ast.Ident); !ok || id.Name != "error" {
		return false
	}
	if fn.Type.Params == nil {
		return false
	}
	for _, p := range fn.Type.Params.List {
		found := false
		ast.Inspect(p.Type, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "connect" {
				return true
			}
			if connectBoundaryParamTypes[sel.Sel.Name] {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// receiverIdent returns the handler receiver's variable name ("s" in
// `func (s *Service) ...`), or "" for an unnamed receiver — in which case
// no collaborator chain can be rooted at it and the rule stays silent.
func receiverIdent(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	names := fn.Recv.List[0].Names
	if len(names) == 0 {
		return ""
	}
	return names[0].Name
}

// signatureIdents is the set of names bound by fn's signature —
// parameters, named results and the receiver. A return of one of these is
// out of scope: the value came from the caller.
func signatureIdents(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	add := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				out[n.Name] = true
			}
		}
	}
	add(fn.Recv)
	if fn.Type != nil {
		add(fn.Type.Params)
		add(fn.Type.Results)
	}
	return out
}

// svcerrImportAlias resolves the local name the file uses for
// forge/pkg/svcerr, honouring an explicit alias. Returns "" when the file
// does not import it.
func svcerrImportAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasSuffix(path, "/forge/pkg/svcerr") && !strings.HasSuffix(path, "/pkg/svcerr") {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "svcerr"
	}
	return ""
}

// isWireErrorConstructor reports whether call produces a wire-ready
// error: anything from connect-go itself, or anything from forge's
// svcerr. Both are libraries whose whole job is to carry a status code,
// so a value they produced is by construction not a raw domain error.
func isWireErrorConstructor(call *ast.CallExpr, svcerrAlias string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	root, depth := selectorRoot(sel)
	if depth != 1 {
		return false
	}
	return root == "connect" || (svcerrAlias != "" && root == svcerrAlias)
}

// callMentionsIdent reports whether name appears anywhere in call's
// arguments — the tell that the value was fed back through a call rather
// than produced fresh.
func callMentionsIdent(call *ast.CallExpr, name string) bool {
	found := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// selectorRoot unwinds a selector chain to its leftmost identifier and
// reports how many selections separate it from the call. `connect.NewError`
// is ("connect", 1); `s.deps.Lifecycle.ShipOrder` is ("s", 3). Depth is
// what distinguishes a package-or-own-method call from a call that
// reaches through a collaborator. A chain not rooted at an identifier
// (an index expression, a call) returns ("", 0).
func selectorRoot(sel *ast.SelectorExpr) (string, int) {
	depth := 1
	cur := sel.X
	for {
		switch x := cur.(type) {
		case *ast.Ident:
			return x.Name, depth
		case *ast.SelectorExpr:
			depth++
			cur = x.X
		default:
			return "", 0
		}
	}
}

// renderExpr produces a compact source rendering of a call for the
// diagnostic, so the message names the collaborator the error came from
// rather than making the reader go look.
func renderExpr(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.CallExpr:
		return renderExpr(x.Fun) + "(...)"
	case *ast.SelectorExpr:
		return renderExpr(x.X) + "." + x.Sel.Name
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return renderExpr(x.X)
	case *ast.StarExpr:
		return renderExpr(x.X)
	default:
		return "a call"
	}
}

// walkFuncStmts visits every node under body EXCEPT the bodies of nested
// function literals. A closure's `return` does not cross the RPC
// boundary, and its `err` is a different variable; folding them in would
// produce findings on errgroup callbacks and crud op wiring.
func walkFuncStmts(body *ast.BlockStmt, visit func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		visit(n)
		return true
	})
}
