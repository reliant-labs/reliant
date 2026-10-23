package depsurface

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
)

// DepsFileName is where Narrow declares the interfaces of a package that has
// no contract.go; DepsFileMarker is its first line, which `forge generate`
// reads to keep the file's mocks (mock_gen.go) in step with it.
const (
	DepsFileName   = "deps.go"
	DepsFileMarker = "// forge:deps — consumer-declared interfaces for this package's Deps."
)

// NarrowResult is what Narrow did to one package.
type NarrowResult struct {
	Dir string
	// File is the project-relative file the interfaces were written to:
	// the package's contract.go, or deps.go.
	File     string
	Narrowed []Narrowed
	Skipped  []Skipped
	// Bloated are the declared interfaces wider than the project's
	// interfacebloat limit.
	Bloated []Bloated
}

// Narrowed is one field Narrow retyped.
type Narrowed struct {
	Field, From, To string
	// Methods are every method the new interface holds; Embeds are the
	// interfaces it embeds for the ones the package passes the value on as.
	Methods []string
	Embeds  []string
}

// Skipped is one interface field Narrow left alone, and why.
type Skipped struct {
	Field, Type, Reason string
}

// Narrow rewrites every foreign interface field of dir's Deps that declares
// methods the package never needs to an interface the package declares
// itself, holding exactly the methods it needs (design §2, K1): those it
// calls, with each interface it passes the value on as (Field.Roles)
// embedded. The composition still fills the field with the same value: Go
// satisfies the narrower interface structurally, so compose.go needs no
// edit, and every call that passes the value on still type-checks.
//
// A field whose value also leaves through something that bounds nothing
// (Escapes: `any`, a concrete type, a return value) is left alone — what it
// needs is not all visible here — as is a field the package never uses at
// all (delete it instead), and an interface the package already declares
// (edit it in place).
func Narrow(projectDir, dir string) (*NarrowResult, error) {
	pkgs, err := Analyze(projectDir, []string{dir})
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("%s: no component package with a Deps struct that type-checks (run `go build ./%s` to see why)", dir, dir)
	}
	p := pkgs[0]
	res := &NarrowResult{Dir: p.Dir}
	target := filepath.Join(projectDir, filepath.FromSlash(p.Dir), "contract.go")
	if _, err := os.Stat(target); err != nil {
		target = filepath.Join(projectDir, filepath.FromSlash(p.Dir), DepsFileName)
	}
	res.File, _ = filepath.Rel(projectDir, target)
	res.File = filepath.ToSlash(res.File)

	cfg := readLintConfig(projectDir)
	prefix := cfg.importPrefix(checksums.GoImportsLocalPrefix(projectDir))
	w := newIfaceWriter(p)
	w.lint = cfg
	for path, name := range p.importNames(res.File) {
		w.imports[path], w.names[name] = name, path
	}
	retype := map[string]string{} // field → new type name
	for _, f := range p.Fields {
		switch {
		case !f.Interface:
			continue
		case !f.Foreign:
			if len(Unused(f)) > 0 {
				res.Skipped = append(res.Skipped, Skipped{f.Name, f.Type, "the package declares this interface itself: delete the methods it does not call there"})
			}
			continue
		case len(Unused(f)) == 0:
			continue
		case f.Downcast:
			res.Skipped = append(res.Skipped, Skipped{f.Name, f.Type, "the package type-asserts it to a concrete type (forgeconv-deps-no-downcast): declare what it needs from that type first"})
			continue
		case f.Escapes:
			res.Skipped = append(res.Skipped, Skipped{f.Name, f.Type, "its value is also passed on as `any` or a concrete type, or returned (" + f.EscapeAt + "), so what this package uses is not everything it needs"})
			continue
		case len(f.Used) == 0:
			res.Skipped = append(res.Skipped, Skipped{f.Name, f.Type, "the package never calls it or passes it on: delete the field"})
			continue
		}
		name, entries, err := w.declare(f)
		if err != nil {
			return nil, err
		}
		retype[f.Name] = name
		n := Narrowed{Field: f.Name, From: f.Type, To: name, Methods: sortedUsed(f)}
		embeds, _ := splitRoles(f)
		for _, r := range embeds {
			n.Embeds = append(n.Embeds, r.Type)
		}
		res.Narrowed = append(res.Narrowed, n)
		if cfg.overBloat(entries) {
			res.Bloated = append(res.Bloated, Bloated{name, entries, cfg.interfaceBloatMax, cfg.limitSource(), cfg.suppressesBloat(entries)})
		}
	}
	if len(retype) == 0 {
		return res, nil
	}
	// Render both files before writing either, so a failure leaves the
	// package as it was.
	// When Deps lives in the target itself (a contract package's
	// contract.go), the declarations are appended to the retyped file.
	depsFile := filepath.Join(projectDir, filepath.FromSlash(p.DepsFile))
	retyped, err := retypeFields(depsFile, retype, p.importNames(p.DepsFile), prefix)
	if err != nil {
		return nil, err
	}
	var base []byte
	if depsFile == target {
		base = retyped
	}
	decls, err := w.render(target, base, prefix)
	if err != nil {
		return nil, err
	}
	if depsFile != target {
		if err := atomicfile.WriteFile(depsFile, retyped, 0o644); err != nil {
			return nil, err
		}
	}
	if err := atomicfile.WriteFile(target, decls, 0o644); err != nil {
		return nil, err
	}
	return res, nil
}

