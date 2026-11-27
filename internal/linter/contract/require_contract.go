package contract

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/contractsrc"
)

// RequireContractAnalyzer checks that packages in the module's internal/ tree
// with exported methods on structs have a contract.go file (or a
// //forge:contract type) defining the interface contract.
var RequireContractAnalyzer = &analysis.Analyzer{
	Name:     "requirecontract",
	Doc:      "checks that packages under the module's root internal/ with exported struct methods have a contract.go file",
	Run:      runRequireContract,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

// RequireContractFixHint is the remediation for a requirecontract finding:
// every way to satisfy the rule, and when the per-package opt-out is refused.
const RequireContractFixHint = "give the package a contract: mark its implementing struct `//forge:contract` " +
	"(forge derives the Service interface, its mock and decorator) or write contract.go — or unexport the " +
	"methods only the package itself calls. A package with no behavioural seam (pure functions, a data " +
	"vocabulary, a strategy registry) opts out with `//forge:exclude-contract: <why>` in its doc.go, which " +
	"forge refuses on a package that does outbound I/O or owns an interface with two implementations, " +
	"because there the contract is the point; or by listing its path under forge.yaml `contracts.exclude`. " +
	"See `forge skill load contracts`"

func init() {
	registerExcludeFlag(&RequireContractAnalyzer.Flags)
}

func runRequireContract(pass *analysis.Pass) (interface{}, error) {
	// Only check packages in the module's own internal/ tree — see
	// inModuleInternalTree for why a nested internal/ is not.
	pkgPath := pass.Pkg.Path()
	if !inModuleInternalTree(pass) {
		return nil, nil
	}

	// Skip external test packages (`package <name>_test`). These exist only to
	// host black-box tests; their Test/Benchmark/Example functions and any
	// test helper structs are not part of the package's API surface by Go
	// convention, so requiring a contract.go here would be spurious and would
	// force users into the internal-test form, losing API-boundary discipline.
	// The package-under-test is analyzed separately and still subject to the
	// contract rule.
	if strings.HasSuffix(pass.Pkg.Name(), "_test") {
		return nil, nil
	}

	// Skip commands (`package main`). See isMainPackage.
	if isMainPackage(pass) {
		return nil, nil
	}

	// Skip proto-service handler packages (internal/handlers/<svc>). By forge
	// convention these are pb-through Connect handlers: the package's
	// exported methods are the proto-defined RPC methods (GetCurrentUser, ...)
	// plus framework glue (Name/Register/RegisterHTTP), and the package
	// IMPLEMENTS the generated Connect handler interface
	// (<svc>v1connect.<Svc>ServiceHandler). Their contract is the proto service,
	// not a hand-written Go contract.go — the *Service methods ARE the rpc
	// logic, and logic extracted for isolation goes into a `forge scaffold
	// package` domain package (internal/<name>), which this rule still covers.
	// Requiring a contract.go here would duplicate the proto boundary. See the
	// api skill. (When a handler package DOES declare a contract.go the
	// single-seam Analyzer still enforces it; this only removes the
	// *requirement*.)
	if isHandlerPackage(pkgPath) {
		return nil, nil
	}

	// Skip the app composition seam (internal/app). By forge's architecture
	// this package is the explicit per-binary composition site — compose.go's
	// NewComponents bag, the scaffold-once providers.go (*Infra and its
	// DefaultClient accessor), lifecycle.go's typed Worker/Operator accessors,
	// and the Tier-1 mounts_services.go Mount<Svc> surface. Its exported
	// methods are wiring plumbing whose shape forge itself defines and
	// (partially) regenerates — not a behavioral service contract — so
	// requiring a contract.go here would demand an interface over forge's own
	// composition machinery that no caller consumes polymorphically. The
	// exemption is structural, not a bare path test: it applies only when the
	// package actually carries the forge seam files (see
	// isCompositionSeamPackage), so a hand-rolled unrelated `internal/app`
	// package remains subject to the rule.
	if isCompositionSeamPackage(pass, pkgPath) {
		return nil, nil
	}

	// Skip supervised worker packages (internal/workers/<name>). `forge
	// scaffold worker` emits a package whose exported methods ARE an
	// interface forge's own runtime defines — serverkit.Worker
	// (Name/Start/Stop, plus Run on the cron variant) — and which the
	// supervisor consumes polymorphically through that interface. A
	// contract.go here could only restate serverkit.Worker, for a package
	// shape forge itself scaffolds, so the requirement was forge's
	// scaffolder and forge's linter disagreeing about a fact forge
	// recorded.
	//
	// Like the composition-seam exemption above, this is STRUCTURAL rather
	// than a path blacklist: it applies only when the package actually
	// presents the worker lifecycle (see isSupervisedWorkerPackage), so an
	// ordinary package filed under internal/workers/ stays subject to the
	// rule. The alternative — having the scaffold stamp
	// `//forge:exclude-contract` — was rejected: that directive is the
	// USER's per-package opt-out, and spending it on forge's behalf would
	// silence the rule for any genuine contract the user later adds here.
	if isSupervisedWorkerPackage(pass, pkgPath) {
		return nil, nil
	}

	// Skip operator packages (internal/operators/<name>) on the same
	// structural terms: `forge scaffold operator` emits a controller whose
	// exported methods (Name, SetupWithManager, Reconcile) ARE the
	// controller-runtime reconciler surface the manager consumes, so a
	// contract.go could only restate it.
	if isOperatorPackage(pass, pkgPath) {
		return nil, nil
	}

	// Skip the ORM projection's package (internal/db by default, wherever
	// database.orm.package puts it), on the same structural terms: it holds
	// forge's generated stores (store_gen.go, <entity>_orm_gen.go), whose
	// generated <Entity>Store interfaces ARE the package's contract, and the
	// user-owned <entity>_repo_ext.go the db skill names as THE seam for the
	// queries generated CRUD cannot express. An exported method there is a
	// query on that seam, and the finding pointed at a generated file's
	// package clause asking for a contract.go nobody could write sensibly
	// (forge report 63a34d66).
	if isORMProjectionPackage(pass) {
		return nil, nil
	}

	// Honor forge.yaml's contracts.exclude AND the per-package
	// //forge:exclude-contract header — these packages are intentionally kept
	// contract-free (utility packages with no behavioral interface), opted out
	// via either source.
	if IsExcludedPass(pass) {
		return nil, nil
	}

	// Check if any struct has exported methods — but ONLY in hand-written
	// files. Forge-generated files (e.g. mock_gen.go / *_mock.go's mock
	// methods) are the codegen OUTPUT of a contract, not a hand-authored
	// behavioral surface, so they must not force a contract.go. A handler
	// package whose only exported methods come from generated files is
	// contract-defined by its proto service, not by a Go contract.go.
	// Skipping generated files here keeps the rule pointed at genuine
	// hand-written service packages.
	//
	// "Generated" is detected two ways (union): the shared
	// isForgeGeneratedFile predicate (generated.go — the canonical
	// `// Code generated ... DO NOT EDIT.` header, which covers files like
	// handlers/mocks/*_mock.go that don't use the _gen.go suffix, plus the
	// `// forge:hash=` self-certification marker), AND the `*_gen.go`
	// filename convention as a belt-and-suspenders fallback.
	//
	// Test files are skipped for the same reason (testfiles.go): a fake
	// with exported methods in a _test.go file is not a surface anything
	// that imports the package can call.
	hasExportedMethods := false
	private := privateReceiverTypes(pass)
	for _, file := range pass.Files {
		if hasExportedMethods {
			break
		}
		filename := pass.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(filename, "_gen.go") || isForgeGeneratedFile(file) || isTestFile(filename) {
			continue
		}
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
				continue
			}
			if isStdlibConventionMethod(funcDecl) {
				continue
			}
			if private[receiverTypeName(funcDecl)][funcDecl.Name.Name] {
				continue
			}
			if funcDecl.Name.IsExported() {
				hasExportedMethods = true
				break
			}
		}
	}

	if !hasExportedMethods {
		return nil, nil
	}

	// Check for a contract in the package: contract.go, the contract_gen.go
	// derived from a `//forge:contract` type, or that marked type itself
	// (before the first `forge generate` writes contract_gen.go).
	hasContract := false
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if contractsrc.IsContractFile(filepath.Base(filename)) || declaresMarkedContractType(file) {
			hasContract = true
			break
		}
	}

	if !hasContract {
		// Report on the package clause of the first file.
		if len(pass.Files) > 0 {
			reportWithHint(pass, pass.Files[0].Package, RequireContractFixHint,
				"package %s has exported methods but no contract.go%s",
				pass.Pkg.Name(), malformedExcludeHint(pass))
		}
	}

	return nil, nil
}

