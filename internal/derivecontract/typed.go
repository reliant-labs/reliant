package derivecontract

import (
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/contractsrc"
	"github.com/reliant-labs/forge/internal/generator/contract"
	"github.com/reliant-labs/forge/internal/goexec"
)

// The migration asks the type checker, not the syntax, every question whose
// answer is a type: which type implements Service (through T's method set or
// *T's), whether a method's signature is Service's (an alias, an import
// named two ways, a type declared in another file), and whether the
// rewritten package's derived Service is the one it replaces.

// loadMode type-checks the package from source, so an overlay of the
// rewritten files is what gets checked.
const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
	packages.NeedImports | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo

// maxListedErrors bounds the type errors a refusal quotes.
const maxListedErrors = 5

// verifyFileName and verifyIfaceName are the overlay-only file that
// re-declares the original Service inside the rewritten package, so the two
// interfaces are compared in one type universe.
const (
	verifyFileName  = "zz_forge_derive_verify.go"
	verifyIfaceName = "forgeDeriveOriginal"
)

// loadPackage type-checks the package in dir — with overlay standing in for
// files on disk when it is non-nil — and returns its errors, root-relative,
// as the reasons it cannot be used.
func loadPackage(root, dir string, overlay map[string][]byte) (*packages.Package, []string, error) {
	cfg := &packages.Config{Mode: loadMode, Dir: dir, Env: goexec.Env(), Overlay: overlay}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("type-check %s: %w", dir, err)
	}
	if len(pkgs) != 1 {
		return nil, []string{fmt.Sprintf("the Go toolchain found %d packages in the directory, want exactly one", len(pkgs))}, nil
	}
	pkg := pkgs[0]
	// `go list` compiles the package too, and reports the compiler's view of
	// a type error as a list error beside the type checker's own: quote the
	// type checker, and the list errors only when it found nothing.
	checked := false
	for _, e := range pkg.Errors {
		if e.Kind == packages.TypeError || e.Kind == packages.ParseError {
			checked = true
		}
	}
	var errs []string
	for _, e := range pkg.Errors {
		if checked && e.Kind == packages.ListError {
			continue
		}
		msg := e.Error()
		for _, prefix := range []string{root + string(filepath.Separator), dir + string(filepath.Separator)} {
			msg = strings.ReplaceAll(msg, prefix, "")
		}
		errs = append(errs, msg)
	}
	if len(errs) == 0 && (pkg.Types == nil || pkg.TypesInfo == nil) {
		errs = append(errs, "the Go toolchain returned no type information for it")
	}
	return pkg, errs, nil
}

// typeErrorReason is the refusal for a package that does not type-check.
func typeErrorReason(what string, errs []string) string {
	listed := errs
	more := ""
	if len(listed) > maxListedErrors {
		listed = listed[:maxListedErrors]
		more = fmt.Sprintf(" (and %d more)", len(errs)-maxListedErrors)
	}
	return fmt.Sprintf("%s does not type-check, so the migration cannot prove it keeps the contract — fix the build first: %s%s", what, strings.Join(listed, "; "), more)
}

// excludedSources are the package's source files the type-checked build left
// out (build constraints). The derived contract reads every source file, so
// a build the migration checked would not be the only one it changed.
func excludedSources(p *pkgSrc, tp *packages.Package) []string {
	inBuild := map[string]bool{}
	for _, f := range append(append([]string(nil), tp.GoFiles...), tp.CompiledGoFiles...) {
		inBuild[filepath.Base(f)] = true
	}
	var out []string
	for _, f := range p.files {
		if !inBuild[f.name] {
			out = append(out, f.name)
		}
	}
	return out
}

// position is an object's "file.go:line", by base name.
func position(tp *packages.Package, obj types.Object) (string, int) {
	pos := tp.Fset.Position(obj.Pos())
	return filepath.Base(pos.Filename), pos.Line
}

// typedService is contract.go's Service as the type checker sees it.
func typedService(tp *packages.Package) (*types.TypeName, *types.Interface, []string) {
	tn, ok := tp.Types.Scope().Lookup(contractsrc.InterfaceName).(*types.TypeName)
	if !ok {
		return nil, nil, []string{"the type checker finds no Service in the package"}
	}
	iface, ok := tn.Type().Underlying().(*types.Interface)
	if !ok {
		return nil, nil, []string{"Service is not an interface type"}
	}
	if f, _ := position(tp, tn); f != contractsrc.FileName {
		return nil, nil, []string{fmt.Sprintf("the type checker's Service is declared in %s, not contract.go", f)}
	}
	return tn, iface, nil
}

