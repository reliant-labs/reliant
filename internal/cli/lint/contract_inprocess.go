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
func runContractAnalysisInProcess(ctx context.Context, paths, excludes []string, gate contract.ExcludeGateOptions) ([]contractDiagnostic, error) {
	// The exclude list mirrors forge.yaml's contracts.exclude — same
	// registration cmd/contractlint performs before multichecker.Main.
	contract.SetExcludes(excludes)

	pkgs, err := loadPackagesForAnalysis(ctx, paths)
	if err != nil {
		return nil, err
	}

	graph, err := checker.Analyze(contractAnalyzers(), pkgs, nil)
	if err != nil {
		return nil, fmt.Errorf("contract analysis: %w", err)
	}

	cwd, _ := os.Getwd()
	seen := map[string]bool{}
	var out []contractDiagnostic
	for act := range graph.All() {
		if !act.IsRoot {
			continue
		}
		if act.Err != nil {
			return nil, fmt.Errorf("%s on %s: %w", act.Analyzer.Name, act.Package.PkgPath, act.Err)
		}
		for _, d := range act.Diagnostics {
			pos := act.Package.Fset.Position(d.Pos)
			if cwd != "" {
				if rel, rerr := filepath.Rel(cwd, pos.Filename); rerr == nil && !strings.HasPrefix(rel, "..") {
					pos.Filename = rel
				}
			}
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
	// pass over the same loaded packages — see
	// internal/linter/contract/exclude_directive.go for why it is not an
	// analyzer. The forge.yaml contracts.exclude list does not silence it: that
	// list excludes paths, and the gate is about a marker in the source.
	// The not-a-component gate rides the same pass: a reason, and a marker
	// placed where composition reads it (not_a_component_directive.go).
	gateFindings := contract.CheckExcludeDirectives(pkgs, gate)
	gateFindings = append(gateFindings, contract.CheckNotAComponentDirectives(pkgs, gate)...)
	for _, f := range gateFindings {
		pos := token.Position{Filename: f.File, Line: f.Line}
		if cwd != "" {
			if rel, rerr := filepath.Rel(cwd, pos.Filename); rerr == nil && !strings.HasPrefix(rel, "..") {
				pos.Filename = rel
			}
		}
		out = append(out, contractDiagnostic{
			Analyzer: f.Rule, Pos: pos, Message: f.Message, FixHint: f.FixHint,
			Warning: f.Severity != "" && f.Severity != finding.SeverityError,
		})
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

// loadPackagesForAnalysis loads paths, test variants included, with the full
// syntax and type information go/analysis's checker needs. Shared by the
// in-process analyzer lanes (contract, slow-tests). A package that fails to
// load is an error: half-typed packages would yield half-analyzed results.
func loadPackagesForAnalysis(ctx context.Context, paths []string) ([]*packages.Package, error) {
	// Same module-resolution discipline the subprocess used: outside a
	// go.work workspace, pin GOWORK=off and -mod=mod so the loader can
	// fetch missing modules; inside one (forge itself), honour the
	// workspace so the local pkg/ checkout resolves.
	env := analysisEnv(os.Environ(), hasWorkspaceGoMod())

	cfg := &packages.Config{
		Context: ctx,
		// checker.Analyze requires LoadAllSyntax-equivalent data.
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes |
			packages.NeedModule,
		Env: env,
		// Parity with the retired multichecker driver (-test=true) and
		// `go vet -vettool`: test variants are loaded too. The analyzers
		// skip test files themselves (contract/testfiles.go) — a test
		// file is not a package's API — so every driver agrees.
		Tests: true,
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
