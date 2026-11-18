package codemod

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// connecterrors_readers.go — who reads a *connect.Error in process, and
// which handler packages each can see. A handler package with any such
// reader keeps its rewrites *connect.Error (svcerr.ToConnect), and the
// report names every one of them and how it reaches the package.

// ceReader is one in-process read of a *connect.Error.
type ceReader struct {
	at     ceSite
	fn     string // the enclosing function, "pkg.Func" / "pkg.Type.Method"; "" at package level
	what   string // "connect.CodeOf", "errors.As into a *connect.Error", …
	codeOf bool   // a connect.CodeOf, which rewriteTests turns into svcerr.Code in a test
}

// How a reader reaches the package it keeps wrapped.
const (
	viaOwn       = "own"        // in the package's code
	viaTest      = "test"       // in the package's own tests
	viaTestScope = "test-scope" // a connect.CodeOf in its own tests, outside --scope
	viaImport    = "import"     // in the code of a package importing it
	viaIface     = "iface"      // in the code of a package calling it through an interface
)

// ceBlock is one reader that keeps a package's rewrites *connect.Error.
type ceBlock struct {
	reader ceReader
	via    string
	from   string       // the reading package, module-relative (viaImport, viaIface)
	call   *ceIfaceCall // viaIface: the interface call that reaches the package
	impl   string       // viaIface: the package's type implementing it
	wired  ceSite       // viaIface: where a value of that type is handed to the interface
}

func (c *connectErrorsRun) renderBlock(b ceBlock) string {
	s := c.at(b.reader.at)
	if b.reader.fn != "" {
		s += " " + b.reader.fn
	}
	s += " (" + b.reader.what + ") — "
	switch b.via {
	case viaOwn:
		return s + "in the package"
	case viaTest:
		return s + "in its own tests, which call it directly"
	case viaTestScope:
		return s + "in its own tests, which call it directly, outside --scope: this run may not switch it to svcerr.Code (add the file to --scope)"
	case viaImport:
		return s + b.from + " imports it"
	default:
		return fmt.Sprintf("%s%s calls it through the interface %s.%s at %s, which %s implements and %s hands it",
			s, b.from, b.call.iface, b.call.method, c.at(b.call.at), b.impl, c.at(b.wired))
	}
}

// ceIfaceCall is a call through an interface method that returns an error.
type ceIfaceCall struct {
	named  *types.Named
	iface  string // as printed: pkg.Name
	method string
	at     ceSite
}

// readerIndex knows which packages read a *connect.Error in process, and
// how each package can see another's errors.
//
// A test's connect.CodeOf is not a reader — rewriteTests turns it into
// svcerr.Code — unless --scope keeps the file from being rewritten; a test's
// errors.As into a *connect.Error, or a type assertion on one, is: it would
// fail on a bare kind, and it is not mechanical to change.
//
// A production reader blocks its own package, every package its package
// imports (a value one call away reaches it), and every handler package
// whose type implements an interface its package calls (composition wires
// the handler in; nothing is imported). A test reader blocks only its own
// package: a handler package's tests call its functions directly, while a
// test in an importer — a composition root's route test — reads a client's
// error off the wire, which stays a *connect.Error.
type readerIndex struct {
	c          *connectErrorsRun
	prod       map[string][]ceReader // package → production readers
	tests      map[string][]ceReader // package → readers in its own tests
	testCodeOf map[string][]ceReader // package → connect.CodeOf in its own tests
	// prodImporters: package → the packages whose CODE imports it.
	prodImporters map[string]map[string]bool
	// importers: package → the packages whose code or tests import it.
	importers map[string]map[string]bool
	plain     map[string]*packages.Package // import path → its non-test variant
	calls     map[string][]ceIfaceCall     // package → its code's interface calls (lazily)
	// convs: an interface declared in this module → every value production
	// code converts to it (lazily).
	convs map[*types.TypeName][]ceConv
}

// ceConv is one value converted to an interface: its static type, and where.
type ceConv struct {
	src types.Type
	at  ceSite
}