// malformedExcludeHint explains a contract finding on a package that MEANT to
// opt out: it carries `//forge:exclude-contract` in a spelling the directive
// parser does not read (`— <why>` instead of `: <why>`), so the opt-out did
// not happen. Without this the rule failed with no hint at all — the author
// had written the opt-out and could see it on screen.
func malformedExcludeHint(pass *analysis.Pass) string {
	dir := passPkgDir(pass)
	if dir == "" {
		return ""
	}
	bad := codegen.MalformedGoMarkers(dir, "forge:exclude-contract")
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf(" — %s:%d spells forge:exclude-contract in a form forge does not read, so the package is NOT excluded; write it as `//forge:exclude-contract: <reason>`",
		filepath.Base(bad[0].File), bad[0].Line)
}

// stdlibConventionMethods are exported method names whose meaning is fixed
// by the standard library rather than by the package's own API: the
// interfaces the runtime, fmt, encoding/json and friends look for by name.
// Implementing one is a rendering/marshalling convention on a data record,
// not a behavioral seam a caller would swap out — and because the stdlib
// matches on the exact exported name, the rule's usual "unexport it if it's
// helper-only" repair does not exist for them.
var stdlibConventionMethods = map[string]bool{
	"String":          true, // fmt.Stringer
	"Error":           true, // error
	"Unwrap":          true, // errors.Unwrap
	"Is":              true, // errors.Is
	"As":              true, // errors.As
	"Format":          true, // fmt.Formatter
	"GoString":        true, // fmt.GoStringer
	"MarshalJSON":     true, // json.Marshaler
	"UnmarshalJSON":   true, // json.Unmarshaler
	"MarshalText":     true, // encoding.TextMarshaler
	"UnmarshalText":   true, // encoding.TextUnmarshaler
	"MarshalYAML":     true, // yaml.Marshaler
	"UnmarshalYAML":   true, // yaml.Unmarshaler
	"MarshalBinary":   true, // encoding.BinaryMarshaler
	"UnmarshalBinary": true, // encoding.BinaryUnmarshaler
}

