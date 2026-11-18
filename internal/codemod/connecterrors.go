package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// connecterrors.go — `forge fix connect-errors`: the wire-identical half of
// moving a project's hand-built Connect errors onto pkg/svcerr.
//
// `connect.NewError(code, err)` publishes err.Error() to the caller, and
// svcerr's redaction cannot reach it: a *connect.Error is the handler's own
// verdict and svcerr.Interceptor passes it through as built. Most of a
// project's sites are harmless — a fixed message the handler wrote under an
// explicit code — and those convert mechanically. The rest wrap an error
// value under a guessed code, which is where the leaks and the wrong codes
// are, and converting them CHANGES what a client reads. So this codemod
// rewrites only the first kind, and lists the second.
//
// The contract is the one every codemod here keeps, at its strictest: for
// every site it rewrites, the client reads the same code and the same
// message as before. A site where no svcerr spelling says exactly the old
// thing is left alone and listed:
//
//   - the error is an error VALUE (`err`, `fmt.Errorf("…: %w", err)`, an
//     error-typed or .Error() argument) — svcerr.WrapOr is the fix, and it
//     changes the text, so it belongs in a reviewed change;
//   - the error is nil — the message was "", and every svcerr kind has text;
//   - the message is computed and could be empty (errors.New(msg), "%s") —
//     an empty detail makes a constructor return its bare sentinel, whose
//     text is not "";
//   - the code is not a constant (connect.NewError(code, …)).
//
// Where a rewritten value is read matters as much as what it says. A bare
// svcerr kind reaches the client with its code only through
// svcerr.Interceptor, which forge's chain builders install innermost on the
// HANDLER chain — so a bare kind is emitted only in a handler package
// (a path under handlers/), only where the expression's context type is
// exactly `error`, and only when no in-process reader of a *connect.Error
// (connect.CodeOf, errors.As or any other call given a **connect.Error As
// target — testify's require.ErrorAs, a test helper — a type assertion), which
// would see a bare kind as something else, can see the package's errors:
//
//   - in the package — its code or its tests (a test's connect.CodeOf aside,
//     which is rewritten);
//   - in the production code of a package importing it (an import by tests
//     alone is not an edge: those tests are the importer's, and what they
//     read is theirs);
//   - in the production code of a package that calls, through an interface
//     declared in this module, a method a type of the package implements,
//     where production code hands a value of that type to the interface —
//     the consumer-declared interface composition wires the handler into,
//     which imports nothing. (An interface only ever handed a generated
//     client — a CLI's — has the handler's method set too, but its calls go
//     over the wire.)
//
// A read of an error a Connect client returned — a *connect.Client or
// generated <svc>connect client method, directly or through a function of
// this module whose every error is one — is not an in-process reader: that
// error came off the wire and stays a *connect.Error whatever the handler
// built. A reader further away is not traced, nor is an importer's test: one
// that does not read a client's error fails loudly. The report names EVERY
// reader keeping a package wrapped, and how it reaches the package — on every
// run, so a re-run after fixing one names the next. Everywhere else —
// interceptors, clients, workers, a *connect.Error-typed return, a package
// such a reader can see — the result is svcerr.ToConnect(svcerr.<Kind>(…)):
// the same *connect.Error, built by svcerr, wherever it goes.
//
// --scope bounds the files rewritten, never the analysis: a lane rewrites its
// own slice of a shared checkout while every reader in the module still
// counts. A test outside the scope that the rewrite would have switched to
// svcerr.Code is listed instead; one in the package's own tests keeps the
// package wrapped (it calls the package directly and would read unknown).
//
// Every file:line the report prints is the file as it is on disk when the run
// ends: after a real run, the rewritten file's lines.
//
// Tests are rewritten in one way only: connect.CodeOf(x) → svcerr.Code(x),
// in the test files of every package that got a bare kind and of every
// package importing one. The two agree for a *connect.Error and for nil, and for a
// rewritten site svcerr.Code reads exactly the code connect.CodeOf read
// before; they differ only for an unclassified error (Unknown vs Internal),
// which a test then reports loudly. A connect.NewError inside a test is a
// fixture and stays.

const (
	connectPath = "connectrpc.com/connect"
	svcerrPath  = "github.com/reliant-labs/forge/pkg/svcerr"
)

