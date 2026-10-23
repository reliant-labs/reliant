package contract

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"

	"github.com/reliant-labs/forge/internal/codegen"
)

// RequireContractAnalyzer checks that internal packages with exported methods
// on structs have a contract.go file defining the interface contract.
var RequireContractAnalyzer = &analysis.Analyzer{
	Name:     "requirecontract",
	Doc:      "checks that internal packages with exported struct methods have a contract.go file",
	Run:      runRequireContract,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func init() {
	registerExcludeFlag(&RequireContractAnalyzer.Flags)
}

func runRequireContract(pass *analysis.Pass) (interface{}, error) {
	// Only check packages under internal/.
	pkgPath := pass.Pkg.Path()
	if !isInternalPackage(pkgPath) {
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
			if funcDecl.Name.IsExported() {
				hasExportedMethods = true
				break
			}
		}
	}

	if !hasExportedMethods {
		return nil, nil
	}

	// Check for contract.go in the package.
	hasContract := false
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if filepath.Base(filename) == "contract.go" {
			hasContract = true
			break
		}
	}

	if !hasContract {
		// Report on the package clause of the first file.
		if len(pass.Files) > 0 {
			pass.Reportf(pass.Files[0].Package,
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

// isInternalPackage returns true if the package path contains an "internal/" segment.
func isInternalPackage(pkgPath string) bool {
	return strings.Contains(pkgPath, "/internal/") ||
		strings.HasPrefix(pkgPath, "internal/") ||
		pkgPath == "internal"
}

// isHandlerPackage returns true for proto-service handler packages, which by
// forge convention live under an `internal/handlers/` directory. Their contract
// is the proto service (the package implements the generated Connect handler
// interface), so they are exempt from the require-contract.go rule.
func isHandlerPackage(pkgPath string) bool {
	return strings.Contains(pkgPath, "/internal/handlers/") ||
		strings.HasPrefix(pkgPath, "internal/handlers/")
}

// workerLifecycleMethods is the serverkit.Worker surface. A package under
// internal/workers/ presenting Name+Start is a supervised worker: those two
// are the interface's required methods (Stop is part of the shape too, but
// keying on the required pair keeps the check honest for a worker that
// legitimately has nothing to drain).
var workerLifecycleMethods = []string{"Name", "Start"}

// isSupervisedWorkerPackage reports whether the package under analysis is a
// forge worker: it lives under internal/workers/ AND actually presents the
// serverkit.Worker lifecycle. Both halves matter — the path anchors the
// exemption to the directory forge's architecture reserves for workers, and
// the method check makes it structural, so a helper package someone files
// under internal/workers/ is an ordinary package and stays subject to the
// require-contract rule. Only the package's own source counts: a fake worker
// in a test file does not make the package a worker (testfiles.go).
func isSupervisedWorkerPackage(pass *analysis.Pass, pkgPath string) bool {
	return roleHasMethods(pass, pkgPath, "internal/workers/", workerLifecycleMethods)
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
