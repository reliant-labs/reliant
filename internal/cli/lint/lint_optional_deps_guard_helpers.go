// File: internal/cli/lint/lint_optional_deps_guard_helpers.go
//
// Guards behind same-package helpers, for optional-deps-guard.
//
// A handler package with several optional deps usually checks them in one
// place rather than at the top of every rpc:
//
//	func (s *Service) gate(ctx context.Context, orgID string, configured bool) error {
//		if s.deps.Roles == nil {
//			return svcerr.Unavailable("alerting is not configured")
//		}
//		...
//		if !configured {
//			return svcerr.Unavailable("alerting is not configured")
//		}
//		return nil
//	}
//
//	func (s *Service) ListAlerts(ctx context.Context, ...) (...) {
//		if err := s.gate(ctx, orgID, s.deps.State != nil); err != nil {
//			return nil, err
//		}
//		all, err := s.deps.State.LoadOwner(ctx, orgID) // guarded
//
// The walker reads that by SUMMARIZING the helper: walking its body with
// the same guard machinery and, at every exit that can be the helper's
// success, recording what is guarded there. What holds at every success
// exit is the summary — a set of optional fields, plus the indices of
// bool params that must be true (`configured` above). A call site applies
// it: after `if err := s.gate(...); err != nil { return }`, the fields are
// guarded, and so is whatever each such bool argument proves when true
// (`s.deps.State != nil` → State).
//
// Three helper shapes are summarized:
//
//	error  (last result `error`)  success = a return whose error is not
//	                               provably non-nil (errNonNil)
//	bool   (single `bool` result) success = a return whose result is not
//	                               the literal false; the result's own
//	                               condition keys hold there
//	void   (no results)           success = every return, and falling
//	                               off the end; a panic is not a success
//
// Which calls resolve: a method called on the function's own receiver
// (`s.gate(...)`, receiver type and method both in the package), and a
// method on Deps called on a deps value (`s.deps.ready()`, `d.ready()`).
// Package-level funcs do not resolve, since their guards would speak for
// a param rather than for the caller's deps; neither do calls through an
// interface or into another package. Recursion summarizes as nothing.
//
// Soundness. A success exit misread as a failure would credit the helper
// with a guard it does not establish, so errNonNil accepts only error
// expressions that cannot be nil: a composite literal or `&T{}`, an
// `Err*` sentinel, `errors.New`, `fmt.Errorf`, `connect.NewError`, a
// forge svcerr constructor, an svcerr wrapper of one of these, and a local
// the enclosing branch proved non-nil (`if err != nil { return err }`).
// Every other error expression, a call to some other function included,
// is taken to be a possible success, which under-credits the helper (flag
// more) rather than over-credits it.

package lint

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// Pseudo guard keys. They begin with a NUL byte, so no Go identifier (an
// optional field's name) can collide with them, and they never surface in
// a finding.
const (
	// nonNilKeyPrefix + name: the local `name` is known non-nil here.
	nonNilKeyPrefix = "\x00nonnil:"
	// trueParamKeyPrefix + name: bool param `name` of the helper being
	// summarized is known true here.
	trueParamKeyPrefix = "\x00true:"
)

// svcerrImportPath is forge's error package, whose constructors the
// walker knows never return nil.
const svcerrImportPath = "github.com/reliant-labs/forge/pkg/svcerr"

// svcerrConstructors are the svcerr functions that always return a
// non-nil error. TestSvcerrClassificationCoversPackage pins this list, and
// svcerrWrappers, against pkg/svcerr's exported API.
var svcerrConstructors = map[string]bool{
	// New takes a wire code, not an error: even invalid codes and empty
	// detail produce a non-nil error, so it is not a nil-preserving wrapper.
	"New":          true,
	"ScaffoldStub": true, "Canceled": true, "Unknown": true, "InvalidArgument": true,
	"DeadlineExceeded": true, "NotFound": true, "AlreadyExists": true,
	"PermissionDenied": true, "ResourceExhausted": true, "FailedPrecondition": true,
	"Aborted": true, "OutOfRange": true, "Unimplemented": true, "Internal": true,
	"Unavailable": true, "DataLoss": true, "Unauthenticated": true, "PlanLimit": true,
	"InsufficientBalance": true, "Expired": true, "Draining": true,
}

// svcerrWrappers are the svcerr functions that return nil exactly when
// their first argument is nil.
var svcerrWrappers = map[string]bool{
	"Wrap": true, "WrapOr": true, "WithCause": true, "WithClass": true, "WithDetail": true,
	"WithMessage": true, "WithReason": true, "WithMetadata": true,
}

// helperKind is the result shape of a summarizable helper.
type helperKind int

const (
	helperVoid helperKind = iota + 1
	helperBool
	helperErr
)