func newReaderIndex(c *connectErrorsRun, files []ceFile, pkgs []*packages.Package) *readerIndex {
	idx := &readerIndex{c: c,
		prod: map[string][]ceReader{}, tests: map[string][]ceReader{}, testCodeOf: map[string][]ceReader{},
		prodImporters: map[string]map[string]bool{}, importers: map[string]map[string]bool{},
		plain: map[string]*packages.Package{}, calls: map[string][]ceIfaceCall{}}
	w := newWireAnalyzer(pkgs, c.relOf)
	for _, f := range files {
		pkg := pkgPathOf(f.pkg)
		for _, rd := range connectReaders(f, w) {
			switch {
			case !f.test:
				idx.prod[pkg] = append(idx.prod[pkg], rd)
			case rd.codeOf:
				idx.testCodeOf[pkg] = append(idx.testCodeOf[pkg], rd)
			default:
				idx.tests[pkg] = append(idx.tests[pkg], rd)
			}
		}
	}
	for _, m := range []map[string][]ceReader{idx.prod, idx.tests, idx.testCodeOf} {
		for pkg := range m {
			rs := m[pkg]
			sort.Slice(rs, func(i, j int) bool {
				if rs[i].at.rel != rs[j].at.rel {
					return rs[i].at.rel < rs[j].at.rel
				}
				return rs[i].at.line < rs[j].at.line
			})
		}
	}
	add := func(m map[string]map[string]bool, imp, q string) {
		if m[imp] == nil {
			m[imp] = map[string]bool{}
		}
		m[imp][q] = true
	}
	for _, p := range pkgs {
		plain := p.ID == p.PkgPath
		if plain {
			idx.plain[p.PkgPath] = p
		}
		for imp := range p.Imports {
			add(idx.importers, imp, pkgPathOf(p))
			if plain {
				add(idx.prodImporters, imp, p.PkgPath)
			}
		}
	}
	return idx
}

// blockers lists every reader that keeps pkg's rewrites a *connect.Error,
// with how it reaches pkg; none when a bare kind is safe.
func (idx *readerIndex) blockers(pkg string) []ceBlock {
	var out []ceBlock
	for _, rd := range idx.prod[pkg] {
		out = append(out, ceBlock{reader: rd, via: viaOwn})
	}
	for _, rd := range idx.tests[pkg] {
		out = append(out, ceBlock{reader: rd, via: viaTest})
	}
	for _, rd := range idx.testCodeOf[pkg] {
		if !idx.c.scope.Contains(rd.at.rel) {
			out = append(out, ceBlock{reader: rd, via: viaTestScope})
		}
	}
	readers := make([]string, 0, len(idx.prod))
	for q := range idx.prod {
		if q != pkg {
			readers = append(readers, q)
		}
	}
	sort.Strings(readers)
	for _, q := range readers {
		if idx.prodImporters[pkg][q] {
			for _, rd := range idx.prod[q] {
				out = append(out, ceBlock{reader: rd, via: viaImport, from: idx.c.relPkg(q)})
			}
			continue
		}
		if call, impl, wired := idx.reachesThroughInterface(q, pkg); call != nil {
			for _, rd := range idx.prod[q] {
				out = append(out, ceBlock{reader: rd, via: viaIface, from: idx.c.relPkg(q), call: call, impl: impl, wired: wired})
			}
		}
	}
	return out
}

// reachesThroughInterface returns the first call in q's code through an
// interface that a type declared in p implements AND that production code
// hands a value of that type — the type, and where it is handed over. An
// interface only ever handed a generated client (a CLI's) satisfies the
// handler's method set too, but its calls go over the wire.
func (idx *readerIndex) reachesThroughInterface(q, p string) (*ceIfaceCall, string, ceSite) {
	pp := idx.plain[p]
	if pp == nil || pp.Types == nil {
		return nil, "", ceSite{}
	}
	calls := idx.ifaceCalls(q)
	if len(calls) == 0 {
		return nil, "", ceSite{}
	}
	var impls []types.Type
	scope := pp.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok || named.TypeParams().Len() > 0 {
			continue
		}
		if _, isIface := named.Underlying().(*types.Interface); isIface {
			continue
		}
		impls = append(impls, named, types.NewPointer(named))
	}
	qual := func(p *types.Package) string { return p.Name() }
	for i := range calls {
		iface := calls[i].named.Underlying().(*types.Interface)
		for _, t := range impls {
			if !types.Implements(t, iface) {
				continue
			}
			if at, ok := idx.handedTo(calls[i].named, t); ok {
				return &calls[i], types.TypeString(t, qual), at
			}
		}
	}
	return nil, "", ceSite{}
}