// isStdlibConventionMethod reports whether decl is one of the
// stdlibConventionMethods — a method that should not, on its own, make a
// package look like it has a behavioral contract. A package that ALSO has
// a genuine exported method is still reported: the check skips the
// convention methods rather than exempting the package.
func isStdlibConventionMethod(decl *ast.FuncDecl) bool {
	return stdlibConventionMethods[decl.Name.Name]
}

// receiverTypeName is the name of a method's receiver base type: T for
// `func (T) M()`, `func (*T) M()` and `func (t *T[K]) M()`.
func receiverTypeName(decl *ast.FuncDecl) string {
	expr := decl.Recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// privateReceiverTypes maps each of the package's unexported types whose
// methods no caller outside the package can reach as package API to the
// exported methods it has only to satisfy another package's interface.
//
// A type qualifies when it never appears in an exported signature, var,
// struct field or method result (directly or through a type that does), and
// implements none of the package's own exported interfaces. Of its exported
// methods, the ones exempted are those an interface declared by an imported
// package requires, which the type implements: `Quantize` on an unexported
// palette handed to gif.Options satisfies image/draw.Quantizer, so it cannot
// be unexported and is no surface a contract.go would describe.
//
// Still reported: a helper-only exported method on an unexported type (the
// rule's "unexport it" repair applies), an unexported implementation of an
// exported interface the package declares (`func New() Service` returning
// `*service` — that interface belongs in contract.go), and a type that
// escapes through the exported API (`func New() *impl`).
func privateReceiverTypes(pass *analysis.Pass) map[string]map[string]bool {
	pkg := pass.Pkg
	if pkg == nil {
		return nil
	}
	scope := pkg.Scope()
	reachable := apiReachableTypes(pkg)

	var localIfaces []*types.Interface
	for _, name := range scope.Names() {
		if tn, ok := scope.Lookup(name).(*types.TypeName); ok && tn.Exported() {
			if iface := methodInterface(tn); iface != nil {
				localIfaces = append(localIfaces, iface)
			}
		}
	}

	var imported []*types.Interface // computed on first need
	private := map[string]map[string]bool{}
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || tn.Exported() || tn.IsAlias() || reachable[tn] {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok || named.TypeParams().Len() > 0 || len(implemented(named, localIfaces)) > 0 {
			continue
		}
		if imported == nil {
			imported = importedInterfaces(pkg)
		}
		var exempt map[string]bool
		for _, iface := range implemented(named, imported) {
			for i := 0; i < iface.NumMethods(); i++ {
				if exempt == nil {
					exempt = map[string]bool{}
				}
				exempt[iface.Method(i).Name()] = true
			}
		}
		if exempt != nil {
			private[name] = exempt
		}
	}
	return private
}

// methodInterface returns tn's underlying interface when it declares at least
// one method, else nil.
func methodInterface(tn *types.TypeName) *types.Interface {
	if iface, ok := tn.Type().Underlying().(*types.Interface); ok && iface.NumMethods() > 0 {
		return iface
	}
	return nil
}

// implemented returns the interfaces of ifaces that named or *named implements.
func implemented(named *types.Named, ifaces []*types.Interface) []*types.Interface {
	var out []*types.Interface
	ptr := types.NewPointer(named)
	for _, iface := range ifaces {
		if types.Implements(named, iface) || types.Implements(ptr, iface) {
			out = append(out, iface)
		}
	}
	return out
}

// importedInterfaces lists every exported interface with methods declared by
// a package pkg imports, transitively.
func importedInterfaces(pkg *types.Package) []*types.Interface {
	out := []*types.Interface{}
	visited := map[*types.Package]bool{pkg: true}
	pkgs := append([]*types.Package(nil), pkg.Imports()...)
	for len(pkgs) > 0 {
		p := pkgs[0]
		pkgs = pkgs[1:]
		if visited[p] {
			continue
		}
		visited[p] = true
		pkgs = append(pkgs, p.Imports()...)
		for _, name := range p.Scope().Names() {
			if tn, ok := p.Scope().Lookup(name).(*types.TypeName); ok && tn.Exported() {
				if iface := methodInterface(tn); iface != nil {
					out = append(out, iface)
				}
			}
		}
	}
	return out
}

// apiReachableTypes returns pkg's named types a caller outside pkg can reach
// through its exported API: every type named by an exported object's type,
// and, transitively, by a reachable type's exported fields and methods.
func apiReachableTypes(pkg *types.Package) map[*types.TypeName]bool {
	r := &typeReach{pkg: pkg, reachable: map[*types.TypeName]bool{}, seen: map[types.Type]bool{}}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if obj := scope.Lookup(name); obj.Exported() {
			r.walk(obj.Type())
		}
	}
	for len(r.queue) > 0 {
		n := r.queue[0]
		r.queue = r.queue[1:]
		r.walk(n.Underlying())
		for i := 0; i < n.NumMethods(); i++ {
			if m := n.Method(i); m.Exported() {
				r.walk(m.Type())
			}
		}
	}
	return r.reachable
}