// helperSummary is what a helper proves at every one of its success exits.
type helperSummary struct {
	kind helperKind
	// fields are the optional fields non-nil at every success exit.
	fields map[string]bool
	// trueParams are the indices of bool params true at every success
	// exit; a call site's argument there contributes its condition keys.
	trueParams []int
}

// helperFunc is one function declaration of the package, with the
// imports of the file that declares it.
type helperFunc struct {
	decl    *ast.FuncDecl
	imports map[string]string
}

// optionalGuardPackage is the package-wide state the walker consults: the
// optional fields, and the helpers a guard may hide behind.
type optionalGuardPackage struct {
	fset      *token.FileSet
	optional  map[string]optionalFieldShape
	funcs     map[string]helperFunc
	summaries map[string]*helperSummary
	// inProgress marks the helpers being summarized; a call back into
	// one (recursion) summarizes as nothing.
	inProgress map[string]bool
	// errVars are the package-level vars named Err*: sentinels, so
	// returning one is a failure exit.
	errVars map[string]bool
}

func newOptionalGuardPackage(fset *token.FileSet) *optionalGuardPackage {
	return &optionalGuardPackage{
		fset:       fset,
		optional:   map[string]optionalFieldShape{},
		funcs:      map[string]helperFunc{},
		summaries:  map[string]*helperSummary{},
		inProgress: map[string]bool{},
		errVars:    map[string]bool{},
	}
}

// collect records a file's function declarations and Err* sentinels.
func (p *optionalGuardPackage) collect(f *ast.File) {
	imports := fileImports(f)
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil || d.Recv == nil {
				continue
			}
			if recv := receiverTypeName(d); recv != "" {
				p.funcs[recv+"."+d.Name.Name] = helperFunc{decl: d, imports: imports}
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if strings.HasPrefix(n.Name, "Err") || strings.HasPrefix(n.Name, "err") {
						p.errVars[n.Name] = true
					}
				}
			}
		}
	}
}

// newWalker builds the walker for one function body. Findings land in
// findings; a summarizing walker passes a throwaway slice.
func (p *optionalGuardPackage) newWalker(fn *ast.FuncDecl, imports map[string]string, findings *[]optionalDepsGuardFinding) *optionalGuardWalker {
	w := &optionalGuardWalker{
		fset:        p.fset,
		optional:    p.optional,
		funcName:    fn.Name.Name,
		depsIdents:  map[string]bool{},
		depsAliases: map[string]bool{},
		aliases:     map[string]string{},
		elemAliases: map[string]string{},
		findings:    findings,
		helpers:     p,
		imports:     imports,
		recvType:    receiverTypeName(fn),
		errBindings: map[string]errBinding{},
	}
	// Receiver / params typed Deps (or *Deps) root the bare `d.X` form —
	// methods on Deps itself (validateDeps-style helpers) and
	// constructors like `func New(deps Deps)`.
	if fn.Recv != nil {
		for _, f := range fn.Recv.List {
			for _, n := range f.Names {
				w.recvName = n.Name
				if isDepsType(f.Type) {
					w.depsIdents[n.Name] = true
				}
			}
		}
	}
	if fn.Type.Params != nil {
		for _, f := range fn.Type.Params.List {
			if isDepsType(f.Type) {
				for _, n := range f.Names {
					w.depsIdents[n.Name] = true
				}
			}
		}
	}
	return w
}

// summary returns the helper's summary, computing it on first use, or nil
// when key names no summarizable helper.
func (p *optionalGuardPackage) summary(key string) *helperSummary {
	if s, ok := p.summaries[key]; ok {
		return s
	}
	hf, ok := p.funcs[key]
	if !ok || p.inProgress[key] {
		return nil
	}
	kind := helperKindOf(hf.decl)
	if kind == 0 {
		p.summaries[key] = nil
		return nil
	}
	p.inProgress[key] = true
	defer delete(p.inProgress, key)

	var discard []optionalDepsGuardFinding
	w := p.newWalker(hf.decl, hf.imports, &discard)
	// Only the receiver roots a guard here: a check on a Deps-typed param
	// proves nothing about the deps the call site holds.
	w.depsIdents = map[string]bool{}
	if isDepsType(hf.decl.Recv.List[0].Type) && w.recvName != "" {
		w.depsIdents[w.recvName] = true
	}
	w.summary = &summaryAccumulator{kind: kind}
	w.boolParams = unassignedBoolParams(hf.decl)

	guarded := map[string]bool{}
	w.walkStmts(hf.decl.Body.List, guarded)
	if kind == helperVoid && !blockAlwaysExits(hf.decl.Body) {
		w.summary.add(guarded)
	}

	s := &helperSummary{kind: kind, fields: map[string]bool{}}
	for k := range w.summary.common {
		switch {
		case strings.HasPrefix(k, trueParamKeyPrefix):
			if i, ok := w.boolParams[strings.TrimPrefix(k, trueParamKeyPrefix)]; ok {
				s.trueParams = append(s.trueParams, i)
			}
		case strings.HasPrefix(k, "\x00"):
		default:
			s.fields[k] = true
		}
	}
	p.summaries[key] = s
	return s
}