// handedTo reports where production code converts a value of type t to
// iface — directly, or to an interface of this module that is in turn
// converted to iface.
func (idx *readerIndex) handedTo(iface *types.Named, t types.Type) (ceSite, bool) {
	convs := idx.conversions()
	seen := map[*types.TypeName]bool{}
	var walk func(n *types.Named) (ceSite, bool)
	walk = func(n *types.Named) (ceSite, bool) {
		obj := n.Origin().Obj()
		if seen[obj] {
			return ceSite{}, false
		}
		seen[obj] = true
		for _, cv := range convs[obj] {
			if types.Identical(cv.src, t) {
				return cv.at, true
			}
			if sn, ok := cv.src.(*types.Named); ok {
				if _, isIface := sn.Underlying().(*types.Interface); isIface {
					if at, ok := walk(sn); ok {
						return at, true
					}
				}
			}
		}
		return ceSite{}, false
	}
	return walk(iface)
}

// conversions indexes every value the module's production code converts to
// an interface it declares: a call's argument, an assignment, a var
// declaration, a composite literal's field or element, a return, an explicit
// conversion. Composition hands a handler to the consumer's interface in one
// of these.
func (idx *readerIndex) conversions() map[*types.TypeName][]ceConv {
	if idx.convs != nil {
		return idx.convs
	}
	idx.convs = map[*types.TypeName][]ceConv{}
	paths := make([]string, 0, len(idx.plain))
	for path := range idx.plain {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		p := idx.plain[path]
		if p.TypesInfo == nil {
			continue
		}
		cs := &convScan{idx: idx, p: p, info: p.TypesInfo}
		for _, f := range p.Syntax {
			cs.file(f)
		}
	}
	return idx.convs
}

// convScan finds one package's conversions to the module's interfaces.
type convScan struct {
	idx  *readerIndex
	p    *packages.Package
	info *types.Info
	sigs []*types.Signature // the enclosing functions', innermost last
}

func (cs *convScan) file(f *ast.File) {
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			switch stack[len(stack)-1].(type) {
			case *ast.FuncDecl, *ast.FuncLit:
				cs.sigs = cs.sigs[:len(cs.sigs)-1]
			}
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		cs.node(n)
		return true
	})
}

func (cs *convScan) node(n ast.Node) {
	switch x := n.(type) {
	case *ast.FuncDecl:
		sig, _ := cs.info.TypeOf(x.Name).(*types.Signature)
		cs.sigs = append(cs.sigs, sig)
	case *ast.FuncLit:
		sig, _ := cs.info.TypeOf(x).(*types.Signature)
		cs.sigs = append(cs.sigs, sig)
	case *ast.CallExpr:
		cs.call(x)
	case *ast.AssignStmt:
		if len(x.Lhs) == len(x.Rhs) {
			for i := range x.Lhs {
				cs.record(cs.info.TypeOf(x.Lhs[i]), x.Rhs[i])
			}
		}
	case *ast.ValueSpec:
		if x.Type != nil && len(x.Values) == len(x.Names) {
			for _, v := range x.Values {
				cs.record(cs.info.TypeOf(x.Type), v)
			}
		}
	case *ast.ReturnStmt:
		if len(cs.sigs) == 0 {
			break
		}
		if sig := cs.sigs[len(cs.sigs)-1]; sig != nil && sig.Results().Len() == len(x.Results) {
			for i, r := range x.Results {
				cs.record(sig.Results().At(i).Type(), r)
			}
		}
	case *ast.CompositeLit:
		cs.compositeLit(x)
	}
}

// call: an explicit conversion, or the arguments handed to parameters.
func (cs *convScan) call(x *ast.CallExpr) {
	if tv, ok := cs.info.Types[x.Fun]; ok && tv.IsType() {
		if len(x.Args) == 1 {
			cs.record(tv.Type, x.Args[0])
		}
		return
	}
	sig, ok := cs.info.TypeOf(x.Fun).(*types.Signature)
	if !ok {
		return
	}
	params := sig.Params()
	for i, a := range x.Args {
		switch {
		case sig.Variadic() && i >= params.Len()-1:
			if s, ok := params.At(params.Len() - 1).Type().(*types.Slice); ok && !x.Ellipsis.IsValid() {
				cs.record(s.Elem(), a)
			}
		case i < params.Len():
			cs.record(params.At(i).Type(), a)
		}
	}
}

