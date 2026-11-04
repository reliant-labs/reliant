// File: internal/codegen/cross_pkg_interface.go
//
// Cross-package interface resolution for the testing.go auto-stub
// generator. The locally-declared interface path lives in
// interface_parser.go; this file handles the other half: when a
// service's Deps field is typed as `pkg.RepositoryName` (selector,
// not a bare identifier), we have to chase the `pkg` alias to its
// import path, load that package, and dig out the named interface.
//
// Failure mode is deliberately soft: if the import alias can't be
// resolved, the package can't load, or the named type isn't an
// interface, we return ok=false and the caller falls through to
// "field stays nil + emit a TODO marker". The auto-stub feature is
// a convenience layered on top of "user can hand-roll a stub" — the
// failure path must not break the test scaffold for projects whose
// repo packages happen to be temporarily un-buildable.
//
// We deliberately do not vendor the loaded package's source into the
// generated testing.go. We only read enough type information to
// emit a stub struct with the right method signatures.

package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// CrossPkgInterfaceResult bundles everything the auto-stub emitter
// needs to satisfy a selector-typed Deps field. ImportPath is the
// canonical import path of the package declaring the interface (so
// the testing.go imports block can pick up a new entry). NeededImports
// lists every extra import path the stub's method signatures
// reference (e.g. an `*orm.Context` parameter contributes orm's
// path). NeededImports is map[importPath]alias, where alias is the
// suggested local alias (typically the package's declared name).
type CrossPkgInterfaceResult struct {
	// PackagePath is the import path of the package declaring the
	// interface (e.g. "example.com/proj/internal/repo").
	PackagePath string
	// PackageName is the package's declared Go name (the qualifier
	// the testing.go file should use for the interface, e.g. "repo").
	PackageName string
	// Methods is the flattened method set of the interface, with each
	// method's parameter / result types fully qualified using package
	// aliases that appear in NeededImports.
	Methods []InterfaceMethod
	// NeededImports maps every extra import path the stub's method
	// signatures reference to the local alias used in the rendered
	// signatures. The interface's own package is INCLUDED here so
	// callers can fold one map into the file's import block.
	NeededImports map[string]string
}

// ResolveCrossPkgInterface attempts to locate `<pkgAlias>.<typeName>` —
// where pkgAlias was imported by some file in handlerDir — and return
// the data the bootstrap_testing.go template needs to emit a
// satisfying stub struct.
//
// Returns ok=false on any failure mode (alias not found, package
// can't load, type isn't an interface, etc.). The caller should
// treat ok=false the same as "no stub possible" and skip the field —
// the generated testing.go will leave it nil and rely on the user
// overriding via With<Svc>Deps when a test cares.
//
// Implementation outline:
//
//  1. Parse every non-test, non-_gen.go file in handlerDir to build
//     a map alias -> importPath. We take the FIRST alias declaration
//     we see; in practice each package is imported with one alias
//     per file and aliases agree across files in a well-formed
//     package.
//  2. Resolve pkgAlias to an import path.
//  3. Use golang.org/x/tools/go/packages to load that path, with
//     cfg.Dir = handlerDir so module resolution works (go.mod in
//     the project root governs the lookup).
//  4. Look the named type up in the package's types scope. Reject
//     anything not an interface.
//  5. Walk the interface's method set (which Go's types package
//     pre-flattens — embedded methods are included automatically).
//  6. Render each method's signature with types.TypeString plus a
//     custom Qualifier that records every package the signature
//     references AND returns the right alias for the rendered text.
func ResolveCrossPkgInterface(handlerDir, pkgAlias, typeName string) (CrossPkgInterfaceResult, bool) {
	return resolveCrossPkgInterface(handlerDir, pkgAlias, typeName, newTypesResolver(handlerDir, nil))
}