// candidate is a named type of the package that implements Service.
type candidate struct {
	obj   *types.TypeName
	named *types.Named
	// pointerOnly: T's method set does not implement Service, *T's does.
	pointerOnly bool
	// declared are the Service methods declared on T itself (the rest are
	// promoted from embedded fields).
	declared int
}

// declaredMethods are the methods declared on named, by name: what the
// derived contract reads. Promoted methods are not among them.
func declaredMethods(named *types.Named) map[string]*types.Func {
	out := map[string]*types.Func{}
	for i := 0; i < named.NumMethods(); i++ {
		m := named.Method(i)
		out[m.Name()] = m
	}
	return out
}

// methodOf is the method name of T's (or *T's) full method set, promoted
// ones included, or nil.
func methodOf(tp *packages.Package, named *types.Named, name string) *types.Func {
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(named), false, tp.Types, name)
	fn, _ := obj.(*types.Func)
	return fn
}

// receiverNamed is the named type a method is declared on.
func receiverNamed(fn *types.Func) *types.Named {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return nil
	}
	t := sig.Recv().Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, _ := t.(*types.Named)
	if named != nil {
		named = named.Origin()
	}
	return named
}

// findImplementation returns the one type of the package that implements
// Service — through T's method set or *T's — and whether only *T does. It
// refuses every shape whose derived contract would not be Service: no
// implementation or several, one that is generic, one that gets a Service
// method only by promotion from an embedded field or declares it through an
// alias receiver, and one that exports a method Service does not have.
//
// A type that implements Service only by promoting every method from the
// one declaring implementation (`type wrapper struct{ *service }`) is that
// implementation reached another way, not a second one.
//
//nolint:funlen,gocognit // One classification: candidates, near misses, then the winner's refusals.
func findImplementation(p *pkgSrc, tp *packages.Package, svc *serviceDecl) (*typeDecl, bool, []string) {
	svcObj, iface, reasons := typedService(tp)
	if len(reasons) > 0 {
		return nil, false, reasons
	}
	decls := p.typeDecls()
	sources := map[string]bool{}
	for _, f := range p.files {
		sources[f.name] = true
	}

	var cands, generic []*candidate
	var near []string
	scope := tp.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() || tn == svcObj {
			continue
		}
		if file, _ := position(tp, tn); !sources[file] {
			continue // generated (a mock) or not this package's source
		}
		// Interfaces are contracts, not implementations: a package-local
		// interface that Service's method set satisfies (a narrower role,
		// or one embedding Service) is not a second implementation.
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		if _, isIface := named.Underlying().(*types.Interface); isIface {
			continue
		}
		declared := declaredMethods(named)
		if named.TypeParams().Len() > 0 {
			all := true
			for _, im := range svc.methods {
				if declared[im.name] == nil {
					all = false
				}
			}
			if all {
				generic = append(generic, &candidate{obj: tn, named: named})
			}
			continue
		}
		valueOK := types.Implements(named, iface)
		if !valueOK && !types.Implements(types.NewPointer(named), iface) {
			near = append(near, nearMiss(tp, decls[name], named, svc, iface)...)
			continue
		}
		c := &candidate{obj: tn, named: named, pointerOnly: !valueOK}
		for _, im := range svc.methods {
			if declared[im.name] != nil {
				c.declared++
			}
		}
		cands = append(cands, c)
	}

	var declaring []*candidate
	for _, c := range cands {
		if c.declared > 0 {
			declaring = append(declaring, c)
		}
	}
	var impls []*candidate
	for _, c := range cands {
		if c.declared == 0 && len(declaring) == 1 && wraps(tp, c, declaring[0], svc) {
			continue
		}
		impls = append(impls, c)
	}

	where := func(c *candidate) string {
		f, l := position(tp, c.obj)
		return fmt.Sprintf("%s (%s:%d)", c.obj.Name(), f, l)
	}
	switch {
	case len(impls) == 0 && len(generic) == 1:
		f, l := position(tp, generic[0].obj)
		return nil, false, []string{fmt.Sprintf("its implementation %s (%s:%d) is generic, and a generic type cannot implement the non-generic Service by itself", generic[0].obj.Name(), f, l)}
	case len(impls) == 0 && len(generic) == 0:
		sort.Strings(near)
		return nil, false, append([]string{"no type in the package implements Service (through its own method set or its pointer's)"}, near...)
	case len(impls)+len(generic) > 1:
		all := append(append([]*candidate(nil), impls...), generic...)
		sort.Slice(all, func(i, j int) bool { return all[i].obj.Name() < all[j].obj.Name() })
		list := make([]string, 0, len(all))
		for _, c := range all {
			list = append(list, where(c))
		}
		return nil, false, []string{fmt.Sprintf("%d types implement Service — %s — and a derived contract has one source; with several implementations the interface IS the abstraction, so keep it hand-written", len(all), strings.Join(list, ", "))}
	}

	c := impls[0]
	t := decls[c.obj.Name()]
	if t == nil {
		f, l := position(tp, c.obj)
		return nil, false, []string{fmt.Sprintf("its implementation %s (%s:%d) is not a type declaration the migration can mark", c.obj.Name(), f, l)}
	}
	declared := declaredMethods(c.named)

	// Promoted Service methods: the derived contract would lose them.
	byField := map[string][]string{}
	var fields []string
	for _, im := range svc.methods {
		if declared[im.name] != nil {
			continue
		}
		field := "an embedded field"
		if _, index, _ := types.LookupFieldOrMethod(types.NewPointer(c.named), false, tp.Types, im.name); len(index) > 1 {
			if st, ok := c.named.Underlying().(*types.Struct); ok && index[0] < st.NumFields() {
				field = "its embedded field " + st.Field(index[0]).Name()
			}
		}
		if _, seen := byField[field]; !seen {
			fields = append(fields, field)
		}
		byField[field] = append(byField[field], im.name)
	}
	for _, field := range fields {
		reasons = append(reasons, fmt.Sprintf("%s gets Service's %s only through %s, and the derived contract lists the methods %s declares itself, so it would lose them: declare them on %s (forwarding to the field), or keep the hand-written contract", c.obj.Name(), strings.Join(byField[field], ", "), field, c.obj.Name(), c.obj.Name()))
	}

	// The derived contract reads the exported methods declared, in the
	// package's source files, with the marked type's own name as receiver:
	// a Service method declared in a generated file, or through an alias
	// receiver, would be lost; an extra exported one would be gained.
	var viaAlias, inGenerated, extra []string
	inService := map[string]bool{}
	for _, im := range svc.methods {
		inService[im.name] = true
	}
	for name, fn := range declared {
		if !fn.Exported() {
			continue
		}
		f, l := position(tp, fn)
		at := fmt.Sprintf("%s (%s:%d)", name, f, l)
		switch {
		case !sources[f]:
			if inService[name] {
				inGenerated = append(inGenerated, at)
			}
		case !inService[name]:
			extra = append(extra, at)
		}
		if _, ok := t.methods[name]; !ok && sources[f] && inService[name] {
			viaAlias = append(viaAlias, at)
		}
	}
	if len(inGenerated) > 0 {
		sort.Strings(inGenerated)
		reasons = append(reasons, fmt.Sprintf("%s declares %s in a generated file, which the derived contract does not read, so it would lose them; move them into a source file first", c.obj.Name(), strings.Join(inGenerated, ", ")))
	}
	if len(viaAlias) > 0 {
		sort.Strings(viaAlias)
		reasons = append(reasons, fmt.Sprintf("%s declares %s through an alias receiver, and the derived contract reads the methods declared with the receiver %s; spell the receiver %s first", c.obj.Name(), strings.Join(viaAlias, ", "), c.obj.Name(), c.obj.Name()))
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		reasons = append(reasons, fmt.Sprintf("%s exports methods Service does not have — %s — and the derived contract would grow them; unexport them, or move them off %s, first", c.obj.Name(), strings.Join(extra, ", "), c.obj.Name()))
	}
	return t, c.pointerOnly, reasons
}