type typeReach struct {
	pkg       *types.Package
	reachable map[*types.TypeName]bool
	seen      map[types.Type]bool
	queue     []*types.Named
}

func (r *typeReach) walk(t types.Type) {
	if t == nil || r.seen[t] {
		return
	}
	r.seen[t] = true
	switch tt := t.(type) {
	case *types.Named:
		if obj := tt.Obj(); obj.Pkg() == r.pkg && !r.reachable[obj] {
			r.reachable[obj] = true
			r.queue = append(r.queue, tt)
		}
		for i := 0; i < tt.TypeArgs().Len(); i++ {
			r.walk(tt.TypeArgs().At(i))
		}
	case *types.Alias:
		r.walk(types.Unalias(tt))
	case *types.Pointer:
		r.walk(tt.Elem())
	case *types.Slice:
		r.walk(tt.Elem())
	case *types.Array:
		r.walk(tt.Elem())
	case *types.Map:
		r.walk(tt.Key())
		r.walk(tt.Elem())
	case *types.Chan:
		r.walk(tt.Elem())
	case *types.Signature:
		r.walkTuple(tt.Params())
		r.walkTuple(tt.Results())
	case *types.Struct:
		for i := 0; i < tt.NumFields(); i++ {
			if f := tt.Field(i); f.Exported() || f.Embedded() {
				r.walk(f.Type())
			}
		}
	case *types.Interface:
		for i := 0; i < tt.NumMethods(); i++ {
			r.walk(tt.Method(i).Type())
		}
		for i := 0; i < tt.NumEmbeddeds(); i++ {
			r.walk(tt.EmbeddedType(i))
		}
	}
}

