package lint

// In-process contract analysis — the version-skew fix for `forge lint
// --contract`.
//
// forge used to shell out to a separately-installed `contractlint`
// binary (PATH, then bin/, then `go run`), an artifact of the era when
// x/tools only exposed the analysis framework through
// multichecker.Main. A ~/go/bin contractlint installed weeks ago could
// silently disagree with a freshly-built forge — fresh forge + stale
// contractlint produced phantom violations with no version handshake to
// catch the skew. x/tools now exposes a programmatic driver
// (go/analysis/checker), so the analyzers — which live in-tree at
// internal/linter/contract — run inside the forge binary itself: the
// analysis is exactly as new as forge, with no PATH lookup and no
// possible skew. cmd/contractlint still ships for `go vet -vettool=`
// integration; it registers the same analyzer set.

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/linter/contract"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

// contractAnalyzers is the analyzer set `forge lint --contract` runs —
// identical to what cmd/contractlint registers with multichecker.
func contractAnalyzers() []*analysis.Analyzer {
	return []*analysis.Analyzer{
		contract.Analyzer,
		contract.RequireContractAnalyzer,
		contract.ExportedVarsAnalyzer,
	}
}

// contractDiagnostic is one analyzer diagnostic with its position
// resolved (relative to the working directory when possible).
type contractDiagnostic struct {
	Analyzer string
	Pos      token.Position
	Message  string
	// FixHint is the remediation, when the diagnostic carries its own (the
	// exclusion gate's findings do; the analyzers' share one generic hint).
	FixHint string
	// Warning marks a non-gating diagnostic (a reasonless suppression of an
	// exclusion-gate rule). Everything else gates.
	Warning bool
}

// codegenUnavailable reports whether the contract rules whose ERROR
// severity is justified by forge codegen (bootstrap wiring, generated
// mocks and decorators) should report as warnings instead: the tree has
// no forge.yaml (cfg is nil), so `forge generate` never runs there, and
// --strict was not asked for.
//
// Why a downgrade and not "stay an error with a better hint": a non-forge
// Go repo that runs `forge lint` (reliant does) otherwise has to repeat
// ONE project-level fact — "not a forge project, so a contract.go yields
// no mock or decorator" — as a suppression reason at every site. Reliant
// accumulated 35 of them. When every reason says the same thing, the
// reason has become ceremony, and the tool already knows the fact. The
// discipline stays visible as a warning on every run, and --strict keeps
// it gating for a repo that wants that.
func codegenUnavailable(cfg *config.ProjectConfig, strict bool) bool {
	return cfg == nil && !strict
}

// contractGateOptions is the exclusion gate's tuning for this run.
func contractGateOptions(cfg *config.ProjectConfig, strict bool) contract.ExcludeGateOptions {
	return contract.ExcludeGateOptions{CodegenUnavailable: codegenUnavailable(cfg, strict)}
}

func (d contractDiagnostic) String() string {
	s := fmt.Sprintf("%s: %s (%s)", d.Pos, d.Message, d.Analyzer)
	if d.FixHint != "" {
		s += "\n    ↳ " + d.FixHint
	}
	return s
}

// runContractAnalysisInProcess loads the requested packages and runs
// the contract analyzers in-process, returning position-sorted
// diagnostics. A load or analyzer failure is an error; diagnostics are
// findings, not errors. gate tunes the exclusion gate to the tree (see
// codegenUnavailable).
//
// Memory follows the request, not the module. The analyzers carry no facts,
// so they need source for the requested packages alone: their dependencies
// come from the compiler's export data. Measured on reliant's merge-gate
// scope (14 packages), the old load type-checked all 2,658 packages in their
// graph from source — the standard library and 2,099 third-party packages
// included — and peaked at 5.5 GiB of heap. The exclusion gate's
// multi-implementation search is the one consumer that needs more than the
// requested packages: it reads every package of their main module from
// source, because export data drops unexported types and an implementation
// wired in by dependency injection sits outside the marked package's import
// closure. When a requested package carries `//forge:exclude-contract` and
// the analysis load did not already parse the whole module, the gate gets a
// second load of the module's packages alone (their dependencies still from
// export data), made after the first has been released (excludeGateLoad).
func runContractAnalysisInProcess(ctx context.Context, paths, excludes []string, gate contract.ExcludeGateOptions) ([]contractDiagnostic, error) {
	// The exclude list mirrors forge.yaml's contracts.exclude — same
	// registration cmd/contractlint performs before multichecker.Main.
	contract.SetExcludes(excludes)

	cwd, _ := os.Getwd()
	out, deferred, err := analyzeContractPackages(ctx, cwd, paths, gate)
	if err != nil {
		return nil, err
	}
	if deferred != nil {
		// The analysis load is garbage now, but the GC's next target is
		// still twice its live heap: without a collection here, the gate's
		// load grows into that headroom and the two peaks stack (measured:
		// 3.8 GiB against 1.8 GiB with it).
		runtime.GC()
		gateFindings, err := deferred.check(ctx, gate)
		if err != nil {
			return nil, err
		}
		out = append(out, gateDiagnostics(cwd, gateFindings)...)
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pos.Filename != b.Pos.Filename {
			return a.Pos.Filename < b.Pos.Filename
		}
		if a.Pos.Line != b.Pos.Line {
			return a.Pos.Line < b.Pos.Line
		}
		if a.Pos.Column != b.Pos.Column {
			return a.Pos.Column < b.Pos.Column
		}
		return a.Analyzer < b.Analyzer
	})
	return out, nil
}