// resolveCrossPkgInterface is ResolveCrossPkgInterface reading packages
// through resolver, so a run that resolves many fields loads them together.
func resolveCrossPkgInterface(handlerDir, pkgAlias, typeName string, resolver *typesResolver) (CrossPkgInterfaceResult, bool) {
	imports, err := collectImports(handlerDir)
	if err != nil || len(imports) == 0 {
		return CrossPkgInterfaceResult{}, false
	}
	importPath, ok := imports[pkgAlias]
	if !ok {
		return CrossPkgInterfaceResult{}, false
	}
	// A package that did not type-check is absent: the method-set walk
	// below would return a partial answer (zero or wrong-arity methods).
	// Better to skip and let the user hand-roll a stub than emit a struct
	// that doesn't satisfy the interface at all.
	pkg := resolver.pkg(importPath)
	if pkg == nil {
		return CrossPkgInterfaceResult{}, false
	}
	iface, ok := resolver.lookup(importPath, typeName)
	if !ok {
		return CrossPkgInterfaceResult{}, false
	}

	// Pre-seed NeededImports with the interface's own package so the
	// stub's `<alias>.<TypeName>` reference resolves. Use the package's
	// declared name as the alias — same convention we already use for
	// imported handler packages.
	needed := map[string]string{
		importPath: pkg.Name(),
	}

	methods, ok := typedInterfaceMethods(iface, makeQualifier(needed))
	if !ok {
		return CrossPkgInterfaceResult{}, false
	}

	return CrossPkgInterfaceResult{
		PackagePath:   importPath,
		PackageName:   pkg.Name(),
		Methods:       methods,
		NeededImports: needed,
	}, true
}

// depsSelectorImports returns the import path of every `<pkg>.<Type>` a
// Deps struct in dirs names, deduplicated and sorted: what
// newTypesResolver should load up front for those directories' stubs.
func depsSelectorImports(dirs []string) []string {
	set := map[string]bool{}
	for _, dir := range dirs {
		fields, err := ParseServiceDeps(dir)
		if err != nil || len(fields) == 0 {
			continue
		}
		imports, err := collectImports(dir)
		if err != nil {
			continue
		}
		for _, f := range fields {
			t := strings.TrimSpace(f.Type)
			dot := strings.IndexByte(t, '.')
			if dot <= 0 || strings.ContainsAny(t, "*[]<>(){}") {
				continue
			}
			if importPath, ok := imports[t[:dot]]; ok {
				set[importPath] = true
			}
		}
	}
	return sortedPathSet(set)
}