func init() {
	register(Codemod{
		Name:    "connect-errors",
		Version: "v1.0.0",
		Short:   "Rewrite fixed-message connect.NewError calls to pkg/svcerr, wire-identically, and list the rest",
		Long: `Move hand-built Connect errors onto pkg/svcerr where that changes nothing a client reads.

connect.NewError(code, err) publishes err.Error() to the caller, and svcerr's
redaction cannot help: a *connect.Error passes svcerr.Interceptor as built. This
rewrites every connect.NewError whose client-visible code AND message an svcerr
spelling reproduces exactly:

  connect.NewError(connect.CodeNotFound, errors.New("chat not found"))  → svcerr.NotFound("chat")
  connect.NewError(connect.CodeNotFound, errors.New("no such chat"))    → svcerr.WithMessage(svcerr.ErrNotFound, "no such chat")
  connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
                                                                        → svcerr.InvalidArgument("id is required")
  connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("name %q is taken", n))
                                                                        → svcerr.AlreadyExists(fmt.Sprintf("name %q is taken", n))

A bare svcerr kind is written only in a handler package, where the expression is
an error and nothing reads a *connect.Error in process (connect.CodeOf, errors.As
into one, an As-style helper such as testify's require.ErrorAs handed a
**connect.Error) in the package, its tests, the code of a package importing it,
or the code of a package calling it through an interface one of its types
implements. A read of a Connect client's error (off the wire) is not one.
Elsewhere (interceptors, clients, a *connect.Error-typed result, a package such a
reader can see) the same value is kept a *connect.Error with svcerr.ToConnect(…),
and the report names every reader and how it reaches the package, on every run;
once they read svcerr.Code, a re-run drops the wrappers.
In the test files of every package that got a bare kind, and of every package
importing one, connect.CodeOf(x) becomes svcerr.Code(x) — the code the
interceptor sends, which a handler test calling a *Service method directly needs
once the handler returns a bare kind.

It never changes a code or a message. A site it cannot rewrite exactly is listed
with the reason, file and line, and is the worklist for the change that does
change what clients read:

  wraps an error value     connect.NewError(code, err), fmt.Errorf("…: %w", err), an error-typed
                           or .Error() argument: publishes err.Error(). svcerr.WrapOr(err,
                           svcerr.<Kind>("<summary>")) redacts it and passes a classified err
                           through — which changes the code or text, so review it.
  nil error                the message is ""; every svcerr kind has text
  computed message         errors.New(msg) / a format with no literal text could be "", which no
                           svcerr constructor reproduces
  computed code            the code is not a connect.Code constant

Generated files, vendor/ and testdata/ are left alone, as is every
connect.NewError in a test (a fixture). Re-running it changes nothing.
--dry-run prints each rewrite as file:line: old → new, and the worklist; after a
real run every file:line is the rewritten file's.

--scope <path> (repeatable) rewrites, and reports on, only files under the paths
named, for one lane of a shared checkout; readers anywhere in the module still
keep a package wrapped. A test outside the scope that would have switched to
svcerr.Code is listed, not rewritten.`,
		CleanTree: true,
		Scoped:    true,
		Run:       runConnectErrors,
	})
}

// isHandlerFile reports whether rel is under a handlers/ directory — the
// tree forge's handler chain (and so svcerr.Interceptor) serves, and the one
// forgeconv-handler-connect-error lints.
func isHandlerFile(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/") {
		if part == "handlers" {
			return true
		}
	}
	return false
}

// ceRewriter rewrites one file.
type ceRewriter struct {
	f       ceFile
	info    *types.Info
	fset    *token.FileSet
	src     []byte
	parents map[ast.Node]ast.Node

	allowBare bool
	svcerrPfx string // "svcerr." or the file's alias
	addImport bool

	edits    []txEdit
	done     []ceDone
	left     []leftSite
	usedBare bool
	// keptWrapper: an svcerr.ToConnect an earlier run left, which this run
	// would drop if a reader did not keep the package on *connect.Error.
	keptWrapper bool
}