// analyzeContractPackages runs the analyzers and the gates over one load of
// paths (test variants included, dependencies from export data). When that
// load cannot serve the exclusion gate's search (see excludeGateFor), the
// gate is left out and returned as the load it needs instead; the packages
// are unreachable once this returns, so the caller's second load does not
// stack on top of them.
func analyzeContractPackages(ctx context.Context, cwd string, paths []string, gate contract.ExcludeGateOptions) ([]contractDiagnostic, *excludeGateLoad, error) {
	pkgs, err := loadPackagesForAnalysis(ctx, paths, analysisLoad{tests: true})
	if err != nil {
		return nil, nil, err
	}

	graph, err := checker.Analyze(contractAnalyzers(), pkgs, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("contract analysis: %w", err)
	}

	seen := map[string]bool{}
	var out []contractDiagnostic
	for act := range graph.All() {
		if !act.IsRoot {
			continue
		}
		if act.Err != nil {
			return nil, nil, fmt.Errorf("%s on %s: %w", act.Analyzer.Name, act.Package.PkgPath, act.Err)
		}
		for _, d := range act.Diagnostics {
			pos := act.Package.Fset.Position(d.Pos)
			pos.Filename = cwdRelative(cwd, pos.Filename)
			// Tests:true loads each package again as its test variant;
			// dedupe the identical diagnostics that produces.
			key := fmt.Sprintf("%s|%d|%d|%s|%s", pos.Filename, pos.Line, pos.Column, act.Analyzer.Name, d.Message)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, contractDiagnostic{Analyzer: act.Analyzer.Name, Pos: pos, Message: d.Message})
		}
	}

	// The exclusion gate: what `//forge:exclude-contract` costs (a reason,
	// and not being an outbound boundary or a multi-impl contract). A module
	// pass over loaded packages — see
	// internal/linter/contract/exclude_directive.go for why it is not an
	// analyzer. The forge.yaml contracts.exclude list does not silence it: that
	// list excludes paths, and the gate is about a marker in the source.
	// The not-a-component gate rides along: a reason, and a marker placed
	// where composition reads it (not_a_component_directive.go). It reads the
	// requested packages alone, so this load always serves it.
	gateFindings := contract.CheckNotAComponentDirectives(pkgs, gate)
	deferred, err := excludeGateFor(ctx, pkgs)
	if err != nil {
		return nil, nil, err
	}
	if deferred == nil {
		gateFindings = append(contract.CheckExcludeDirectives(pkgs, gate), gateFindings...)
	}
	return append(out, gateDiagnostics(cwd, gateFindings)...), deferred, nil
}

// excludeGateLoad is the load the exclusion gate needs when the analysis load
// cannot serve it: every package of the main module(s) the requested packages
// belong to, type-checked from source so that the multi-implementation search
// sees unexported types too, with roots naming the requested packages among
// them. The search spans the whole module, not the requested packages' import
// closure: an implementation wired in only by dependency injection lives in a
// package the marked package never imports, and a scoped run must refuse the
// marker exactly as an unscoped one does. It holds import paths and patterns,
// not packages, so nothing of the analysis load survives into it.
type excludeGateLoad struct {
	roots  []string
	search []string
}

