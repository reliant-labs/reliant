// File: internal/cli/audit/audit_unscoped_auth_module.go
//
// Two ways the unscoped_auth category learns what resolves the caller
// beyond forge's seam and the //forge:resolves-caller marker:
//
//   - It follows a call out of the handler package into another package of
//     the SAME module — `auth.MustGetUserID(ctx)` → `GetUserIDFromContext`
//     → the seam — and counts the handler as resolving the caller when the
//     function it names reaches the seam there.
//   - It reads `lint.auth.caller_resolvers` from forge.yaml, for a resolver
//     the marker cannot reach (a dependency, a sibling module).
//
// Why following a call across a package is safe without a type checker
//
// The package-local pass resolves NAMES, because a call site does not name
// its receiver's type; following a bare name into another package would
// clear handlers on coincidence. An import-qualified call is different:
// `auth.MustGetUserID` names a package (bound by the file's own import) and
// a package-level identifier in it, and a package-level function is unique
// in its package. So the walk follows only `<import>.<Func>` references
// whose import path is inside the main module, and reads the target the
// same way the handler package is read — its own fixed point, its own
// declared resolvers, its own imports.
//
// Inside a followed package forge's seam is matched as
// `middleware.GetUser` / `middleware.ClaimsFromContext`, qualifier
// included. The handler pass matches the selector name alone, which is
// tolerable in a handler package; a wrapper package is where `store.GetUser`
// (a row fetch, not the caller) lives, and clearing a handler on that would
// be the silent pass this category exists to prevent.
//
// Measured: reliant's handlers resolve the caller through
// `auth.MustGetUserID(ctx)` 215 times, and the audit reported all 368 of
// its authenticated rpcs as never resolving the caller.

package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// maxWrapperHops bounds how many packages past the handler package the walk
// follows: handler → auth → session → claims → middleware is four. Deeper
// factoring than that is rare enough that reporting it unscoped (and
// letting the project mark the resolver) beats an unbounded walk of the
// module on every audit.
const maxWrapperHops = 4

// moduleReach answers "does <import path>.<Func> resolve the caller?" for
// packages of the main module, parsing each package at most once.
type moduleReach struct {
	projectDir string
	module     string
	// seam is the vocabulary each followed package is judged against: the
	// declared and configured resolvers. Set once they are known.
	seam     callerSeam
	fset     *token.FileSet
	pkgs     map[string]*modulepackage // import path → parsed package; nil when not a package of this module
	memo     map[reachKey]map[string]bool
	visiting map[string]bool
}

type modulepackage struct {
	name  string
	files []*ast.File
}

// reachKey memoizes by remaining hops as well as by package, so a result
// cut short by the hop bound is never reused where more hops remain: the
// answer for a package does not depend on the order the walk reached it.
type reachKey struct {
	importPath string
	hops       int
}

// newModuleReach returns nil when the project has no go.mod: there is no
// module to follow calls into, and every method below is nil-safe.
func newModuleReach(projectDir string) *moduleReach {
	module, err := codegen.GetModulePath(projectDir)
	if err != nil || module == "" {
		return nil
	}
	return &moduleReach{
		projectDir: projectDir,
		module:     module,
		fset:       token.NewFileSet(),
		pkgs:       map[string]*modulepackage{},
		memo:       map[reachKey]map[string]bool{},
		visiting:   map[string]bool{},
	}
}

// inModule reports whether importPath names a package of the main module.
func (m *moduleReach) inModule(importPath string) bool {
	return importPath == m.module || strings.HasPrefix(importPath, m.module+"/")
}