// summaryAccumulator intersects the guards of a helper's success exits.
type summaryAccumulator struct {
	kind   helperKind
	paths  int
	common map[string]bool
}

func (a *summaryAccumulator) add(guarded map[string]bool) {
	if a.paths == 0 {
		a.common = copyGuards(guarded, nil)
	} else {
		for k := range a.common {
			if !guarded[k] {
				delete(a.common, k)
			}
		}
	}
	a.paths++
}

// errBinding is what a local error variable's nil value proves.
type errBinding struct {
	guards map[string]bool
	depth  int
}

// recordReturn records a success exit of the helper being summarized.
func (w *optionalGuardWalker) recordReturn(r *ast.ReturnStmt, guarded map[string]bool) {
	if w.summary == nil || w.closureDepth > 0 {
		return
	}
	if len(r.Results) == 0 {
		// void, or a bare return of named results we do not track.
		w.summary.add(guarded)
		return
	}
	last := r.Results[len(r.Results)-1]
	switch w.summary.kind {
	case helperBool:
		if id, ok := unparen(last).(*ast.Ident); ok && id.Name == "false" {
			return
		}
		w.summary.add(copyGuards(guarded, w.condKeys(last, true)))
	case helperErr:
		if w.errNonNil(last, guarded) {
			return
		}
		w.summary.add(copyGuards(guarded, w.errorGuards(last)))
	default:
		w.summary.add(guarded)
	}
}

// errNonNil reports whether an error expression cannot be nil (see the
// file header for why the list is closed).
func (w *optionalGuardWalker) errNonNil(e ast.Expr, guarded map[string]bool) bool {
	switch v := unparen(e).(type) {
	case *ast.Ident:
		return guarded[nonNilKeyPrefix+v.Name] || w.helpers.errVars[v.Name]
	case *ast.UnaryExpr:
		_, lit := unparen(v.X).(*ast.CompositeLit)
		return v.Op == token.AND && lit
	case *ast.CompositeLit:
		return true
	case *ast.SelectorExpr:
		_, isImport := w.importPath(v.X)
		return isImport && strings.HasPrefix(v.Sel.Name, "Err")
	case *ast.CallExpr:
		sel, ok := unparen(v.Fun).(*ast.SelectorExpr)
		if !ok {
			return false
		}
		path, ok := w.importPath(sel.X)
		if !ok {
			return false
		}
		name := sel.Sel.Name
		switch {
		case path == "errors" && name == "New",
			path == "fmt" && name == "Errorf",
			path == "connectrpc.com/connect" && name == "NewError":
			return true
		case path == svcerrImportPath && svcerrConstructors[name]:
			return true
		case path == svcerrImportPath && svcerrWrappers[name]:
			return len(v.Args) > 0 && w.errNonNil(v.Args[0], guarded)
		}
	}
	return false
}

// isDepsValue reports whether e denotes a whole Deps value: a Deps ident,
// or `x.deps` — while summarizing, the receiver's only.
func (w *optionalGuardWalker) isDepsValue(e ast.Expr) bool {
	switch v := unparen(e).(type) {
	case *ast.Ident:
		return w.depsIdents[v.Name]
	case *ast.SelectorExpr:
		base, ok := unparen(v.X).(*ast.Ident)
		if !ok || (v.Sel.Name != "deps" && v.Sel.Name != "Deps") {
			return false
		}
		return w.summary == nil || base.Name == w.recvName
	}
	return false
}

// importPath resolves an ident naming an import of the current file.
func (w *optionalGuardWalker) importPath(e ast.Expr) (string, bool) {
	id, ok := unparen(e).(*ast.Ident)
	if !ok {
		return "", false
	}
	path, ok := w.imports[id.Name]
	return path, ok
}

// errorGuards is what a nil value of e proves: e is an error variable
// bound to a helper call, or a helper call itself.
func (w *optionalGuardWalker) errorGuards(e ast.Expr) map[string]bool {
	switch v := unparen(e).(type) {
	case *ast.Ident:
		if b, ok := w.errBindings[v.Name]; ok {
			return b.guards
		}
	case *ast.CallExpr:
		return w.helperCallGuards(v, helperErr)
	}
	return nil
}