func newCERewriter(f ceFile) (*ceRewriter, error) {
	src, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	if tf := f.pkg.Fset.File(f.file.Pos()); tf == nil || tf.Size() != len(src) {
		return nil, fmt.Errorf("%s changed while it was being read; re-run", f.rel)
	}
	r := &ceRewriter{f: f, info: f.pkg.TypesInfo, fset: f.pkg.Fset, src: src, parents: map[ast.Node]ast.Node{}}
	var stack []ast.Node
	ast.Inspect(f.file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			r.parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	name := importName(f.file, svcerrPath, "svcerr")
	if name == "" {
		r.addImport = true
		name = "svcerr"
		if nameTaken(f.file, name) {
			name = "forgesvcerr"
		}
	}
	r.svcerrPfx = name + "."
	return r, nil
}

func (r *ceRewriter) off(pos token.Pos) int { return r.fset.Position(pos).Offset }

func (r *ceRewriter) text(n ast.Node) string { return string(r.src[r.off(n.Pos()):r.off(n.End())]) }

func (r *ceRewriter) site(n ast.Node) ceSite {
	return ceSite{r.f.rel, r.fset.Position(n.Pos()).Line}
}

// funcOf returns the package-level function a call's callee resolves to, as
// (package path, name), or ("", "") for anything else.
func funcOf(info *types.Info, call *ast.CallExpr) (string, string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return "", ""
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return "", ""
	}
	return fn.Pkg().Path(), fn.Name()
}

func (r *ceRewriter) rewriteNewErrors() {
	ast.Inspect(r.f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkg, name := funcOf(r.info, call)
		if pkg == svcerrPath && name == "ToConnect" && len(call.Args) == 1 {
			r.unwrapToConnect(call)
			return true
		}
		if pkg != connectPath || name != "NewError" || len(call.Args) != 2 {
			return true
		}
		r.newError(call)
		// A NewError nested in another's argument is a value of the outer
		// one; rewriting both would overlap, and the outer is left (its
		// argument is an error value), so the inner one is visited alone.
		return true
	})
}

// newError rewrites one connect.NewError call, or records why not.
func (r *ceRewriter) newError(call *ast.CallExpr) {
	// Whitespace collapsed but never truncated: the line is the diff a
	// reviewer reads in --dry-run, and the worklist a lane starts from.
	old := strings.Join(strings.Fields(r.text(call)), " ")
	kind, ok := r.codeKind(call.Args[0])
	if !ok {
		r.left = append(r.left, leftSite{r.site(call), old, leftComputedCode})
		return
	}
	msg, why := r.message(call.Args[1])
	if msg == nil {
		r.left = append(r.left, leftSite{r.site(call), old, why})
		return
	}
	expr := r.kindExpr(kind, *msg)
	if r.allowBare && r.contextIsError(call) {
		r.usedBare = true
	} else {
		expr = r.svcerrPfx + "ToConnect(" + expr + ")"
	}
	r.edits = append(r.edits, txEdit{start: r.off(call.Pos()), end: r.off(call.End()), text: expr})
	r.done = append(r.done, ceDone{r.site(call), old + " → " + expr})
}

// unwrapToConnect drops a svcerr.ToConnect around an svcerr call where a
// bare kind is now allowed: the shape an earlier run left in a package a
// reader kept on *connect.Error, once that reader reads svcerr.Code. Through
// svcerr.Interceptor the two are the same error on the wire.
func (r *ceRewriter) unwrapToConnect(call *ast.CallExpr) {
	if !r.contextIsError(call) {
		return
	}
	inner, ok := ast.Unparen(call.Args[0]).(*ast.CallExpr)
	if !ok {
		return
	}
	if pkg, _ := funcOf(r.info, inner); pkg != svcerrPath {
		return
	}
	if !r.allowBare {
		// Still kept: the report names who keeps it, on this run too.
		r.keptWrapper = true
		return
	}
	text := r.text(inner)
	r.usedBare = true
	r.edits = append(r.edits, txEdit{start: r.off(call.Pos()), end: r.off(call.End()), text: text})
	r.done = append(r.done, ceDone{r.site(call), strings.Join(strings.Fields(r.text(call)), " ") + " → " + strings.Join(strings.Fields(text), " ")})
}

// codeKind maps a constant connect.Code<X> to svcerr's <X>.
func (r *ceRewriter) codeKind(e ast.Expr) (string, bool) {
	var id *ast.Ident
	switch x := ast.Unparen(e).(type) {
	case *ast.SelectorExpr:
		id = x.Sel
	case *ast.Ident:
		id = x
	default:
		return "", false
	}
	c, ok := r.info.Uses[id].(*types.Const)
	if !ok || c.Pkg() == nil || c.Pkg().Path() != connectPath || !strings.HasPrefix(c.Name(), "Code") {
		return "", false
	}
	kind := strings.TrimPrefix(c.Name(), "Code")
	if !svcerrKinds[kind] {
		return "", false
	}
	return kind, true
}