// load parses the non-test files of a package of the main module, or
// returns nil for a path outside it, a nested module, or a directory with
// no Go files.
func (m *moduleReach) load(importPath string) *modulepackage {
	if p, ok := m.pkgs[importPath]; ok {
		return p
	}
	m.pkgs[importPath] = nil
	if !m.inModule(importPath) {
		return nil
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(importPath, m.module), "/")
	dir := filepath.Join(m.projectDir, filepath.FromSlash(rel))
	// A go.mod between the module root and the package makes it another
	// module's code, which this module's import path cannot reach.
	for d := dir; d != m.projectDir && strings.HasPrefix(d, m.projectDir); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	p := &modulepackage{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(m.fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		if p.name == "" {
			p.name = f.Name.Name
		}
		if f.Name.Name != p.name {
			continue
		}
		p.files = append(p.files, f)
	}
	if len(p.files) == 0 {
		return nil
	}
	m.pkgs[importPath] = p
	return p
}

// moduleImports maps the identifiers a file binds to packages of the main
// module onto their import paths. Blank and dot imports bind no qualifier.
func (m *moduleReach) moduleImports(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !m.inModule(importPath) {
			continue
		}
		var ident string
		switch {
		case imp.Name != nil:
			ident = imp.Name.Name
		default:
			p := m.load(importPath)
			if p == nil {
				continue
			}
			ident = p.name
		}
		if ident == "_" || ident == "." {
			continue
		}
		out[ident] = importPath
	}
	return out
}

// callsReachingImport reports whether fn calls `<import>.<Func>`, with the
// import a package of the main module, where Func resolves the caller
// within hops further packages.
//
// An import alias that fn itself declares as a name anywhere — a receiver,
// parameter or result, a `:=` (a select case's receive included), a var,
// const or type, a closure's parameter — is not followed in fn at all,
// whatever the scope: `auth.UserID(ctx)` on a local `auth` value is not the
// imported resolver. (A label lives in its own namespace and cannot shadow
// a package name.) The files are parsed
// without object resolution, so this is the conservative reading; a call it
// skips is reported, never cleared.
func (m *moduleReach) callsReachingImport(fn *ast.FuncDecl, f *ast.File, hops int) bool {
	if m == nil || hops <= 0 || fn == nil || fn.Body == nil || f == nil {
		return false
	}
	imports := m.moduleImports(f)
	for name := range localNames(fn) {
		delete(imports, name)
	}
	if len(imports) == 0 {
		return false
	}
	body := fn.Body
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// A selector used as a value (or nested in a field selector) does
		// not invoke the resolver. Follow only the call's direct target.
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if importPath, bound := imports[id.Name]; bound && m.reaching(importPath, hops-1)[sel.Sel.Name] {
			found = true
		}
		return !found
	})
	return found
}

// localNames are the identifiers fn declares anywhere in its signature or
// body.
func localNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	fields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				out[n.Name] = true
			}
		}
	}
	fields(fn.Recv)
	ast.Inspect(fn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncType:
			fields(n.TypeParams)
			fields(n.Params)
			fields(n.Results)
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		case *ast.RangeStmt:
			if n.Tok == token.DEFINE {
				for _, e := range []ast.Expr{n.Key, n.Value} {
					if id, ok := e.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				out[id.Name] = true
			}
		case *ast.TypeSpec:
			out[n.Name.Name] = true
		}
		return true
	})
	return out
}

// reaching returns the names in a package of the main module that resolve
// the caller, following further module imports for up to hops packages.
func (m *moduleReach) reaching(importPath string, hops int) map[string]bool {
	key := reachKey{importPath: importPath, hops: hops}
	if got, ok := m.memo[key]; ok {
		return got
	}
	// Go forbids import cycles, so this guard fires only on a tree that
	// does not build; it answers "nothing" rather than recursing forever.
	if m.visiting[importPath] {
		return nil
	}
	p := m.load(importPath)
	if p == nil {
		m.memo[key] = nil
		return nil
	}
	m.visiting[importPath] = true
	defer delete(m.visiting, importPath)

	pkgSeam := m.seam.forPackage(importPath)
	isSeamPkg := p.name == seamPackageName()
	seamFuncs := codegen.CRUDAuthSeamFuncs()

	var decls []*ast.FuncDecl
	fileOf := map[*ast.FuncDecl]*ast.File{}
	fileSeams := map[*ast.FuncDecl]fileCallerSeam{}
	for _, f := range p.files {
		fs := pkgSeam.forFile(f)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			decls = append(decls, fn)
			fileOf[fn] = f
			fileSeams[fn] = fs
		}
	}
	names := seamReachingNames(decls, func(fn *ast.FuncDecl) bool {
		// The scaffolded seam package's own GetUser/ClaimsFromContext ARE
		// the seam, so a wrapper declared beside them reaches it by name.
		if isSeamPkg && fn.Recv == nil && seamFuncs[fn.Name.Name] {
			return true
		}
		return pkgSeam.declaresLocal(fn) ||
			bodyReachesQualifiedSeam(fn.Body, fileSeams[fn]) ||
			m.callsReachingImport(fn, fileOf[fn], hops)
	})
	m.memo[key] = names
	return names
}