// typedInterfaceMethods renders the COMPLETE method set of a type-checked
// interface into stub-ready InterfaceMethods, naming each package a
// signature references through qualifier.
//
// go/types flattens embedding at every depth and from every package, so an
// interface that embeds another package's interface — which may embed a third
// — arrives here with all of its methods. That is what the AST walk in
// ParseLocalInterfaces cannot do on its own, and why it hands cross-package
// embeds to this function.
//
// ok=false means no stub declared outside the interface's package can
// satisfy it: a method is unexported (only the declaring package can
// implement it), or a signature names a type the stub cannot spell — another
// package's unexported type, or an invalid type from a partial type-check. A
// stub that silently drops or misspells a method does not compile, so the
// caller leaves the field to the test instead.
func typedInterfaceMethods(iface *types.Interface, qualifier types.Qualifier) ([]InterfaceMethod, bool) {
	// iface.Method(i) is the full method set in go/types' stable (sorted)
	// order: deterministic, which is what generated code needs.
	var methods []InterfaceMethod
	for i := 0; i < iface.NumMethods(); i++ {
		fn := iface.Method(i)
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			continue
		}
		if !fn.Exported() || !signatureSpellable(sig) {
			return nil, false
		}
		m := InterfaceMethod{Name: fn.Name()}

		// Params: each tuple element's type, rendered with our qualifier
		// and never its name (see InterfaceMethod.Params).
		var paramParts []string
		for j := 0; j < sig.Params().Len(); j++ {
			paramParts = append(paramParts, types.TypeString(sig.Params().At(j).Type(), qualifier))
		}
		// Variadic last param: types.Signature.Variadic() is true
		// when the last param is a slice rendered as `...T`. We have
		// to convert []T → ...T by hand since types.TypeString gives
		// us "[]T" by default.
		if sig.Variadic() && len(paramParts) > 0 {
			last := paramParts[len(paramParts)-1]
			last = strings.Replace(last, "[]", "...", 1)
			paramParts[len(paramParts)-1] = last
		}
		m.Params = strings.Join(paramParts, ", ")
		m.ParamTypes = paramParts

		// Results: collect types, defer the parens decision to match
		// the existing buildInterfaceMethod shape.
		//
		// Each result's interface-ness is recorded alongside its
		// rendered text. We are already inside a full types walk, so
		// types.IsInterface answers it exactly — and it is the only
		// thing that can, since the rendered string for an interface
		// result is indistinguishable from a struct one. Getting this
		// wrong emits `store.CrewStore{}`, which is not valid Go; see
		// zeroValueForResultType.
		var resultTypes []string
		var resultIsIface []bool
		for j := 0; j < sig.Results().Len(); j++ {
			r := sig.Results().At(j)
			tStr := types.TypeString(r.Type(), qualifier)
			resultTypes = append(resultTypes, tStr)
			resultIsIface = append(resultIsIface, types.IsInterface(r.Type()))
		}
		m.ResultTypes = resultTypes
		for idx, t := range resultTypes {
			m.ResultZeros = append(m.ResultZeros, zeroValueForResultType(t, resultIsIface[idx]))
		}
		switch len(resultTypes) {
		case 0:
			m.Results = ""
			m.ReturnStatement = ""
		case 1:
			m.Results = resultTypes[0]
			m.ReturnStatement = "return " + m.ResultZeros[0]
		default:
			m.Results = "(" + strings.Join(resultTypes, ", ") + ")"
			m.ReturnStatement = "return " + strings.Join(m.ResultZeros, ", ")
		}
		methods = append(methods, m)
	}
	return methods, true
}

// signatureSpellable reports whether every type sig mentions can be written
// in another package: no invalid type, and no unexported named type of any
// package (types.TypeString would render `pkg.hidden`, which only pkg itself
// may name).
func signatureSpellable(sig *types.Signature) bool {
	ok := true
	var walk func(t types.Type, depth int)
	walk = func(t types.Type, depth int) {
		if !ok || t == nil || depth > 32 {
			return
		}
		switch x := t.(type) {
		case *types.Basic:
			if x.Kind() == types.Invalid {
				ok = false
			}
		case *types.Alias:
			if obj := x.Obj(); obj.Pkg() != nil && !obj.Exported() {
				ok = false
				return
			}
			walk(types.Unalias(x), depth+1)
		case *types.Named:
			// A named type of no package is a universe type (error).
			if obj := x.Obj(); obj.Pkg() != nil && !obj.Exported() {
				ok = false
				return
			}
			for i := 0; i < x.TypeArgs().Len(); i++ {
				walk(x.TypeArgs().At(i), depth+1)
			}
		case *types.Pointer:
			walk(x.Elem(), depth+1)
		case *types.Slice:
			walk(x.Elem(), depth+1)
		case *types.Array:
			walk(x.Elem(), depth+1)
		case *types.Map:
			walk(x.Key(), depth+1)
			walk(x.Elem(), depth+1)
		case *types.Chan:
			walk(x.Elem(), depth+1)
		case *types.Signature:
			for i := 0; i < x.Params().Len(); i++ {
				walk(x.Params().At(i).Type(), depth+1)
			}
			for i := 0; i < x.Results().Len(); i++ {
				walk(x.Results().At(i).Type(), depth+1)
			}
		case *types.Struct:
			for i := 0; i < x.NumFields(); i++ {
				walk(x.Field(i).Type(), depth+1)
			}
		case *types.Interface:
			for i := 0; i < x.NumMethods(); i++ {
				walk(x.Method(i).Type(), depth+1)
			}
		}
	}
	walk(sig, 0)
	return ok
}