// svcerrKinds are the connect codes with an svcerr constructor of the same
// name — every code connect defines.
var svcerrKinds = map[string]bool{
	"Canceled": true, "Unknown": true, "InvalidArgument": true, "DeadlineExceeded": true,
	"NotFound": true, "AlreadyExists": true, "PermissionDenied": true, "ResourceExhausted": true,
	"FailedPrecondition": true, "Aborted": true, "OutOfRange": true, "Unimplemented": true,
	"Internal": true, "Unavailable": true, "DataLoss": true, "Unauthenticated": true,
}

// ceMessage is the message a rewritten site passes to its constructor.
type ceMessage struct {
	expr string // Go source of a string expression, never empty at run time
	// lit is the constant value when the message is a constant, for the
	// NotFound sentence; valid when isConst.
	lit     string
	isConst bool
	// fromLit: expr is a string literal the source wrote, so a NotFound
	// may be respelled from its value without losing a named constant.
	fromLit bool
}

// message reduces connect.NewError's error argument to the string the
// client reads, when that string can be passed to an svcerr constructor
// unchanged and is provably non-empty. Otherwise it says why not.
func (r *ceRewriter) message(e ast.Expr) (*ceMessage, string) {
	e = ast.Unparen(e)
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		if _, isNil := r.info.Uses[id].(*types.Nil); isNil {
			return nil, leftNil
		}
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, r.errorValueHint()
	}
	switch pkg, name := funcOf(r.info, call); {
	case pkg == "errors" && name == "New" && len(call.Args) == 1:
		arg := ast.Unparen(call.Args[0])
		if r.carriesError(arg) {
			return nil, r.errorValueHint()
		}
		if v, ok := r.constString(arg); ok {
			if v == "" {
				return nil, leftEmptyMessage
			}
			_, isLit := arg.(*ast.BasicLit)
			return &ceMessage{expr: r.text(arg), lit: v, isConst: true, fromLit: isLit}, ""
		}
		if inner, ok := arg.(*ast.CallExpr); ok {
			if p, n := funcOf(r.info, inner); p == "fmt" && n == "Sprintf" {
				if why := r.formatOK(inner); why != "" {
					return nil, why
				}
				return &ceMessage{expr: r.text(inner)}, ""
			}
		}
		return nil, leftComputedMessage
	case pkg == "fmt" && name == "Errorf" && len(call.Args) >= 1:
		if why := r.formatOK(call); why != "" {
			return nil, why
		}
		if v, _ := r.constString(call.Args[0]); len(call.Args) == 1 {
			arg := ast.Unparen(call.Args[0])
			_, isLit := arg.(*ast.BasicLit)
			if !strings.Contains(v, "%") {
				// No verbs and no arguments: Errorf renders the format as is.
				return &ceMessage{expr: r.text(arg), lit: v, isConst: true, fromLit: isLit}, ""
			}
			if text, verbs, _ := formatText(v); verbs == 0 {
				// Only %% escapes: the rendered text is a constant.
				return &ceMessage{expr: strconv.Quote(text), lit: text, isConst: true, fromLit: true}, ""
			}
		}
		// fmt.Errorf(f, args…) without %w renders exactly fmt.Sprintf(f, args…).
		fmtName := strings.TrimSuffix(r.text(call.Fun), "Errorf")
		return &ceMessage{expr: fmtName + "Sprintf" + string(r.src[r.off(call.Fun.End()):r.off(call.End())])}, ""
	}
	return nil, r.errorValueHint()
}

func (r *ceRewriter) errorValueHint() string { return leftErrorValue }

// leftSite is one connect.NewError the codemod did not rewrite.
type leftSite struct {
	at   ceSite
	call string // the call, whitespace collapsed
	why  string // one of leftReasons
}

// Why a site is left as written. Each worklist line carries one; the report
// explains each that occurs once, after the list.
const (
	leftErrorValue      = "wraps an error value"
	leftNil             = "nil error"
	leftComputedMessage = "computed message"
	leftEmptyMessage    = "empty message"
	leftComputedCode    = "computed code"
)

var leftReasons = []string{leftErrorValue, leftNil, leftComputedMessage, leftEmptyMessage, leftComputedCode}