// seamPackageName is the package forge's seam lives in ("middleware"),
// read from the seam the generator stamps rather than spelled here.
func seamPackageName() string {
	pkg, _, _ := strings.Cut(codegen.CRUDAuthSeam(), ".")
	return pkg
}

// bodyReachesQualifiedSeam is bodyReachesAuthSeam with forge's seam matched
// only under its package qualifier (`middleware.GetUser`), for the packages
// the walk follows into — see the file header.
func bodyReachesQualifiedSeam(body *ast.BlockStmt, fs fileCallerSeam) bool {
	seamFuncs := codegen.CRUDAuthSeamFuncs()
	seamPkg := seamPackageName()
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				if (id.Name == seamPkg && seamFuncs[x.Sel.Name]) || fs.qualified[id.Name][x.Sel.Name] {
					found = true
				}
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && fs.dotted[id.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// CallerResolversConfigKey is where forge.yaml declares caller resolvers.
const CallerResolversConfigKey = "lint.auth.caller_resolvers"

// invalidResolverConfig is one lint.auth.caller_resolvers entry the audit
// could not honour, reported rather than dropped.
type invalidResolverConfig struct {
	Call   string `json:"call"`
	Reason string `json:"reason"`
}

// majorVersionSuffixRE matches a module major-version path element ("v2").
var majorVersionSuffixRE = regexp.MustCompile(`^v[0-9]+$`)

// configuredCallerResolvers reads lint.auth.caller_resolvers into the same
// shape the //forge:resolves-caller marker produces. The package name — the
// identifier an unaliased import binds — is read from the package clause
// when the package is in the main module, and otherwise taken from the
// import path's last element (skipping a /vN major-version suffix); an
// import that binds a different name can alias it.
func configuredCallerResolvers(cfg *config.ProjectConfig, mod *moduleReach) ([]codegen.CallerResolver, []invalidResolverConfig) {
	if cfg == nil {
		return nil, nil
	}
	var (
		out     []codegen.CallerResolver
		invalid []invalidResolverConfig
	)
	for _, entry := range cfg.Lint.Auth.CallerResolvers {
		call := strings.TrimSpace(entry.Call)
		idx := strings.LastIndex(call, ".")
		if idx <= 0 || idx == len(call)-1 || strings.HasSuffix(call[:idx], "/") {
			invalid = append(invalid, invalidResolverConfig{Call: entry.Call, Reason: `want "<import path>.<Func>", e.g. github.com/acme/app/internal/auth.UserID`})
			continue
		}
		importPath, fn := call[:idx], call[idx+1:]
		if !token.IsIdentifier(fn) {
			invalid = append(invalid, invalidResolverConfig{Call: entry.Call, Reason: fn + " is not a Go identifier"})
			continue
		}
		pkg := ""
		if mod != nil {
			if p := mod.load(importPath); p != nil {
				pkg = p.name
			}
		}
		if pkg == "" {
			pkg = path.Base(importPath)
			if majorVersionSuffixRE.MatchString(pkg) && path.Dir(importPath) != "." {
				pkg = path.Base(path.Dir(importPath))
			}
		}
		out = append(out, codegen.CallerResolver{ImportPath: importPath, Package: pkg, Func: fn, File: "forge.yaml"})
	}
	return out, invalid
}

// mergeCallerResolvers appends the configured resolvers to the declared
// ones, dropping a configured entry the code already declares.
func mergeCallerResolvers(declared, configured []codegen.CallerResolver) []codegen.CallerResolver {
	seen := map[string]bool{}
	out := make([]codegen.CallerResolver, 0, len(declared)+len(configured))
	for _, r := range append(append([]codegen.CallerResolver{}, declared...), configured...) {
		k := r.ImportPath + "." + r.Func
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}