// wraps reports whether every Service method of c IS impl's — c promotes
// them all from impl, so it is impl reached through an embedded field.
func wraps(tp *packages.Package, c, impl *candidate, svc *serviceDecl) bool {
	for _, im := range svc.methods {
		fn := methodOf(tp, c.named, im.name)
		if fn == nil || receiverNamed(fn) != impl.named {
			return false
		}
	}
	return true
}

// nearMiss explains a type that has every Service method by name but does
// not implement Service: each method whose signature differs. A type missing
// a method by name is not a near miss.
func nearMiss(tp *packages.Package, t *typeDecl, named *types.Named, svc *serviceDecl, iface *types.Interface) []string {
	var out []string
	qual := types.RelativeTo(tp.Types)
	for _, im := range svc.methods {
		fn := methodOf(tp, named, im.name)
		if fn == nil {
			return nil
		}
		want := ifaceMethod(iface, im.name)
		if want == nil || types.Identical(fn.Type(), want.Type()) {
			continue
		}
		got, have := strings.TrimPrefix(types.TypeString(fn.Type(), qual), "func"), im.display
		if t != nil {
			if md, ok := t.methods[im.name]; ok {
				got = strings.TrimPrefix(render(md.fd.Type), "func")
			}
		}
		if got == have {
			// The same text naming different types: spell out the packages.
			got = strings.TrimPrefix(types.TypeString(fn.Type(), qual), "func")
			have = strings.TrimPrefix(types.TypeString(want.Type(), qual), "func")
			if got == have {
				got = strings.TrimPrefix(types.TypeString(fn.Type(), nil), "func")
				have = strings.TrimPrefix(types.TypeString(want.Type(), nil), "func")
			}
		}
		out = append(out, fmt.Sprintf("%s.%s is %s%s, Service has %s%s", named.Obj().Name(), im.name, im.name, got, im.name, have))
	}
	return out
}