var leftLegend = map[string]string{
	leftErrorValue: "the client reads the error's Error() text, unredacted. svcerr.WrapOr(err, svcerr.<Kind>(\"<summary>\")) redacts it and lets a classified err (a store's NotFound, a deadline) keep its code — which changes what the client reads, so it is a reviewed change. " +
		"Text the user should read goes in the constructor's argument (svcerr.FailedPrecondition(daemonMsg)); a remote Connect client's error is wrapped with svcerr.WithCause, never WrapOr.",
	leftNil:             "the client reads an empty message, and every svcerr kind has text: return the sentinel svcerr.Err<Kind> (its message is the category) or a constructor with real text.",
	leftComputedMessage: "the message is computed and could be empty, and an empty detail makes a constructor return its bare sentinel, whose text is not empty: pass the value to svcerr.<Kind>(…) if it never is.",
	leftEmptyMessage:    "no svcerr constructor publishes an empty message: give it one.",
	leftComputedCode:    "the code is not a connect.Code constant: name the kind at the site, or move the mapping to the producer (its sentinels become svcerr kinds) and svcerr.Wrap(err).",
}

// formatOK checks an Errorf / Sprintf call: a constant format with no %w,
// literal text outside its verbs (so the result is never ""), and no
// argument that is an error or calls Error() — those publish an error's text
// and belong on the worklist, not frozen into a constructor.
func (r *ceRewriter) formatOK(call *ast.CallExpr) string {
	f, ok := r.constString(call.Args[0])
	if !ok {
		return leftComputedMessage
	}
	lit, _, hasW := formatText(f)
	if hasW {
		return r.errorValueHint()
	}
	if lit == "" {
		return leftComputedMessage // no literal text: the result could be ""
	}
	for _, a := range call.Args[1:] {
		if r.carriesError(a) {
			return r.errorValueHint()
		}
	}
	return ""
}

var errorIface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// carriesError reports whether an argument is an error, or contains a call
// to an error's Error method.
func (r *ceRewriter) carriesError(e ast.Expr) bool {
	if t := r.info.TypeOf(e); t != nil && types.Implements(t, errorIface) {
		return true
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Error" {
			return true
		}
		if t := r.info.TypeOf(sel.X); t != nil && types.Implements(t, errorIface) {
			found = true
		}
		return true
	})
	return found
}

func (r *ceRewriter) constString(e ast.Expr) (string, bool) {
	tv, ok := r.info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// formatText returns the text a printf format prints on its own —
// everything but its verbs, with %% as % — how many verbs it has, and
// whether one is %w.
func formatText(f string) (lit string, verbs int, hasW bool) {
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			b.WriteByte(f[i])
			continue
		}
		i++
		if i < len(f) && f[i] == '%' {
			b.WriteByte('%')
			continue
		}
		// flags, [n], width, .precision, [n]
		for i < len(f) && strings.IndexByte("+-# 0", f[i]) >= 0 {
			i++
		}
		skipArgIndex := func() {
			if i < len(f) && f[i] == '[' {
				for i < len(f) && f[i] != ']' {
					i++
				}
				i++
			}
		}
		skipNum := func() {
			if i < len(f) && f[i] == '*' {
				i++
				return
			}
			for i < len(f) && f[i] >= '0' && f[i] <= '9' {
				i++
			}
		}
		skipArgIndex()
		skipNum()
		if i < len(f) && f[i] == '.' {
			i++
			skipArgIndex()
			skipNum()
		}
		skipArgIndex()
		// The loop's i++ steps over the verb; a format that ends mid-verb
		// still counts one (fmt prints %!(NOVERB) for it).
		verbs++
		if i < len(f) && f[i] == 'w' {
			hasW = true
		}
	}
	return b.String(), verbs, hasW
}

// kindExpr spells svcerr's constructor for kind with msg as its message.
func (r *ceRewriter) kindExpr(kind string, msg ceMessage) string {
	if kind != "NotFound" {
		return r.svcerrPfx + kind + "(" + msg.expr + ")"
	}
	// NotFound composes "<entity> not found" itself.
	if msg.isConst && msg.fromLit {
		if entity, ok := strings.CutSuffix(msg.lit, " not found"); ok && entity != "" {
			return r.svcerrPfx + "NotFound(" + strconv.Quote(entity) + ")"
		}
	}
	return r.svcerrPfx + "WithMessage(" + r.svcerrPfx + "ErrNotFound, " + msg.expr + ")"
}