// compositeLit: a struct's fields, a slice's, array's or map's elements.
func (cs *convScan) compositeLit(x *ast.CompositeLit) {
	t := cs.info.TypeOf(x)
	if t == nil {
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i, el := range x.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				if i < u.NumFields() {
					cs.record(u.Field(i).Type(), el)
				}
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok {
				if fv, ok := cs.info.Uses[key].(*types.Var); ok {
					cs.record(fv.Type(), kv.Value)
				}
			}
		}
	case *types.Slice, *types.Array, *types.Map:
		elem := u.(interface{ Elem() types.Type }).Elem()
		for _, el := range x.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			cs.record(elem, el)
		}
	}
}

// record notes src converted to target, when target is an interface
// declared in this module and src is not already that type.
func (cs *convScan) record(target types.Type, src ast.Expr) {
	named, ok := target.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || !cs.idx.inModule(named.Obj().Pkg().Path()) {
		return
	}
	if _, ok := named.Underlying().(*types.Interface); !ok {
		return
	}
	st := cs.info.TypeOf(src)
	if st == nil || types.Identical(st, target) {
		return
	}
	pos := cs.p.Fset.Position(src.Pos())
	obj := named.Origin().Obj()
	cs.idx.convs[obj] = append(cs.idx.convs[obj], ceConv{src: st, at: ceSite{cs.idx.c.relOf(pos.Filename), pos.Line}})
}

func (idx *readerIndex) inModule(path string) bool {
	return path == idx.c.modPath || strings.HasPrefix(path, idx.c.modPath+"/")
}

// ifaceCalls lists q's production calls through a method of an interface
// declared in this module that returns an error: the seam a handler is
// wired into by composition. error itself (and anything with an Error
// method) is not such a seam.
func (idx *readerIndex) ifaceCalls(q string) []ceIfaceCall {
	if calls, ok := idx.calls[q]; ok {
		return calls
	}
	var calls []ceIfaceCall
	p := idx.plain[q]
	if p != nil && p.TypesInfo != nil {
		inModule := idx.inModule
		seen := map[string]bool{}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
				if !ok || isWireCall(p.TypesInfo, call) {
					// A generated client has a handler's method set, but
					// calling it goes over the wire.
					return true
				}
				s := p.TypesInfo.Selections[sel]
				if s == nil || s.Kind() != types.MethodVal {
					return true
				}
				named, ok := s.Recv().(*types.Named)
				if !ok || named.Obj().Pkg() == nil || !inModule(named.Obj().Pkg().Path()) {
					return true
				}
				iface, ok := named.Underlying().(*types.Interface)
				if !ok || hasMethod(iface, "Error") {
					return true
				}
				fn, ok := s.Obj().(*types.Func)
				if !ok || !returnsError(fn.Type().(*types.Signature)) {
					return true
				}
				key := named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + fn.Name()
				if seen[key] {
					return true
				}
				seen[key] = true
				pos := p.Fset.Position(call.Pos())
				calls = append(calls, ceIfaceCall{
					named:  named,
					iface:  named.Obj().Pkg().Name() + "." + named.Obj().Name(),
					method: fn.Name(),
					at:     ceSite{idx.c.relOf(pos.Filename), pos.Line},
				})
				return true
			})
		}
	}
	sort.SliceStable(calls, func(i, j int) bool {
		if calls[i].at.rel != calls[j].at.rel {
			return calls[i].at.rel < calls[j].at.rel
		}
		return calls[i].at.line < calls[j].at.line
	})
	idx.calls[q] = calls
	return calls
}

func hasMethod(iface *types.Interface, name string) bool {
	for i := range iface.NumMethods() {
		if iface.Method(i).Name() == name {
			return true
		}
	}
	return false
}

func returnsError(sig *types.Signature) bool {
	for i := range sig.Results().Len() {
		if types.Implements(sig.Results().At(i).Type(), errorIface) {
			return true
		}
	}
	return false
}

