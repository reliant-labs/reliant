package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// Resolver answers the two questions a mock renderer cannot answer from one
// file's syntax, by asking the Go toolchain:
//
//   - What is an unaliased import CALLED? `import "github.com/nats-io/nats.go"`
//     binds the identifier `nats` — the package's declared name — and nothing
//     in the import path says so. The generator used to guess the last path
//     element ("nats.go"), never matched the `nats.Msg` a signature used, and
//     dropped the import: control-plane's internal/svcdaemon/mock_gen.go
//     stopped building on every `forge generate --steps mocks`.
//   - What IS a foreign interface's method set? A Deps field naming
//     natsio.Requester is mocked from the type-checked interface, so embedded
//     interfaces contribute their methods and every package the signatures
//     reference arrives with its real path and name.
//
// One Resolver serves a whole `forge generate` run: NewResolver is called once
// with every contract.go and resolves them all in two go/packages loads (a
// name-only `go list` and one type-check of the foreign packages), instead of
// two per contract.
//
// Every answer is best-effort. A toolchain that cannot load a package (no `go`
// on PATH, a module missing from go.mod mid-edit) leaves that path
// unresolved, and the generator falls back to the syntax it can read — mock
// generation runs on every generate and must not block codegen over a dep the
// toolchain does not understand yet.
type Resolver struct {
	// names maps import path → declared package name.
	names map[string]string
	// pkgs holds the type-checked in-module packages that declare a
	// Deps-named interface, by import path.
	pkgs map[string]*packages.Package
	// self maps a contract's package directory to that package's import
	// path, so a contract can find its OWN type-checked package (loaded
	// when one of its interfaces embeds an interface another file of the
	// package declares) and render its types unqualified.
	self map[string]string
}

// NewResolver resolves, for every contract.go in contractPaths, the names of
// the packages its files import unaliased, the types of the in-module
// packages its Deps struct names, and the types of the packages its
// interfaces embed from. Contracts are grouped by module so a
// multi-module caller still gets one load per module.
func NewResolver(contractPaths ...string) *Resolver {
	r := &Resolver{names: map[string]string{}, pkgs: map[string]*packages.Package{}, self: map[string]string{}}

	type moduleWork struct {
		modulePath string
		dirs       []string        // contract package directories
		contracts  []string        // the contract.go files themselves
		names      map[string]bool // import paths whose package name we need
		foreign    map[string]bool // in-module packages to type-check
	}
	byRoot := map[string]*moduleWork{}
	var roots []string

	fset := token.NewFileSet()
	for _, contractPath := range contractPaths {
		dir := filepath.Dir(contractPath)
		moduleRoot, modulePath := findModule(dir)
		if moduleRoot == "" || modulePath == "" {
			continue
		}
		w := byRoot[moduleRoot]
		if w == nil {
			w = &moduleWork{modulePath: modulePath, names: map[string]bool{}, foreign: map[string]bool{}}
			byRoot[moduleRoot] = w
			roots = append(roots, moduleRoot)
		}
		w.dirs = append(w.dirs, dir)
		w.contracts = append(w.contracts, contractPath)
		absDir, _ := filepath.Abs(dir)
		if selfPath := importPathOf(moduleRoot, modulePath, absDir); selfPath != "" {
			r.self[absDir] = selfPath
			// An interface here that embeds one declared in ANOTHER file of
			// the package (a deps.go role embedding a role from roles.go)
			// has a method set this file's syntax cannot give: the package
			// is type-checked, like a foreign package an embed names.
			if embedsSiblingInterface(fset, contractPath) {
				w.foreign[selfPath] = true
			}
		}
	}

	// need records the name of every package dir's files import
	// unaliased. In-module packages are named by their package clause on
	// disk, which is exact and free; standard-library paths follow the
	// convention exactly; only the rest are batched for `go list`.
	need := func(moduleRoot string, w *moduleWork, dir string, aliasedToo bool) {
		for _, path := range importedPaths(fset, dir, aliasedToo) {
			if _, known := r.names[path]; known {
				continue
			}
			if pkgDir, inModule := packageDir(moduleRoot, w.modulePath, path); inModule {
				if name := packageClause(fset, pkgDir); name != "" {
					r.names[path] = name
				}
				continue
			}
			if !isStdlibPath(path) {
				w.names[path] = true
			}
		}
	}

	sort.Strings(roots)
	for _, root := range roots {
		w := byRoot[root]
		for _, dir := range w.dirs {
			need(root, w, dir, false)
		}
		// Deps qualifiers are resolved through the names just recorded,
		// so a dep whose package is not named after its directory is
		// still found.
		for _, dir := range w.dirs {
			for _, f := range depsFieldTypes(dir, fset, r) {
				pkgDir, ok := packageDir(root, w.modulePath, f.importPath)
				if !ok || w.foreign[f.importPath] {
					continue
				}
				w.foreign[f.importPath] = true
				// The syntax fallback qualifies the foreign package's
				// signatures by each package's DECLARED name — aliased
				// imports included — so the mock reads the same whether
				// or not the package type-checked.
				need(root, w, pkgDir, true)
			}
		}
		r.loadNames(root, sortedKeys(w.names))
		// Interfaces a contract EMBEDS from another package are typed
		// too, from any module or the standard library (`io.Closer`):
		// their methods are part of the contract's method set. Read
		// after loadNames, so an unaliased import resolves by its
		// declared name. One load keeps go/types identity across all.
		for _, contractPath := range w.contracts {
			for _, path := range embeddedImportPaths(fset, contractPath, r) {
				w.foreign[path] = true
			}
		}
		r.loadTypes(root, sortedKeys(w.foreign))
	}
	return r
}