// helperCallGuards is what a successful call proves, when call resolves to
// a same-package helper of the given kind: its fields, plus the condition
// keys of each argument passed for a param it requires true.
func (w *optionalGuardWalker) helperCallGuards(call *ast.CallExpr, kind helperKind) map[string]bool {
	key, ok := w.helperKey(call)
	if !ok {
		return nil
	}
	s := w.helpers.summary(key)
	if s == nil || s.kind != kind {
		return nil
	}
	out := copyGuards(s.fields, nil)
	if call.Ellipsis == token.NoPos {
		for _, i := range s.trueParams {
			if i < len(call.Args) {
				out = mergeKeys(out, w.condKeys(call.Args[i], true))
			}
		}
	}
	return out
}

// helperKey resolves a call to the package-funcs key of the method it
// calls: `recv.m(...)` on the function's own receiver, or `<deps>.m(...)`
// on a Deps value.
func (w *optionalGuardWalker) helperKey(call *ast.CallExpr) (string, bool) {
	sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	switch x := unparen(sel.X).(type) {
	case *ast.Ident:
		if w.depsIdents[x.Name] {
			return "Deps." + sel.Sel.Name, true
		}
		if x.Name == w.recvName && w.recvName != "_" && w.recvType != "" {
			return w.recvType + "." + sel.Sel.Name, true
		}
	case *ast.SelectorExpr:
		if base, isID := unparen(x.X).(*ast.Ident); isID && base.Name == w.recvName &&
			(x.Sel.Name == "deps" || x.Sel.Name == "Deps") {
			return "Deps." + sel.Sel.Name, true
		}
	}
	return "", false
}

// recordErrBindings binds the error variable an assignment fills from a
// helper call (`err := s.gate(...)`, `v, err := s.load(...)`), and drops
// any binding the assignment overwrites.
func (w *optionalGuardWalker) recordErrBindings(lhs, rhs []ast.Expr) {
	for _, l := range lhs {
		if id, ok := unparen(l).(*ast.Ident); ok {
			delete(w.errBindings, id.Name)
		}
	}
	var name string
	var value ast.Expr
	switch {
	case len(lhs) == len(rhs) && len(lhs) == 1:
		value = rhs[0]
	case len(rhs) == 1 && len(lhs) > 1:
		value = rhs[0] // a multi-value call: the error is the last result
	default:
		return
	}
	id, ok := unparen(lhs[len(lhs)-1]).(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	name = id.Name
	call, ok := unparen(value).(*ast.CallExpr)
	if !ok {
		return
	}
	if g := w.helperCallGuards(call, helperErr); len(g) > 0 {
		w.errBindings[name] = errBinding{guards: g, depth: w.depth}
	}
}

// dropInitBindings forgets the bindings an if/switch init declared: they
// are scoped to that statement, and a same-named variable after it is a
// different one.
func (w *optionalGuardWalker) dropInitBindings(init ast.Stmt) {
	as, ok := init.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE {
		return
	}
	for _, l := range as.Lhs {
		if id, ok := unparen(l).(*ast.Ident); ok {
			delete(w.errBindings, id.Name)
		}
	}
}

// helperKindOf classifies a function's results, or returns 0 when it is
// not a shape the walker summarizes.
func helperKindOf(fn *ast.FuncDecl) helperKind {
	res := fn.Type.Results
	if res == nil || len(res.List) == 0 {
		return helperVoid
	}
	last := res.List[len(res.List)-1]
	id, ok := last.Type.(*ast.Ident)
	if !ok {
		return 0
	}
	switch {
	case id.Name == "error":
		return helperErr
	case id.Name == "bool" && len(res.List) == 1 && len(last.Names) <= 1:
		return helperBool
	}
	return 0
}

// receiverTypeName is the bare type name of a method's receiver
// (`*Service[T]` → "Service"), or "" for a func.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch v := t.(type) {
	case *ast.IndexExpr:
		t = v.X
	case *ast.IndexListExpr:
		t = v.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// unassignedBoolParams maps each bool param the body never assigns (or
// takes the address of) to its index. A reassigned param's value at an
// exit is not the caller's argument, so it cannot carry a guard.
func unassignedBoolParams(fn *ast.FuncDecl) map[string]int {
	out := map[string]int{}
	i := 0
	for _, f := range fn.Type.Params.List {
		id, isBool := f.Type.(*ast.Ident)
		isBool = isBool && id.Name == "bool"
		if len(f.Names) == 0 {
			i++
			continue
		}
		for _, n := range f.Names {
			if isBool && n.Name != "_" {
				out[n.Name] = i
			}
			i++
		}
	}
	if len(out) == 0 {
		return out
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for _, l := range v.Lhs {
				if id, ok := unparen(l).(*ast.Ident); ok {
					delete(out, id.Name)
				}
			}
		case *ast.UnaryExpr:
			if id, ok := unparen(v.X).(*ast.Ident); ok && v.Op == token.AND {
				delete(out, id.Name)
			}
		}
		return true
	})
	return out
}

// fileImports maps a file's import names to their paths; an unnamed import
// is named by its last path element.
func fileImports(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		out[name] = path
	}
	return out
}