func sortedUsed(f Field) []string {
	out := make([]string, 0, len(f.Used))
	for m := range f.Used {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// ifaceWriter renders interface declarations in the consumer's package,
// collecting the imports their signatures need.
type ifaceWriter struct {
	p       *Package
	taken   map[string]bool   // identifiers in use in the package scope
	imports map[string]string // import path → name used
	names   map[string]string // name used → import path
	added   map[string]bool   // imports the declarations introduce
	lint    lintConfig        // the project's golangci-lint settings
	decls   bytes.Buffer
}

func newIfaceWriter(p *Package) *ifaceWriter {
	w := &ifaceWriter{p: p, taken: map[string]bool{}, imports: map[string]string{}, names: map[string]string{}, added: map[string]bool{}}
	for _, n := range p.pkg.Types.Scope().Names() {
		w.taken[n] = true
	}
	return w
}

// qualifier names a package in the consumer's file, choosing a unique import
// name for it.
func (w *ifaceWriter) qualifier(pkg *types.Package) string {
	if pkg == w.p.pkg.Types {
		return ""
	}
	if n, ok := w.imports[pkg.Path()]; ok {
		return n
	}
	name := pkg.Name()
	for i := 2; w.names[name] != "" || w.taken[name]; i++ {
		name = pkg.Name() + strconv.Itoa(i)
	}
	w.imports[pkg.Path()], w.names[name] = name, pkg.Path()
	w.added[pkg.Path()] = true
	return name
}

// ifaceName picks the name of the interface narrowed from a field typed
// typeName: the type's own name when the package does not use it, else the
// field's name, else the two joined. The field's name comes before the join
// because the usual collision is a handler's own Service — svcdaemon.Service
// on Deps.DaemonService is DaemonService, not DaemonServiceService — and a
// Deps field and a package-scope type never collide (`DaemonService
// DaemonService` is a legal field).
func ifaceName(typeName, fieldName string, taken map[string]bool) (string, bool) {
	for _, n := range []string{typeName, fieldName, fieldName + typeName} {
		if !taken[n] {
			return n, true
		}
	}
	return "", false
}

// declare renders the narrowed interface for f and returns its name and its
// entry count as interfacebloat counts them (each written-out method and each
// embedded interface).
//
// An interface wider than the project's interfacebloat limit is written with
// a reasoned //nolint:interfacebloat, never split to fit. The lint counts an
// embedded interface as ONE entry, so splitting the methods into embedded
// roles would pass the counter while the dependency stayed exactly as wide —
// hiding the very signal the lint exists to give. And the only principled
// roles are the ones the value is passed on as, which are already embedded;
// grouping the rest by the function that calls them turns a handler with one
// shim per rpc into one single-method role per rpc. What narrow-deps writes
// is, by construction, exactly what the package calls: its width is the
// package's real dependency, and shrinking it means splitting the package,
// which is a design decision the codemod cannot make. So it says so, at the
// type, and the warning names the remedy.
func (w *ifaceWriter) declare(f Field) (string, int, error) {
	named, ok := types.Unalias(f.typ).(*types.Named)
	if !ok {
		return "", 0, fmt.Errorf("field Deps.%s: %s is not a named interface", f.Name, f.Type)
	}
	iface := named.Underlying().(*types.Interface)
	name, ok := ifaceName(named.Obj().Name(), f.Name, w.taken)
	if !ok {
		return "", 0, fmt.Errorf("field Deps.%s: %s, %s and %s%s are all taken in package %s — declare the interface by hand", f.Name, named.Obj().Name(), f.Name, f.Name, named.Obj().Name(), w.p.pkg.Name)
	}
	w.taken[name] = true
	embeds, direct := splitRoles(f)
	fmt.Fprintf(&w.decls, "\n// %s is the part of %s.%s that this package uses through Deps.%s:\n", name, named.Obj().Pkg().Name(), named.Obj().Name(), f.Name)
	fmt.Fprintf(&w.decls, "// declared here, by its consumer, so the package knows only what it uses\n// (forge fix narrow-deps). Add a method when the package calls one.\n")
	if len(embeds) > 0 {
		fmt.Fprintf(&w.decls, "// The embedded interfaces are what the package passes the value on as.\n")
	}
	entries := len(embeds) + len(direct)
	if w.lint.suppressesBloat(entries) {
		fmt.Fprintf(&w.decls, "//\n//nolint:interfacebloat // forge fix narrow-deps: exactly the %d of %s.%s's %d methods this package calls through Deps.%s, one dependency; shrink it by splitting the package, not the interface.\n",
			entries, named.Obj().Pkg().Name(), named.Obj().Name(), iface.NumMethods(), f.Name)
	}
	fmt.Fprintf(&w.decls, "type %s interface {\n", name)
	for _, r := range embeds {
		fmt.Fprintf(&w.decls, "\t%s\n", types.TypeString(r.typ, w.qualifier))
	}
	for _, m := range direct {
		obj, _, _ := types.LookupFieldOrMethod(iface, false, nil, m)
		fn, ok := obj.(*types.Func)
		if !ok {
			return "", 0, fmt.Errorf("field Deps.%s: %s has no method %s", f.Name, f.Type, m)
		}
		var sig bytes.Buffer
		types.WriteSignature(&sig, fn.Type().(*types.Signature), w.qualifier)
		fmt.Fprintf(&w.decls, "\t%s%s\n", m, sig.String())
	}
	w.decls.WriteString("}\n")
	return name, entries, nil
}

// splitRoles divides what f's narrowed interface holds: the Roles it embeds
// (each nameable one that adds a method the larger ones before it do not
// cover) and the used methods no embedded role covers, written out.
func splitRoles(f Field) (embeds []Role, direct []string) {
	roles := make([]Role, 0, len(f.Roles))
	for _, r := range f.Roles {
		if r.Embeddable {
			roles = append(roles, r)
		}
	}
	sort.SliceStable(roles, func(i, j int) bool {
		if len(roles[i].Methods) != len(roles[j].Methods) {
			return len(roles[i].Methods) > len(roles[j].Methods)
		}
		return roles[i].Type < roles[j].Type
	})
	covered := map[string]bool{}
	for _, r := range roles {
		adds := false
		for _, m := range r.Methods {
			adds = adds || !covered[m]
		}
		if !adds {
			continue
		}
		embeds = append(embeds, r)
		for _, m := range r.Methods {
			covered[m] = true
		}
	}
	sort.Slice(embeds, func(i, j int) bool { return embeds[i].Type < embeds[j].Type })
	for _, m := range sortedUsed(f) {
		if !covered[m] {
			direct = append(direct, m)
		}
	}
	return embeds, direct
}

// render returns target (or base, its pending content) with the
// declarations appended — deps.go, fresh, when absent — and the imports they
// need.
func (w *ifaceWriter) render(target string, base []byte, prefix string) ([]byte, error) {
	src, err := base, error(nil)
	if src == nil {
		src, err = os.ReadFile(target)
	}
	if os.IsNotExist(err) {
		src = []byte(DepsFileMarker + "\n// The interfaces below are this package's own declarations of what it\n// needs from its Deps; mock_gen.go mocks them.\npackage " + w.p.pkg.Name + "\n")
	} else if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, target, append(append([]byte{}, src...), w.decls.Bytes()...), parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("narrow-deps rendered an unparseable %s (a forge bug — please report it): %w", target, err)
	}
	added := map[string]string{}
	for p := range w.added {
		added[p] = w.imports[p]
	}
	addImports(fset, file, added)
	return formatFile(fset, file, target, prefix)
}

// addImports adds each path under its name, omitting the name when it is
// the path's own.
func addImports(fset *token.FileSet, file *ast.File, imports map[string]string) {
	for p, name := range imports {
		if name == path.Base(p) {
			astutil.AddImport(fset, file, p)
		} else {
			astutil.AddNamedImport(fset, file, name, p)
		}
	}
}

// formatFile prints file as goimports -local prefix would leave it: stdlib,
// third-party, then the local group, so the project's goimports lint accepts
// what narrow-deps wrote.
func formatFile(fset *token.FileSet, file *ast.File, name, prefix string) ([]byte, error) {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, fmt.Errorf("format %s: %w", name, err)
	}
	out, err := checksums.CanonicalGoSource(prefix, name, buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format %s: %w", name, err)
	}
	return out, nil
}