func (r *typeReach) walkTuple(t *types.Tuple) {
	for i := 0; i < t.Len(); i++ {
		r.walk(t.At(i).Type())
	}
}

// isMainPackage reports whether the package under analysis is a command
// (`package main`). Go refuses to import a main package, so nothing outside
// it can call its exported methods or read its exported vars: exporting is
// not an API surface there, and a rule about a package's API has nothing to
// judge. The exported methods a command does have are almost always a
// stdlib interface it hands to the stdlib — flag.Value's Set, sort's Less —
// and demanding a contract.go for them only trained authors to spend the
// //forge:exclude-contract opt-out on every command
// (internal/consumercanary, internal/apigate). The command's collaborators,
// the packages it imports, are analyzed on their own.
func isMainPackage(pass *analysis.Pass) bool {
	return pass.Pkg.Name() == "main"
}

// isInternalPackage returns true if the package path contains an "internal/" segment.
func isInternalPackage(pkgPath string) bool {
	return strings.Contains(pkgPath, "/internal/") ||
		strings.HasPrefix(pkgPath, "internal/") ||
		pkgPath == "internal"
}

// inModuleInternalTree reports whether the package under analysis lives in
// the internal/ directory at the ROOT of its module: internal/<pkg>, not
// pkg/<sdk>/internal/<pkg> or cmd/<tool>/internal/<pkg>.
//
// That is the tree the contract earns its keep in. forge's contract codegen
// (the derived Service, mock_gen.go, the decorators) and the composition
// that constructs components both walk the project's root internal/ only
// (codegen.DiscoverInternalPackages, generateInternalPackageContracts). A
// nested internal/ is Go's "private to the parent" — a public SDK's
// plumbing under pkg/, a command's helpers — which forge never composes,
// and a contract.go written there gets no mock and no decorator. Requiring
// one demanded a file forge does nothing with: ranger's
// pkg/internal/rangerconn (the Connect plumbing behind its public
// pkg/rangerclient SDK) was flagged, could not use //forge:exclude-contract
// (it builds Connect clients, so the outbound-io refusal applies), and was
// contorted into returning closures instead of exporting methods. The
// public pkg/ packages above it were never subject to the rule.
//
// The module root comes from the nearest go.mod, and is trusted only when
// its module path plus the directory reproduces the import path. Otherwise
// (a GOPATH-mode fixture, a synthetic pass) the package path alone decides,
// as before: any internal segment.
func inModuleInternalTree(pass *analysis.Pass) bool {
	pkgPath := pass.Pkg.Path()
	if dir := passPkgDir(pass); dir != "" {
		if root, modPath := moduleOf(dir); modPath != "" {
			if rel, err := filepath.Rel(root, dir); err == nil {
				rel = filepath.ToSlash(rel)
				if (rel == "." && pkgPath == modPath) || pkgPath == modPath+"/"+rel {
					return rel == "internal" || strings.HasPrefix(rel, "internal/")
				}
			}
		}
	}
	return isInternalPackage(pkgPath)
}

// moduleCache maps a directory to its moduleInfo; analyzers run
// concurrently across packages.
var moduleCache sync.Map

type moduleInfo struct{ root, path string }

