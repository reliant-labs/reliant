// File: internal/codegen/observe_wrap.go
//
// The instrument decision for a component package, and detection of its
// OPTIONAL owned observability seam (observe_chain.go).
//
// Every contract package whose constructor returns its Service interface gets
// the generated observability decorator (middleware_gen.go): one span, the
// call metrics and a log line per method call, routed through an in-process
// observe.ComponentChain. The decorator is forge-owned and regenerated from
// the Service interface every run (contract.WriteObservedDecorator), so adding
// a method to the contract grows the wrapper automatically.
//
// Instrumentation is the DEFAULT, not an opt-in. It used to be keyed on a
// `// forge:constructor` marker or an observe_chain.go seam, which left every
// package that predated the scaffold silently untraced — 26 of 46 in the
// control plane, its hottest business paths among them. `// forge:no-observe`
// on the package (or on one method) is the opt-out.
//
// The chain is observe.DefaultComponentChain unless the package declares the
// owned seam
//
//	func newObserveChain() *observe.ComponentChain { ... }
//
// (DetectObserveChainSeam), which the decorator then calls instead — the place
// to add a middleware, drop a layer or tune logging for one package.
//
// compose.go / reconcileComposeAddComponents emit an instrumented component's
// construction in wrapped form: `c.X = pkg.New<Concrete>WithForgeMiddleware(pkg.New(pkg.Deps{...}))`,
// where the wrapper constructor — named after the constructor's CONCRETE return
// type (ResolveMiddlewareWrapper), e.g. NewServiceWithForgeMiddleware — lives in
// the generated decorator. Concrete-returning handler packages are never
// wrapped: otelconnect owns the RPC edge, and wrapping a *Service would change
// the Components field type.

package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// observeChainSeamFunc is the owned chain-builder a package may declare to
// replace observe.DefaultComponentChain. Kept in sync with
// internal/generator/contract (observeChainSeamFunc) and the
// observe_chain.go.tmpl scaffold.
const observeChainSeamFunc = "newObserveChain"

// ShouldInstrumentComponent reports whether the component package at dir gets
// the generated observability decorator (and, at the composition site, the
// wrapped `pkg.New<Concrete>WithForgeMiddleware(pkg.New(...))` emission). It is THE single
// instrument decision — the compose assembler, the generate walk and the
// tracing lint all route through it so they can never disagree.
//
// ctorType is DetectConstructorType(dir); ifaceName is the component's
// Service-contract interface name (serviceIfaceOrDefault). A package is
// instrumented when all hold:
//
//  1. the constructor RETURNS the Service interface (ctorType == ifaceName).
//     Handler-shaped packages return a concrete *Service and are never wrapped
//     — otelconnect owns the RPC edge, and wrapping *Service would change the
//     Components field type.
//  2. it is a contract package — a contract.go the contracts step generates
//     from, not opted out with `//forge:exclude-contract` — because that step
//     is what writes the decorator. A package returning an interface with no
//     contract.go has no middleware_gen.go, and wrapping it would not compile.
//     (A package excluded in forge.yaml's contracts.exclude is not discovered
//     as a component at all.)
//  3. it has NOT opted out via a package-level `// forge:no-observe`
//     (HasPackageNoObserveDirective).
func ShouldInstrumentComponent(dir, ctorType, ifaceName string) bool {
	if ifaceName == "" {
		ifaceName = "Service"
	}
	if ctorType != ifaceName {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "contract.go")); err != nil {
		return false
	}
	if HasExcludeContractDirective(dir) {
		return false
	}
	return !HasPackageNoObserveDirective(dir)
}

// DetectObserveChainSeam reports whether the component package at dir declares
// the OWNED observability seam `func newObserveChain(...) ...` in a non-
// generated, non-test source file. When it does, the generated decorator calls
// it for its chain; otherwise the decorator uses observe.DefaultComponentChain.
//
// Returns false for a missing/unparseable dir. Generated (_gen.go) and test
// files are ignored, so the generated decorator's own reference to the seam
// never counts as the seam itself.
func DetectObserveChainSeam(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name == nil {
				continue
			}
			if fn.Name.Name == observeChainSeamFunc {
				return true
			}
		}
	}
	return false
}