// ifaceMethod is iface's method name, or nil.
func ifaceMethod(iface *types.Interface, name string) *types.Func {
	for i := 0; i < iface.NumMethods(); i++ {
		if m := iface.Method(i); m.Name() == name {
			return m
		}
	}
	return nil
}

// typeDecls are the package's non-interface type declarations, by name, each
// with the methods declared on it by receiver name.
func (p *pkgSrc) typeDecls() map[string]*typeDecl {
	byName := map[string]*typeDecl{}
	for _, f := range p.files {
		for _, ts := range typeSpecs(f) {
			byName[ts.ts.Name.Name] = &typeDecl{name: ts.ts.Name.Name, file: f, gd: ts.gd, ts: ts.ts, methods: map[string]methodDecl{}}
		}
	}
	for _, f := range p.files {
		for _, fd := range methodDecls(f) {
			if t := byName[receiverTypeName(fd.Recv.List[0].Type)]; t != nil {
				t.methods[fd.Name.Name] = methodDecl{fd: fd, file: f}
			}
		}
	}
	return byName
}

// verify proves the rewrite keeps the contract before anything is written.
// It renders contract_gen.go from the rewritten package (in a scratch copy,
// since the deriver reads files on disk), then type-checks the rewritten
// package as an overlay of the real directory: every planned write, every
// removed file emptied to its package clause, the rendered contract_gen.go —
// whose `var _ Service = …` assertion proves the marked type still
// implements it — and one more file re-declaring the ORIGINAL Service under
// a private name. Both interfaces are then in one type universe, where
// types.Identical is meaningful, and must match method for method.
func verify(p *pkgSrc, orig *packages.Package, origIface *types.Interface, outputs map[string][]byte) ([]string, error) {
	rendered, reason, err := renderDerived(p, outputs)
	if err != nil || reason != "" {
		return nonEmpty(reason), err
	}
	verifyPath := filepath.Join(p.dir, verifyFileName)
	if _, err := os.Lstat(verifyPath); err == nil {
		return nil, fmt.Errorf("%s exists; it is the name the migration's check uses, so remove or rename it", verifyPath)
	}
	overlay := map[string][]byte{}
	for name, content := range outputs {
		path := filepath.Join(p.dir, name)
		if content == nil {
			content = []byte("package " + p.name + "\n")
		}
		overlay[path] = content
	}
	overlay[filepath.Join(p.dir, contractsrc.DerivedFileName)] = rendered
	overlay[verifyPath] = originalInterfaceFile(orig.Types, p.name, origIface)

	np, errs, err := loadPackage(p.root, p.dir, overlay)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return []string{typeErrorReason("the rewritten package, with the contract_gen.go forge would derive,", errs)}, nil
	}
	derivedObj, ok := np.Types.Scope().Lookup(contractsrc.InterfaceName).(*types.TypeName)
	if !ok {
		return []string{"the rewritten package declares no Service"}, nil
	}
	if f, _ := position(np, derivedObj); f != contractsrc.DerivedFileName {
		return []string{fmt.Sprintf("the rewritten package's Service is declared in %s, not the derived %s", f, contractsrc.DerivedFileName)}, nil
	}
	origObj, ok := np.Types.Scope().Lookup(verifyIfaceName).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("the check's re-declared Service is missing from the overlay load")
	}
	derived, ok1 := derivedObj.Type().Underlying().(*types.Interface)
	original, ok2 := origObj.Type().Underlying().(*types.Interface)
	if !ok1 || !ok2 {
		return []string{"the derived Service is not an interface"}, nil
	}

	qual := types.RelativeTo(np.Types)
	var reasons []string
	for i := 0; i < original.NumMethods(); i++ {
		om := original.Method(i)
		dm := ifaceMethod(derived, om.Name())
		switch {
		case dm == nil:
			reasons = append(reasons, fmt.Sprintf("the derived contract would lose Service.%s", om.Name()))
		case !types.Identical(om.Type(), dm.Type()):
			reasons = append(reasons, fmt.Sprintf("the derived contract would change Service.%s from %s to %s", om.Name(),
				strings.TrimPrefix(types.TypeString(om.Type(), qual), "func"), strings.TrimPrefix(types.TypeString(dm.Type(), qual), "func")))
		}
	}
	for i := 0; i < derived.NumMethods(); i++ {
		if dm := derived.Method(i); ifaceMethod(original, dm.Name()) == nil {
			reasons = append(reasons, fmt.Sprintf("the derived contract would gain %s", dm.Name()))
		}
	}
	if len(reasons) == 0 && (derived.NumMethods() != original.NumMethods() || !types.Identical(derived, original)) {
		reasons = append(reasons, "the derived Service is not identical to the one it replaces")
	}
	sort.Strings(reasons)
	return reasons, nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// renderDerived renders contract_gen.go from the rewritten package, in a