// excludeGateFor returns the load the exclusion gate needs beyond pkgs, or
// nil when pkgs serves it: no requested package declares
// `//forge:exclude-contract` (so the gate has nothing to judge), or the
// analysis load already parsed every package of the requested packages'
// main modules — an unscoped `forge lint` requests them all.
func excludeGateFor(ctx context.Context, pkgs []*packages.Package) (*excludeGateLoad, error) {
	if !contract.DeclaresExcludeContract(pkgs) {
		return nil, nil
	}
	cwd, _ := os.Getwd()
	roots, modules := map[string]bool{}, map[string]bool{}
	for _, p := range pkgs {
		if !plainPackage(p) {
			continue
		}
		roots[p.PkgPath] = true
		if p.Module != nil && p.Module.Main && p.Module.Dir != "" {
			modules[modulePackagesPattern(cwd, p.Module.Dir)] = true
		}
	}
	if len(modules) == 0 {
		return nil, nil // no requested package is in a main module: nothing to search
	}
	parsed := map[string]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if plainPackage(p) && p.TypesInfo != nil {
			parsed[p.PkgPath] = true
		}
	})
	search := sortedSet(modules)
	inModule, err := listPackagePaths(ctx, search)
	if err != nil {
		return nil, err
	}
	covered := true
	for _, path := range inModule {
		if !parsed[path] {
			covered = false // not loaded, or from export data: its unexported types are missing
			break
		}
	}
	if covered {
		return nil, nil
	}
	return &excludeGateLoad{roots: sortedSet(roots), search: search}, nil
}

// modulePackagesPattern is the pattern naming every package of the module
// rooted at moduleDir and nothing else: the DIRECTORY pattern `<dir>/...`
// (cwd-relative when moduleDir lies under cwd), never the import-path pattern
// `<module path>/...`.
//
// The two differ. A directory pattern stops at a nested go.mod, as
// `go list ./...` does. An import-path pattern matches every package in the
// build list under that prefix, so a nested module the main module requires
// (control-plane's tools/registry-reliant, replaced to ./tools/registry-reliant)
// came along — and was loaded under the MAIN module's go.sum, which holds no
// entries for the nested module's own dependencies: "missing go.sum entry for
// module providing package github.com/aws/aws-sdk-go/aws". Where -mod=mod was
// in effect the loader did not fail but wrote those entries into the main
// module's go.sum instead.
func modulePackagesPattern(cwd, moduleDir string) string {
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, moduleDir); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "./..."
			}
			return "./" + filepath.ToSlash(rel) + "/..."
		}
	}
	return filepath.ToSlash(filepath.Join(moduleDir, "..."))
}

// listPackagePaths names the packages patterns match (go list, no type
// checking), with the analysis loader's environment and node_modules
// exclusion, so it agrees with what loadPackagesForAnalysis would load.
func listPackagePaths(ctx context.Context, patterns []string) ([]string, error) {
	env := analysisEnv(os.Environ(), hasWorkspaceGoMod())
	if cwd, werr := os.Getwd(); werr == nil {
		patterns = goexec.ExcludeNodeModules(ctx, cwd, env, patterns)
	}
	pkgs, err := packages.Load(&packages.Config{Context: ctx, Mode: packages.NeedName, Env: env}, patterns...)
	if err != nil {
		return nil, fmt.Errorf("list packages %v: %w", patterns, err)
	}
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.PkgPath)
	}
	return out, nil
}

// check runs the exclusion gate over its own load. Tests are not loaded: the
// gate judges a package's non-test source and searches plain packages only.
func (l *excludeGateLoad) check(ctx context.Context, gate contract.ExcludeGateOptions) ([]contract.ExcludeFinding, error) {
	pkgs, err := loadPackagesForAnalysis(ctx, l.search, analysisLoad{})
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, r := range l.roots {
		want[r] = true
	}
	var roots []*packages.Package
	for _, p := range pkgs {
		if want[p.PkgPath] {
			roots = append(roots, p)
		}
	}
	return contract.CheckExcludeDirectivesIn(roots, pkgs, gate), nil
}