// connectReaders lists a file's in-process readers of a *connect.Error:
// connect.CodeOf, errors.As into one, any other call handed a
// **connect.Error As target, and type assertions or switches on one. Each
// would read a bare svcerr kind differently — unless what it reads is a
// Connect client's error (wireAnalyzer), which stays a *connect.Error
// whatever a handler built.
//
// The As target is matched by the ARGUMENT's type, not the callee: testify's
// require.ErrorAs / assert.ErrorAs (and their suite methods) take the target
// as interface{}, and a project's own helper can be spelled any way, but every
// one of them is handed `&ce` with ce a *connect.Error, and fails on a bare
// kind exactly as errors.As does.
func connectReaders(f ceFile, w *wireAnalyzer) []ceReader {
	rs := &readerScan{f: f, w: w, info: f.pkg.TypesInfo}
	for _, d := range f.file.Decls {
		fn, _ := d.(*ast.FuncDecl)
		rs.fn = fn
		ast.Inspect(d, rs.node)
	}
	return rs.sites
}

// readerScan walks one file for connectReaders.
type readerScan struct {
	f     ceFile
	w     *wireAnalyzer
	info  *types.Info
	fn    *ast.FuncDecl // the declaration being walked; nil at package level
	sites []ceReader
}

// add records the read at n of the error read — unless that error came off
// the wire.
func (rs *readerScan) add(n ast.Node, read ast.Expr, what string, codeOf bool) {
	if read != nil && rs.w.offWire(rs.info, rs.fn, read) {
		return
	}
	rs.sites = append(rs.sites, ceReader{
		at:   ceSite{rs.f.rel, rs.f.pkg.Fset.Position(n.Pos()).Line},
		fn:   funcName(rs.f.pkg.Types, rs.fn),
		what: what, codeOf: codeOf,
	})
}

func (rs *readerScan) node(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.CallExpr:
		rs.call(x)
	case *ast.TypeAssertExpr:
		if x.Type != nil && isConnErr(rs.info.TypeOf(x.Type)) {
			rs.add(x, x.X, "type assertion to *connect.Error", false)
		}
	case *ast.TypeSwitchStmt:
		read := typeSwitchSubject(x)
		for _, cl := range x.Body.List {
			for _, t := range cl.(*ast.CaseClause).List {
				if isConnErr(rs.info.TypeOf(t)) {
					rs.add(t, read, "type switch on *connect.Error", false)
				}
			}
		}
	}
	return true
}

func (rs *readerScan) call(x *ast.CallExpr) {
	switch pkg, name := funcOf(rs.info, x); {
	case pkg == connectPath && name == "CodeOf" && len(x.Args) == 1:
		rs.add(x, x.Args[0], "connect.CodeOf", true)
		return
	case pkg == "errors" && name == "As" && len(x.Args) == 2 && isConnErr(rs.info.TypeOf(x.Args[1])):
		rs.add(x, x.Args[0], "errors.As into a *connect.Error", false)
		return
	}
	for _, arg := range x.Args {
		if !isAsTarget(rs.info.TypeOf(arg)) {
			continue
		}
		// What it reads is the error handed beside the target.
		var read ast.Expr
		for _, a := range x.Args {
			if t := rs.info.TypeOf(a); a != arg && t != nil && types.Implements(t, errorIface) {
				read = a
				break
			}
		}
		rs.add(x, read, types.ExprString(x.Fun)+" into a *connect.Error", false)
		return
	}
}

// isConnErr reports whether t is *connect.Error, or a pointer to one.
func isConnErr(t types.Type) bool {
	for t != nil {
		ptr, ok := t.(*types.Pointer)
		if !ok {
			return false
		}
		if named, ok := ptr.Elem().(*types.Named); ok && named.Obj().Pkg() != nil &&
			named.Obj().Pkg().Path() == connectPath && named.Obj().Name() == "Error" {
			return true
		}
		t = ptr.Elem()
	}
	return false
}

// isAsTarget is exactly **connect.Error: the address of a *connect.Error
// variable, which is what an As-style call fills in.
func isAsTarget(t types.Type) bool {
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	_, ok = ptr.Elem().(*types.Pointer)
	return ok && isConnErr(ptr.Elem())
}

// typeSwitchSubject is the value a type switch switches on.
func typeSwitchSubject(x *ast.TypeSwitchStmt) ast.Expr {
	var e ast.Expr
	switch s := x.Assign.(type) {
	case *ast.ExprStmt:
		e = s.X
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 {
			e = s.Rhs[0]
		}
	}
	if ta, ok := e.(*ast.TypeAssertExpr); ok {
		return ta.X
	}
	return nil
}