// typesResolver answers `<importPath>.<TypeName>` with the type-checked
// interface, for packages loaded from one module.
//
// It loads from export data (NeedTypes without syntax), which is what
// makes it cheap: each package and its dependencies are compiled once
// into the build cache, never re-parsed here, and go/types still
// flattens embedded interfaces from every package. It loads in batches:
// newTypesResolver takes the import paths a caller already knows it will
// ask about and reads them in ONE go/packages call, and a later miss
// loads just that path. The harness step used to type-check each
// cross-package Deps field's package from source, one load per field — on
// reliant, 72s of a 74s `generate --steps harness`.
//
// A package that loads with errors is treated as absent — its partial
// types could hand back a method set with a hole in it, and a stub
// missing a method does not compile.
type typesResolver struct {
	dir string
	// pkgs holds every import path a load was attempted for; a nil value
	// is one that did not load cleanly.
	pkgs map[string]*types.Package
	// dirs maps each cleanly loaded import path to its source directory,
	// where forgeMock looks for the package's mock_gen.go.
	dirs map[string]string
	// mocks caches readForgeMocks per directory: a run asks about one
	// package's mocks once per Deps field that names it.
	mocks map[string]forgeMockIndex
	// loads counts go/packages calls, so a test can hold the batching to
	// one call per run.
	loads int
}

// newTypesResolver loads importPaths from dir's module. An empty list
// loads nothing, so a caller that never asks about a foreign package
// never invokes the toolchain.
func newTypesResolver(dir string, importPaths []string) *typesResolver {
	r := &typesResolver{
		dir:   dir,
		pkgs:  map[string]*types.Package{},
		dirs:  map[string]string{},
		mocks: map[string]forgeMockIndex{},
	}
	r.load(importPaths)
	return r
}

// load reads the importPaths not attempted yet, in one go/packages call.
func (r *typesResolver) load(importPaths []string) {
	var todo []string
	for _, p := range importPaths {
		if _, tried := r.pkgs[p]; tried {
			continue
		}
		r.pkgs[p] = nil
		todo = append(todo, p)
	}
	if len(todo) == 0 {
		return
	}
	cfg := &packages.Config{
		// NeedFiles is for dirs: a package's mock_gen.go sits beside its
		// other files, and the import path alone does not say where.
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedFiles,
		Dir:  r.dir,
		Env:  goexec.Env(),
	}
	r.loads++
	pkgs, err := packages.Load(cfg, todo...)
	if err != nil {
		return
	}
	for _, p := range pkgs {
		if p.Types == nil || p.PkgPath == "" || len(p.Errors) > 0 {
			continue
		}
		if _, asked := r.pkgs[p.PkgPath]; asked {
			r.pkgs[p.PkgPath] = p.Types
			if len(p.GoFiles) > 0 {
				r.dirs[p.PkgPath] = filepath.Dir(p.GoFiles[0])
			}
		}
	}
}

// pkg returns the type-checked package at importPath, loading it if no
// load has tried it yet, or nil when it did not load cleanly.
func (r *typesResolver) pkg(importPath string) *types.Package {
	if _, tried := r.pkgs[importPath]; !tried {
		r.load([]string{importPath})
	}
	return r.pkgs[importPath]
}

// lookup returns the interface importPath declares as typeName.
func (r *typesResolver) lookup(importPath, typeName string) (*types.Interface, bool) {
	pkg := r.pkg(importPath)
	if pkg == nil {
		return nil, false
	}
	tn, ok := pkg.Scope().Lookup(typeName).(*types.TypeName)
	if !ok {
		return nil, false
	}
	iface, ok := tn.Type().Underlying().(*types.Interface)
	return iface, ok
}