// importNames maps each import of the project-relative file to its name in
// that file, read off the type-checker (a package's name need not be its
// path's last element).
func (p *Package) importNames(rel string) map[string]string {
	out := map[string]string{}
	for _, f := range p.pkg.Syntax {
		if !strings.HasSuffix(filepath.ToSlash(p.pkg.Fset.Position(f.Pos()).Filename), "/"+rel) {
			continue
		}
		for _, spec := range f.Imports {
			if pn := p.pkg.TypesInfo.PkgNameOf(spec); pn != nil {
				out[pn.Imported().Path()] = pn.Name()
			}
		}
	}
	return out
}

// retypeFields returns file with the type of each named Deps field
// rewritten, minus every import (names: path → name in file) nothing uses any
// more.
func retypeFields(file string, retype map[string]string, names map[string]string, prefix string) ([]byte, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Deps" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			if len(field.Names) != 1 {
				continue
			}
			if to, ok := retype[field.Names[0].Name]; ok {
				edits = append(edits, edit{fset.Position(field.Type.Pos()).Offset, fset.Position(field.Type.End()).Offset, to})
			}
		}
		return false
	})
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		src = append(src[:e.start], append([]byte(e.text), src[e.end:]...)...)
	}
	fset = token.NewFileSet()
	f, err = parser.ParseFile(fset, file, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	type unused struct{ name, path string }
	var drop []unused
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name, known := names[p]
		if !known || name == "_" || name == "." || usesPackageName(f, name) {
			continue
		}
		explicit := ""
		if imp.Name != nil {
			explicit = imp.Name.Name
		}
		drop = append(drop, unused{explicit, p})
	}
	for _, d := range drop {
		astutil.DeleteNamedImport(fset, f, d.name, d.path)
	}
	return formatFile(fset, f, file, prefix)
}

// usesPackageName reports whether any selector in f is name.X.
func usesPackageName(f *ast.File, name string) bool {
	used := false
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
				used = true
			}
		}
		return !used
	})
	return used
}

// HasDepsMarker reports whether dir holds a deps.go Narrow wrote.
func HasDepsMarker(dir string) bool {
	src, err := os.ReadFile(filepath.Join(dir, DepsFileName))
	return err == nil && strings.HasPrefix(string(src), DepsFileMarker)
}