// funcName names a declared function as a report prints it: pkg.Func, or
// pkg.Type.Method; "" outside any function.
func funcName(pkg *types.Package, fn *ast.FuncDecl) string {
	if fn == nil {
		return ""
	}
	name := fn.Name.Name
	if fn.Recv != nil && len(fn.Recv.List) == 1 {
		t := fn.Recv.List[0].Type
		for {
			switch x := t.(type) {
			case *ast.StarExpr:
				t = x.X
				continue
			case *ast.IndexExpr:
				t = x.X
				continue
			case *ast.IndexListExpr:
				t = x.X
				continue
			}
			break
		}
		if id, ok := t.(*ast.Ident); ok {
			name = id.Name + "." + name
		}
	}
	if pkg != nil {
		name = pkg.Name() + "." + name
	}
	return name
}

// wireAnalyzer decides whether an error a reader reads is provably one no
// package of this module built in process: nil, a fresh errors.New /
// fmt.Errorf (wrapping only such values) or connect.NewError the codemod
// leaves a *connect.Error (in a test, outside handlers/), or an error a Connect client read
// off the wire — directly, or returned by a function of this module whose
// every error result is one of those (followed a few calls deep). Such an
// error is a *connect.Error whatever a handler returns, so a read of it is
// not an in-process reader. Anything else — a parameter, an interface
// method's result, a call outside the module — is not provable, and the
// read counts.
type wireAnalyzer struct {
	funcs map[*types.Func]wireFunc
	fset  *token.FileSet // the load's, shared by every package
	rel   func(path string) string
}

type wireFunc struct {
	decl *ast.FuncDecl
	info *types.Info
}

// wireDepth bounds how many calls deep offWire follows a function's returns.
const wireDepth = 3

func newWireAnalyzer(pkgs []*packages.Package, rel func(string) string) *wireAnalyzer {
	w := &wireAnalyzer{funcs: map[*types.Func]wireFunc{}, rel: rel}
	for _, p := range pkgs {
		if w.fset == nil {
			w.fset = p.Fset
		}
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				if obj, ok := p.TypesInfo.Defs[fd.Name].(*types.Func); ok {
					w.funcs[obj] = wireFunc{fd, p.TypesInfo}
				}
			}
		}
	}
	return w
}

func (w *wireAnalyzer) offWire(info *types.Info, fn *ast.FuncDecl, e ast.Expr) bool {
	return w.expr(info, fn, e, 0, map[types.Object]bool{})
}

// expr is offWire for one expression. Every rule is a conjunction — all of a
// variable's assignments, all of a function's returns — so a cycle may
// assume true: whatever else feeds it still has to pass.
func (w *wireAnalyzer) expr(info *types.Info, fn *ast.FuncDecl, e ast.Expr, depth int, seen map[types.Object]bool) bool {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		switch obj := info.Uses[x].(type) {
		case *types.Nil:
			return true
		case *types.Var:
			if seen[obj] {
				return true
			}
			seen[obj] = true
			return w.variable(info, fn, obj, depth, seen)
		}
	case *ast.CallExpr:
		return w.call(info, fn, x, depth, seen)
	}
	return false
}

// variable: a local whose every assignment in fn is off the wire, and whose
// address is never taken.
func (w *wireAnalyzer) variable(info *types.Info, fn *ast.FuncDecl, v *types.Var, depth int, seen map[types.Object]bool) bool {
	if fn == nil || fn.Body == nil || v.Pos() < fn.Body.Pos() || v.Pos() >= fn.Body.End() {
		return false // a parameter, a result, a package-level variable: the value is someone else's
	}
	ok, assigned := true, false
	is := func(e ast.Expr) bool {
		id, isID := ast.Unparen(e).(*ast.Ident)
		return isID && (info.Uses[id] == v || info.Defs[id] == v)
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if !ok {
			return false
		}
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, l := range s.Lhs {
				if !is(l) {
					continue
				}
				assigned = true
				var rhs ast.Expr
				switch {
				case len(s.Rhs) == len(s.Lhs):
					rhs = s.Rhs[i]
				case len(s.Rhs) == 1:
					rhs = s.Rhs[0] // one call, many results
				}
				if rhs == nil || !w.expr(info, fn, rhs, depth, seen) {
					ok = false
				}
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if info.Defs[name] != v {
					continue
				}
				assigned = true
				switch {
				case len(s.Values) == 0: // the zero value, nil
				case len(s.Values) == len(s.Names):
					ok = ok && w.expr(info, fn, s.Values[i], depth, seen)
				default:
					ok = ok && w.expr(info, fn, s.Values[0], depth, seen)
				}
			}
		case *ast.UnaryExpr:
			if s.Op == token.AND && is(s.X) {
				ok = false // written through a pointer
			}
		case *ast.RangeStmt:
			if (s.Key != nil && is(s.Key)) || (s.Value != nil && is(s.Value)) {
				ok = false
			}
		}
		return true
	})
	return ok && assigned
}