// collectImports parses every non-test, non-_gen.go .go file in dir
// and returns a map alias -> importPath. Aliases are taken from the
// import spec if present (`foo "x/y/z"`), otherwise the package
// name's declared identifier — which for our purposes is the LAST
// path segment, since the type-checker isn't loaded yet and we
// don't have access to the imported package's declared name.
//
// This is the same fast AST walk ParseServiceDeps uses; we don't
// reuse that function because we need the import information, not
// just the field list.
func collectImports(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") || strings.HasSuffix(e.Name(), "_gen.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, imp := range file.Imports {
			pathStr, perr := importPathFromSpec(imp)
			if perr != nil {
				continue
			}
			alias := aliasForImport(imp, pathStr)
			if _, seen := out[alias]; seen {
				continue
			}
			out[alias] = pathStr
		}
	}
	return out, nil
}

// importPathFromSpec extracts the quoted path out of an ImportSpec.
// Returns an error when the spec's Path.Value isn't a valid Go
// string literal — defensively guarding against malformed input.
func importPathFromSpec(spec *ast.ImportSpec) (string, error) {
	if spec == nil || spec.Path == nil {
		return "", fmt.Errorf("nil import spec")
	}
	// spec.Path.Value is a quoted string literal. Trim the quotes.
	v := spec.Path.Value
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", fmt.Errorf("malformed import path: %q", v)
	}
	return v[1 : len(v)-1], nil
}

// aliasForImport returns the local alias the file uses for the
// imported package. When the import has an explicit name (e.g.
// `foo "x/y"`), that wins. Otherwise we fall back to the last path
// segment, since that's what Go uses when there's no alias AND it
// matches the imported package's declared name in the overwhelming
// majority of cases (the exceptions — packages whose declared name
// disagrees with their import path's leaf — are rare in well-curated
// projects and resolved upstream by users adding an explicit alias).
func aliasForImport(spec *ast.ImportSpec, importPath string) string {
	if spec.Name != nil && spec.Name.Name != "" && spec.Name.Name != "_" && spec.Name.Name != "." {
		return spec.Name.Name
	}
	if idx := strings.LastIndex(importPath, "/"); idx >= 0 {
		return importPath[idx+1:]
	}
	return importPath
}

// makeQualifier returns a types.Qualifier that:
//
//   - For the empty-path "current package" (impossible here since we're
//     rendering an interface from another package), uses no qualifier.
//   - For every other package, records the path in `needed` and returns
//     the package's declared name so the rendered text reads
//     `pkg.Type` rather than the fully-qualified `path/to/pkg.Type`.
//
// The map is mutated in place; the caller passes the same map that
// was pre-seeded with the interface's own package so all references
// accumulate into one set.
func makeQualifier(needed map[string]string) types.Qualifier {
	return func(p *types.Package) string {
		if p == nil {
			return ""
		}
		path := p.Path()
		name := p.Name()
		if existing, ok := needed[path]; ok {
			return existing
		}
		needed[path] = name
		return name
	}
}

// SortedNeededImports turns the unordered map produced by
// ResolveCrossPkgInterface into a deterministic ordered slice. Used by
// the bootstrap_testing.go data assembly so generated imports are
// stable across runs (deterministic codegen — required for the
// checksums-based "no spurious diff" guarantee).
func SortedNeededImports(needed map[string]string) []ExtraImport {
	out := make([]ExtraImport, 0, len(needed))
	for path, alias := range needed {
		out = append(out, ExtraImport{Path: path, Alias: alias})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ExtraImport is a single rendered import line for the
// bootstrap_testing.go template's extra-imports block. Carries an
// explicit alias even when it matches the path's leaf, so the
// template can emit `<alias> "<path>"` uniformly — Go tolerates the
// redundant alias and it makes the template logic one line shorter.
type ExtraImport struct {
	Path  string
	Alias string
}