// embedsSiblingInterface reports whether an interface declared in
// contractPath embeds, by bare name, an interface the file does not declare
// — one declared in another file of the same package.
func embedsSiblingInterface(fset *token.FileSet, contractPath string) bool {
	file, err := parser.ParseFile(fset, contractPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	declared := map[string]bool{}
	var embeds []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			declared[ts.Name.Name] = true
			it, ok := ts.Type.(*ast.InterfaceType)
			if !ok || it.Methods == nil {
				continue
			}
			for _, field := range it.Methods.List {
				if id, ok := field.Type.(*ast.Ident); ok && id.Name != "error" {
					embeds = append(embeds, id.Name)
				}
			}
		}
	}
	for _, name := range embeds {
		if !declared[name] {
			return true
		}
	}
	return false
}

// importPathOf is the import path of the package in dir, inside the module
// rooted at moduleRoot (the inverse of packageDir); "" when dir is outside it.
func importPathOf(moduleRoot, modulePath, dir string) string {
	rel, err := filepath.Rel(moduleRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if rel == "." {
		return modulePath
	}
	return modulePath + "/" + filepath.ToSlash(rel)
}

// selfPackage returns the type-checked package a contract in dir belongs
// to, and its import path. The package is nil when it was not loaded (no
// interface of the contract embeds a sibling-file interface) or did not type.
func (r *Resolver) selfPackage(dir string) (*packages.Package, string) {
	if r == nil {
		return nil, ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, ""
	}
	path := r.self[abs]
	if path == "" {
		return nil, ""
	}
	return r.pkgs[path], path
}

// embeddedImportPaths returns the import paths of every package an interface
// declared in contractPath embeds an interface from.
func embeddedImportPaths(fset *token.FileSet, contractPath string, r *Resolver) []string {
	file, err := parser.ParseFile(fset, contractPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	imports := importRefs(file, r)
	seen := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		it, ok := n.(*ast.InterfaceType)
		if !ok || it.Methods == nil {
			return true
		}
		for _, field := range it.Methods.List {
			sel, ok := field.Type.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok {
				if ref, ok := imports[id.Name]; ok {
					seen[ref.path] = true
				}
			}
		}
		return true
	})
	return sortedKeys(seen)
}

// packageClause returns the package name the non-test Go files in dir
// declare ("" when there are none). Generated and hand-written files agree in
// any package that builds; the first readable clause is taken.
func packageClause(fset *token.FileSet, dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if err != nil || file.Name == nil {
			continue
		}
		return file.Name.Name
	}
	return ""
}