func (w *wireAnalyzer) call(info *types.Info, fn *ast.FuncDecl, call *ast.CallExpr, depth int, seen map[types.Object]bool) bool {
	if isWireCall(info, call) {
		return true
	}
	switch pkg, name := funcOf(info, call); {
	case pkg == "errors" && name == "New":
		return true
	case pkg == connectPath && name == "NewError":
		// Built where it is read. The codemod never rewrites one in a test
		// (a fixture) and keeps one outside handlers/ a *connect.Error; in
		// a handler's code it may become a bare kind.
		rel := w.rel(w.fset.Position(call.Pos()).Filename)
		return strings.HasSuffix(rel, "_test.go") || !isHandlerFile(rel)
	case pkg == "fmt" && name == "Errorf":
		for _, a := range call.Args[1:] {
			if t := info.TypeOf(a); t != nil && types.Implements(t, errorIface) && !w.expr(info, fn, a, depth, seen) {
				return false
			}
		}
		return true
	}
	var callee *types.Func
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		callee, _ = info.Uses[f].(*types.Func)
	case *ast.SelectorExpr:
		callee, _ = info.Uses[f.Sel].(*types.Func)
	}
	if callee == nil {
		return false
	}
	callee = callee.Origin()
	src, ok := w.funcs[callee]
	if !ok || depth >= wireDepth {
		return false
	}
	if seen[callee] {
		return true
	}
	seen[callee] = true
	return w.returns(src, depth+1, seen)
}

// returns: every error a function returns is off the wire.
func (w *wireAnalyzer) returns(src wireFunc, depth int, seen map[types.Object]bool) bool {
	obj, _ := src.info.Defs[src.decl.Name].(*types.Func)
	if obj == nil {
		return false
	}
	results := obj.Type().(*types.Signature).Results()
	var errIdx []int
	for i := range results.Len() {
		if types.Implements(results.At(i).Type(), errorIface) {
			errIdx = append(errIdx, i)
		}
	}
	if len(errIdx) == 0 {
		return false
	}
	ok, any := true, false
	ast.Inspect(src.decl.Body, func(n ast.Node) bool {
		if !ok {
			return false
		}
		switch r := n.(type) {
		case *ast.FuncLit:
			return false // its returns are its own
		case *ast.ReturnStmt:
			any = true
			switch {
			case len(r.Results) == results.Len():
				for _, i := range errIdx {
					ok = ok && w.expr(src.info, src.decl, r.Results[i], depth, seen)
				}
			case len(r.Results) == 1:
				ok = w.expr(src.info, src.decl, r.Results[0], depth, seen) // return f()
			default:
				ok = false // a bare return of named results
			}
		}
		return true
	})
	return ok && any
}

// isWireCall reports whether call is a Connect client's: a method of
// connect's client-side types (*connect.Client, the *ForClient streams) or of
// a generated <svc>connect package's client (the <Svc>Client interface and
// the struct implementing it). Its error came off the wire.
func isWireCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	s := info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal {
		return false
	}
	t := s.Recv()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	pkg, name := named.Obj().Pkg(), named.Obj().Name()
	if pkg.Path() == connectPath {
		switch name {
		case "Client", "ServerStreamForClient", "ClientStreamForClient", "BidiStreamForClient":
			return true
		}
		return false
	}
	if !strings.HasSuffix(pkg.Name(), "connect") || !strings.HasSuffix(strings.ToLower(name), "client") {
		return false
	}
	for _, imp := range pkg.Imports() {
		if imp.Path() == connectPath {
			return true
		}
	}
	return false
}