// moduleOf walks up from dir to the nearest go.mod and returns its directory
// and module path, or "", "" when there is none (or it does not parse).
func moduleOf(dir string) (string, string) {
	if v, ok := moduleCache.Load(dir); ok {
		m := v.(moduleInfo)
		return m.root, m.path
	}
	var m moduleInfo
	for cur := dir; ; {
		if data, err := os.ReadFile(filepath.Join(cur, "go.mod")); err == nil {
			m = moduleInfo{root: cur, path: modfile.ModulePath(data)}
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	moduleCache.Store(dir, m)
	return m.root, m.path
}

// isHandlerPackage returns true for proto-service handler packages, which by
// forge convention live under an `internal/handlers/` directory. Their contract
// is the proto service (the package implements the generated Connect handler
// interface), so they are exempt from the require-contract.go rule.
func isHandlerPackage(pkgPath string) bool {
	return strings.Contains(pkgPath, "/internal/handlers/") ||
		strings.HasPrefix(pkgPath, "internal/handlers/")
}

// isSupervisedWorkerPackage reports whether the package under analysis is a
// forge worker: it lives under internal/workers/ AND actually presents the
// direct serverkit.Worker or wrapped lifecyclekit.WorkerLifecycle surface.
// Both halves matter — the path anchors the
// exemption to the directory forge's architecture reserves for workers, and
// the method check makes it structural, so a helper package someone files
// under internal/workers/ is an ordinary package and stays subject to the
// require-contract rule. Only the package's own source counts: a fake worker
// in a test file does not make the package a worker (testfiles.go).
func isSupervisedWorkerPackage(pass *analysis.Pass, pkgPath string) bool {
	return hasWorkerLifecycle(pkgPath, pass.Files, pass.Fset, pass.TypesInfo)
}

// operatorLifecycleMethods is the method an operator's controller registers
// itself with the manager through.
var operatorLifecycleMethods = []string{"SetupWithManager"}

// isOperatorPackage reports whether the package lives under
// internal/operators/ AND declares the controller lifecycle.
func isOperatorPackage(pass *analysis.Pass, pkgPath string) bool {
	return roleHasMethods(pass, pkgPath, "internal/operators/", operatorLifecycleMethods)
}

// roleHasMethods reports whether pkgPath is under the role root and some
// method of its non-test source carries each of methods' names.
func roleHasMethods(pass *analysis.Pass, pkgPath, role string, methods []string) bool {
	if !strings.Contains(pkgPath, "/"+role) && !strings.HasPrefix(pkgPath, role) {
		return false
	}
	found := map[string]bool{}
	for _, file := range pass.Files {
		if isTestFile(pass.Fset.Position(file.Pos()).Filename) {
			continue
		}
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
				continue
			}
			found[funcDecl.Name.Name] = true
		}
	}
	for _, m := range methods {
		if !found[m] {
			return false
		}
	}
	return true
}

// isORMProjectionPackage reports whether the package holds forge's ORM
// projection: a forge-generated store_gen.go or <entity>_orm_gen.go. The
// generated header is required, so a hand-written file that happens to use
// the name does not make a package exempt.
func isORMProjectionPackage(pass *analysis.Pass) bool {
	for _, file := range pass.Files {
		base := filepath.Base(pass.Fset.Position(file.Pos()).Filename)
		if (base == "store_gen.go" || strings.HasSuffix(base, "_orm_gen.go")) && isForgeGeneratedFile(file) {
			return true
		}
	}
	return false
}

// composeSeamFiles are the forge-defined files of the app composition seam.
// Their presence is what certifies an `internal/app` package as forge's
// composition site (vs. a coincidentally-named user package):
//
//	compose.go          — the Components bag + NewComponents construction site
//	providers.go        — the owned *Infra provider set (+ DefaultClient)
//	mounts_services.go  — the Tier-1 typed Mount<Svc> surface + Inventory
//	lifecycle.go        — the typed Worker/Operator supervised-surface accessors
var composeSeamFiles = map[string]bool{
	"compose.go":             true,
	"providers.go":           true,
	"mounts_services_gen.go": true,
	"mounts_services.go":     true, // pre-rename spelling, still a seam marker
	"lifecycle.go":           true,
}

// isCompositionSeamPackage reports whether the package under analysis is
// forge's app composition seam: the module's `internal/app` package AND it
// actually carries at least one of the forge seam files (composeSeamFiles).
// Both halves matter — the path anchors the exemption to the one location
// forge's architecture reserves for composition, and the file check makes it
// structural rather than a path blacklist: an `internal/app` package that
// does NOT carry the seam is an ordinary user package and stays subject to
// the require-contract rule.
func isCompositionSeamPackage(pass *analysis.Pass, pkgPath string) bool {
	if pkgPath != "internal/app" && !strings.HasSuffix(pkgPath, "/internal/app") {
		return false
	}
	for _, file := range pass.Files {
		base := filepath.Base(pass.Fset.Position(file.Pos()).Filename)
		if composeSeamFiles[base] {
			return true
		}
	}
	return false
}

// declaresMarkedContractType reports whether file declares a non-interface
// type carrying `//forge:contract`: the implementation a package's Service
// contract is derived from.
func declaresMarkedContractType(file *ast.File) bool {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, isIface := ts.Type.(*ast.InterfaceType); isIface {
				continue
			}
			var declDoc *ast.CommentGroup
			if len(gd.Specs) == 1 {
				declDoc = gd.Doc
			}
			if contractsrc.HasDirectiveComment(contractsrc.Marker, declDoc, ts.Doc, ts.Comment) {
				return true
			}
		}
	}
	return false
}