// contextIsError reports whether the expression's context requires exactly
// the error interface — the one place a bare svcerr kind can replace a
// *connect.Error without changing a type.
func (r *ceRewriter) contextIsError(e ast.Expr) bool {
	t := r.contextType(e)
	return t != nil && types.Identical(t, types.Universe.Lookup("error").Type())
}

// contextType is the type the expression e is converted to where it is
// used, or nil when that is not a simple slot (a := declaration, a receiver,
// a composite literal, …).
func (r *ceRewriter) contextType(e ast.Expr) types.Type {
	var child ast.Node = e
	parent := r.parents[e]
	for {
		if p, ok := parent.(*ast.ParenExpr); ok {
			child, parent = p, r.parents[p]
			continue
		}
		break
	}
	switch p := parent.(type) {
	case *ast.ReturnStmt:
		sig := r.enclosingSig(p)
		if sig == nil || sig.Results().Len() != len(p.Results) {
			return nil
		}
		for i, res := range p.Results {
			if res == child {
				return sig.Results().At(i).Type()
			}
		}
	case *ast.AssignStmt:
		if p.Tok != token.ASSIGN || len(p.Lhs) != len(p.Rhs) {
			return nil
		}
		for i, rhs := range p.Rhs {
			if rhs == child {
				return r.info.TypeOf(p.Lhs[i])
			}
		}
	case *ast.ValueSpec:
		if p.Type == nil {
			return nil
		}
		return r.info.TypeOf(p.Type)
	case *ast.CallExpr:
		sig, ok := r.info.TypeOf(p.Fun).(*types.Signature)
		if !ok {
			return nil
		}
		for i, a := range p.Args {
			if a != child {
				continue
			}
			params := sig.Params()
			if sig.Variadic() && i >= params.Len()-1 {
				if p.Ellipsis.IsValid() {
					return nil
				}
				if s, ok := params.At(params.Len() - 1).Type().(*types.Slice); ok {
					return s.Elem()
				}
				return nil
			}
			if i < params.Len() {
				return params.At(i).Type()
			}
		}
	}
	return nil
}

// enclosingSig is the signature of the function (declaration or literal)
// whose body holds n.
func (r *ceRewriter) enclosingSig(n ast.Node) *types.Signature {
	for p := r.parents[n]; p != nil; p = r.parents[p] {
		switch fn := p.(type) {
		case *ast.FuncLit:
			sig, _ := r.info.TypeOf(fn).(*types.Signature)
			return sig
		case *ast.FuncDecl:
			if obj, ok := r.info.Defs[fn.Name].(*types.Func); ok {
				sig, _ := obj.Type().(*types.Signature)
				return sig
			}
			return nil
		}
	}
	return nil
}

// rewriteCodeOf rewrites a test file's connect.CodeOf(x) to svcerr.Code(x).
func (r *ceRewriter) rewriteCodeOf() {
	ast.Inspect(r.f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if pkg, name := funcOf(r.info, call); pkg != connectPath || name != "CodeOf" {
			return true
		}
		r.edits = append(r.edits, txEdit{start: r.off(call.Fun.Pos()), end: r.off(call.Fun.End()), text: r.svcerrPfx + "Code"})
		r.done = append(r.done, ceDone{r.site(call), fmt.Sprintf("%s → %sCode", r.text(call.Fun), r.svcerrPfx)})
		return true
	})
}

// apply makes the edits and fixes the imports.
func (r *ceRewriter) apply() ([]byte, error) {
	sort.Slice(r.edits, func(i, j int) bool { return r.edits[i].start > r.edits[j].start })
	out := append([]byte(nil), r.src...)
	for _, e := range r.edits {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, r.f.rel, out, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("re-parse %s after the rewrite: %w", r.f.rel, err)
	}
	if r.addImport {
		name := strings.TrimSuffix(r.svcerrPfx, ".")
		if name == "svcerr" {
			name = ""
		}
		astutil.AddNamedImport(fset, file, name, svcerrPath)
	}
	// The rewrite removes uses of errors.New, fmt.Errorf and connect; drop
	// an import it left unused.
	for path, def := range map[string]string{"errors": "errors", "fmt": "fmt", connectPath: "connect"} {
		name := importName(file, path, def)
		if name == "" || len(packageRefs(file, name, nil)) > 0 {
			continue
		}
		alias := ""
		if name != def {
			alias = name
		}
		astutil.DeleteNamedImport(fset, file, alias, path)
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("format %s: %w", r.f.rel, err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format %s: %w", r.f.rel, err)
	}
	return formatted, nil
}