// scratch copy of its source files. A refusal from the deriver comes back as
// the reason.
func renderDerived(p *pkgSrc, outputs map[string][]byte) ([]byte, string, error) {
	tmp, err := os.MkdirTemp("", "forge-derive-contract-*")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	files := map[string][]byte{}
	for _, f := range p.files {
		files[f.name] = f.src
	}
	for name, content := range outputs {
		if content == nil {
			delete(files, name)
			continue
		}
		files[name] = content
	}
	for name, content := range files {
		if err := atomicfile.WriteFile(filepath.Join(tmp, name), content, 0o644); err != nil {
			return nil, "", err
		}
	}
	var r *contract.Resolver
	if p.nonStd {
		r = contract.NewResolver(filepath.Join(p.dir, contractsrc.FileName))
	}
	rendered, err := contract.RenderDerivedContract(tmp, r)
	if err != nil {
		return nil, "the derived contract cannot be generated from the rewritten package: " + strings.ReplaceAll(err.Error(), tmp, p.dir), nil
	}
	return rendered, "", nil
}

// originalInterfaceFile is Go source, in package name, re-declaring iface
// (from the original load) as verifyIfaceName. Every other package's types
// are qualified through an alias of its own, so no identifier of the
// package can shadow one.
func originalInterfaceFile(self *types.Package, name string, iface *types.Interface) []byte {
	aliases := map[string]string{}
	var paths []string
	qual := func(other *types.Package) string {
		if other.Path() == self.Path() {
			return ""
		}
		a, ok := aliases[other.Path()]
		if !ok {
			a = fmt.Sprintf("_fdv%d", len(paths))
			aliases[other.Path()] = a
			paths = append(paths, other.Path())
		}
		return a
	}
	var body strings.Builder
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		sig, _ := m.Type().(*types.Signature)
		fmt.Fprintf(&body, "\t%s%s\n", m.Name(), signatureString(sig, qual))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", name)
	if len(paths) > 0 {
		b.WriteString("import (\n")
		for _, path := range paths {
			fmt.Fprintf(&b, "\t%s %q\n", aliases[path], path)
		}
		b.WriteString(")\n\n")
	}
	fmt.Fprintf(&b, "type %s interface {\n%s}\n", verifyIfaceName, body.String())
	return []byte(b.String())
}

// signatureString is sig's parameter and result types without their names
// (a parameter named like an alias cannot capture it): "(T1, ...T2) (R1)".
func signatureString(sig *types.Signature, q types.Qualifier) string {
	tuple := func(t *types.Tuple, variadic bool) string {
		parts := make([]string, 0, t.Len())
		for i := 0; i < t.Len(); i++ {
			typ := t.At(i).Type()
			if variadic && i == t.Len()-1 {
				if s, ok := typ.(*types.Slice); ok {
					parts = append(parts, "..."+types.TypeString(s.Elem(), q))
					continue
				}
			}
			parts = append(parts, types.TypeString(typ, q))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	return tuple(sig.Params(), sig.Variadic()) + " " + tuple(sig.Results(), false)
}