// plainPackage reports whether p is a package as it builds outside tests: not
// a test variant (`p [p.test]`), an external test package or a test main.
func plainPackage(p *packages.Package) bool {
	return p.ID == p.PkgPath && !strings.HasSuffix(p.PkgPath, "_test") && !strings.HasSuffix(p.PkgPath, ".test")
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// gateDiagnostics maps the module-pass gates' findings onto diagnostics.
func gateDiagnostics(cwd string, fs []contract.ExcludeFinding) []contractDiagnostic {
	out := make([]contractDiagnostic, 0, len(fs))
	for _, f := range fs {
		out = append(out, contractDiagnostic{
			Analyzer: f.Rule, Pos: token.Position{Filename: cwdRelative(cwd, f.File), Line: f.Line},
			Message: f.Message, FixHint: f.FixHint,
			Warning: f.Severity != "" && f.Severity != finding.SeverityError,
		})
	}
	return out
}

// cwdRelative is file relative to cwd when it lies under it, else file.
func cwdRelative(cwd, file string) string {
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, file); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return file
}

// analysisEnv is the loader's environment: base, plus GOWORK=off and
// -mod=mod outside a go.work workspace, with -trimpath merged into GOFLAGS.
// An environment GOFLAGS replaces the go env file's, so setting -mod=mod alone
// dropped the -trimpath a workspace sets there, and every `forge lint` loaded
// the project into build-cache entries keyed on its worktree's absolute path.
func analysisEnv(base []string, inWorkspace bool) []string {
	env := append([]string(nil), base...)
	if !inWorkspace {
		env = appendEnvIfUnset(env, "GOWORK", "off")
		env = appendEnvIfUnset(env, "GOFLAGS", "-mod=mod")
	}
	env = ensureEnvDefault(env, "GOPROXY", "https://proxy.golang.org,direct")
	return goexec.WithTrimpath(env)
}

// analysisLoad is how much of the import graph loadPackagesForAnalysis
// type-checks from source.
type analysisLoad struct {
	// depsFromSource type-checks every dependency from source too
	// (go/packages NeedDeps), which an analyzer with facts needs:
	// checker.Analyze runs it over each dependency to produce them. Without
	// it a dependency's types come from the compiler's export data out of
	// the build cache, and only the requested packages — and any package on
	// an import path between two of them, without function bodies — are
	// parsed. That is what keeps a scoped lint's memory proportional to its
	// scope: the standard library and every third-party module stay out.
	depsFromSource bool
	// tests loads each requested package's test variants as well.
	tests bool
}

// loadPackagesForAnalysis loads paths with the syntax and type information
// go/analysis's checker needs for them, and for their dependencies as far as
// load says. Shared by the in-process analyzer lanes (contract, slow-tests).
// A package that fails to load is an error: half-typed packages would yield
// half-analyzed results.
func loadPackagesForAnalysis(ctx context.Context, paths []string, load analysisLoad) ([]*packages.Package, error) {
	// Same module-resolution discipline the subprocess used: outside a
	// go.work workspace, pin GOWORK=off and -mod=mod so the loader can
	// fetch missing modules; inside one (forge itself), honour the
	// workspace so the local pkg/ checkout resolves.
	env := analysisEnv(os.Environ(), hasWorkspaceGoMod())

	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
		packages.NeedImports | packages.NeedTypes |
		packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes |
		packages.NeedModule
	if load.depsFromSource {
		mode |= packages.NeedDeps // checker.Analyze's LoadAllSyntax, for facts
	}
	cfg := &packages.Config{
		Context: ctx,
		Mode:    mode,
		Env:     env,
		// Parity with the retired multichecker driver (-test=true) and
		// `go vet -vettool`: the analyzer lanes load test variants too.
		// The contract analyzers skip test files themselves
		// (contract/testfiles.go) — a test file is not a package's API — so
		// every driver agrees.
		Tests: load.tests,
	}
	// A node_modules package that ships Go source is a package of this
	// module as far as ./... is concerned, and not the project's to lint.
	if cwd, werr := os.Getwd(); werr == nil {
		paths = goexec.ExcludeNodeModules(ctx, cwd, env, paths)
	}
	pkgs, err := packages.Load(cfg, paths...)
	if err != nil {
		return nil, fmt.Errorf("load packages %v: %w", paths, err)
	}

	// Broken packages mean the type information the analyzers depend on
	// is incomplete — fail like the subprocess driver did rather than
	// reporting half-analyzed results.
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		const maxShown = 5
		shown := loadErrs
		if len(shown) > maxShown {
			shown = append(append([]string{}, shown[:maxShown]...), fmt.Sprintf("… and %d more", len(loadErrs)-maxShown))
		}
		return nil, fmt.Errorf("packages failed to load:\n  %s", strings.Join(shown, "\n  "))
	}

	return pkgs, nil
}