// loadNames runs a name-only `go list` over paths: no compilation, so it is
// cheap and succeeds for packages that do not build.
func (r *Resolver) loadNames(dir string, paths []string) {
	if len(paths) == 0 {
		return
	}
	cfg := &packages.Config{Mode: packages.NeedName, Dir: dir, Env: goexec.Env()}
	pkgs, err := packages.Load(cfg, paths...)
	if err != nil {
		return
	}
	for _, p := range pkgs {
		if p.Name != "" && p.PkgPath != "" {
			r.names[p.PkgPath] = p.Name
		}
	}
}

// loadTypes type-checks the in-module packages that declare Deps-named
// interfaces, and every package a contract interface embeds from, in ONE
// load so go/types identity holds across them. Packages
// that fail to load keep whatever partial types go/packages produced; the
// renderer checks each interface for invalid types before trusting it.
func (r *Resolver) loadTypes(dir string, paths []string) {
	if len(paths) == 0 {
		return
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes,
		Dir:  dir,
		Env:  goexec.Env(),
	}
	pkgs, err := packages.Load(cfg, paths...)
	if err != nil {
		return
	}
	for _, p := range pkgs {
		if p.Types == nil || p.PkgPath == "" {
			continue
		}
		r.pkgs[p.PkgPath] = p
		if p.Name != "" {
			r.names[p.PkgPath] = p.Name
		}
		// Everything a type-checked package imports is named for free.
		for _, imp := range p.Types.Imports() {
			if _, known := r.names[imp.Path()]; !known {
				r.names[imp.Path()] = imp.Name()
			}
		}
	}
}

// packageName returns the declared name of the package at importPath. It is
// exact when the toolchain resolved the path; otherwise it falls back to the
// Go convention (last element, or the one before a /vN major-version suffix).
func (r *Resolver) packageName(importPath string) string {
	if r != nil {
		if name, ok := r.names[importPath]; ok {
			return name
		}
	}
	return conventionalPackageName(importPath)
}

// foreignPackage returns the type-checked package at importPath, or nil when
// it was not loaded.
func (r *Resolver) foreignPackage(importPath string) *packages.Package {
	if r == nil {
		return nil
	}
	return r.pkgs[importPath]
}

// conventionalPackageName is the name an import path conventionally binds
// when nothing better is known: its last element, except that a /vN
// major-version suffix (math/rand/v2, github.com/jackc/pgx/v5) names the
// element before it. It is EXACT for the standard library — which is why
// stdlib paths are never sent to `go list` — and the fallback for paths the
// toolchain could not resolve. It deliberately does not strip ".go" or
// "go-": that is a guess, and the toolchain is asked instead.
func conventionalPackageName(importPath string) string {
	elems := strings.Split(importPath, "/")
	last := elems[len(elems)-1]
	if len(elems) > 1 && isMajorVersionElem(last) {
		return elems[len(elems)-2]
	}
	return last
}

func isMajorVersionElem(elem string) bool {
	if len(elem) < 2 || elem[0] != 'v' {
		return false
	}
	n, err := strconv.Atoi(elem[1:])
	return err == nil && n >= 2
}

// isStdlibPath reports whether importPath names a standard-library package:
// its first element has no dot. (In-module paths are resolved from disk
// before this is consulted, so a dotless module path is not misread.)
func isStdlibPath(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

// importedPaths returns the import paths that non-test, non-generated Go
// files in dir import without an explicit name — or with one too, when
// aliasedToo.
func importedPaths(fset *token.FileSet, dir string, aliasedToo bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range file.Imports {
			if imp.Name != nil && !aliasedToo {
				continue
			}
			if path, err := strconv.Unquote(imp.Path.Value); err == nil {
				seen[path] = true
			}
		}
	}
	return sortedKeys(seen)
}

// importLocalName is the identifier an import spec binds in its file.
func (r *Resolver) importLocalName(imp *ast.ImportSpec) (name, path string, explicit bool) {
	path, err := strconv.Unquote(imp.Path.Value)
	if err != nil {
		path = strings.Trim(imp.Path.Value, `"`)
	}
	if imp.Name != nil {
		return imp.Name.Name, path, true
	}
	return r.packageName(path), path, false
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
