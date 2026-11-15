// Package contractcheck inspects a project's internal/ tree for
// contract-shape violations. It is the single home for the analyzers
// that previously lived as four sibling files under
// internal/linter/forgeconv/:
//
//   - internal-package-contract-names (Service/Deps/New canonical shape)
//   - deps-are-interfaces             (every Deps field is an interface)
//   - outbound-io-no-rpc              (an outbound boundary registers no RPC)
//
// The forge entry points that used to call those analyzers individually
// (`preCodegenContractCheck` in internal/cli/generate.go and
// `runConventionLint` in internal/cli/lint.go) now route through one
// [Inspect] function. Per-call-site differences — which rules to run,
// what to exclude — are expressed as fields on [Options], not as
// branches inside the engine.
//
// # API design notes
//
// Findings carry the same shape as the other forge lint rules
// ([forgeconv.Finding]) so they can be folded into the existing
// `combined := forgeconv.Result{}` pipelines without adapter glue.
// Severity, rule names, and remediation phrasing are preserved verbatim
// from the old call sites — this package is a re-organization, not a
// behavior change.
//
// # What this package is NOT
//
// The cmd/contractlint binary (and its supporting internal/linter/contract
// package) performs a fundamentally different check: it runs as a
// go/analysis multichecker that needs full type information to assert
// "exported methods on impl types must be declared in the contract
// interface". That check operates on *analysis.Pass, not on a rootDir
// walk, and so cannot share this engine. The two are complementary —
// contractcheck catches structural-shape mistakes at codegen time
// (before the type checker has anything to chew on); the multichecker
// catches behavioral-shape mistakes at vet time.
package contractcheck

import (
	"context"
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// Rule identifies a contract-shape analyzer by its canonical name. The
// names match the strings that already appear in finding output, so
// log-search queries and CI grep patterns survive this refactor
// unchanged.
type Rule string

// The set of rules this package owns. New rules added here MUST be
// surfaced in [AllRules] so the default "run everything" path picks
// them up.
const (
	// RuleInternalPackageContractNames enforces the canonical
	// `type Service interface`, `type Deps struct`, and
	// `func New(Deps) Service` trio on every internal-package
	// contract.go. Errors gate the build because the bootstrap codegen
	// template hardcodes these names — a non-canonical contract
	// produces a bootstrap that doesn't compile.
	RuleInternalPackageContractNames Rule = "forgeconv-internal-package-contract-names"

	// RuleDepsAreInterfaces warns when any package declares a Deps
	// field whose type is a concrete struct (or pointer to one) rather
	// than an interface. Concrete deps defeat the all-mock unit-test
	// surface that `New(Deps)` exists to provide.
	RuleDepsAreInterfaces Rule = "forgeconv-deps-are-interfaces"

	// RuleOutboundIONoRPC warns when a package marked
	// `// forge:outbound-io` registers a Connect RPC handler. The
	// marker asserts the package only calls OUT; an inbound handler
	// contradicts it, and the RPC surface is the actual service.
	RuleOutboundIONoRPC Rule = "forgeconv-outbound-io-no-rpc"

	// RuleInternalPackageMissingContract warns when a package under
	// internal/ has a component's shape but no contract.go — a component
	// created by hand instead of by `forge scaffold package`. Everything
	// downstream keys on contract.go (bootstrap wiring, mock generation,
	// the observe decorator, every reporter), so such a package is not
	// half-wired, it is invisible. RuleInternalPackageContractNames only
	// ever looked INSIDE a contract.go; this is its complement.
	RuleInternalPackageMissingContract Rule = "forgeconv-internal-package-missing-contract"
)

// AllRules is the canonical "run everything" list. Callers that pass
// Options.Rules == nil get this set. It is a getter (not an exported package
// var) so the returned slice can't be mutated by callers and so it satisfies
// the exported-vars contract rule (no mutable exported package state).
func AllRules() []Rule {
	return []Rule{
		RuleInternalPackageContractNames,
		RuleInternalPackageMissingContract,
		RuleDepsAreInterfaces,
		RuleOutboundIONoRPC,
	}
}

// Options controls a single [Inspect] run.
//
// Per-call-site differences between forge's contract-check entry points
// (pre-codegen check vs. `forge lint --conventions`) all reduce to
// differences in these fields. Adding a new entry point should never
// require modifying the engine — it should pick the rule subset and
// excludes it needs and call [Inspect].
type Options struct {
	// Rules is the subset of rules to run. nil or empty means
	// "run all rules in [AllRules]".
	Rules []Rule

	// Excludes is the contracts.exclude list from forge.yaml — a list
	// of module-relative slash paths to skip wholesale. The two
	// canonical-shape rules consult it — internal-package-contract-names
	// (what a contract.go must declare) and internal-package-missing-contract
	// (that a component-shaped package have one at all). Both ask whether
	// forge's shape applies to the package, so "not bootstrap-managed" is a
	// real exemption from both, and honoring it in one but not the other
	// would tell an excluded package to run the very verb the exclusion says
	// it is out of. The remaining rules assert properties that hold
	// regardless of whether forge wires the package, and honoring the
	// exclude list there would silence them on exactly the packages most
	// likely to need them.
	Excludes []string

	// CodegenUnavailable reports that `forge generate` does not run in
	// this tree (no forge.yaml): a contract.go yields no bootstrap wiring,
	// no mock and no decorator. internal-package-contract-names is an
	// ERROR only because "the bootstrap template hardcodes these names and
	// a non-canonical contract produces a bootstrap that doesn't compile",
	// so with no bootstrap it reports as a WARNING that says why. The
	// other rules keep their severity: all-mock testability does not
	// depend on codegen.
	CodegenUnavailable bool
}

// CodegenUnavailableNote is appended to the remediation of a finding
// downgraded under [Options.CodegenUnavailable], so the reader sees both
// why it no longer gates and how to make it gate again.
const CodegenUnavailableNote = " (Reported as a warning: this tree has no forge.yaml, so `forge generate` never " +
	"runs and the codegen this rule protects does not exist here. The shape is still worth keeping; " +
	"`forge lint --strict` gates it.)"

// Inspect runs the configured rules over rootDir and returns findings
// in deterministic (file, line, rule) order. A missing rootDir/internal
// directory is not an error — it produces an empty result.
//
// The context is honored only at coarse granularity: between rules.
// Individual rule implementations are tree-walks that complete in
// milliseconds on real projects, so we don't propagate ctx into the
// walk loops.
func Inspect(ctx context.Context, rootDir string, opts Options) ([]forgeconv.Finding, error) {
	rules := opts.Rules
	if len(rules) == 0 {
		rules = AllRules()
	}

	var findings []forgeconv.Finding
	for _, r := range rules {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fs, err := runRule(rootDir, r, opts)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", r, err)
		}
		findings = append(findings, fs...)
	}

	// Stable ordering across rules: by file, then line, then rule.
	// Matches the per-rule sort the legacy LintXxx helpers already did,
	// so output is identical whether you ran one rule or all three.
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Rule < findings[j].Rule
	})

	return findings, nil
}

// runRule dispatches one rule against rootDir. Centralising the switch
// keeps [Inspect] free of rule-specific knowledge; new rules add a
// case here and an entry in [AllRules].
func runRule(rootDir string, r Rule, opts Options) ([]forgeconv.Finding, error) {
	switch r {
	case RuleInternalPackageContractNames:
		res, err := lintInternalContracts(rootDir, opts.Excludes)
		if err != nil {
			return nil, err
		}
		if opts.CodegenUnavailable {
			for i := range res.Findings {
				if res.Findings[i].Severity == forgeconv.SeverityError {
					res.Findings[i].Severity = forgeconv.SeverityWarning
					res.Findings[i].Remediation += CodegenUnavailableNote
				}
			}
		}
		return res.Findings, nil
	case RuleInternalPackageMissingContract:
		res, err := lintMissingContract(rootDir, opts.Excludes)
		if err != nil {
			return nil, err
		}
		return res.Findings, nil
	case RuleDepsAreInterfaces:
		res, err := lintDepsAreInterfaces(rootDir)
		if err != nil {
			return nil, err
		}
		return res.Findings, nil
	case RuleOutboundIONoRPC:
		res, err := lintOutboundIONoRPC(rootDir)
		if err != nil {
			return nil, err
		}
		return res.Findings, nil
	}
	return nil, fmt.Errorf("unknown rule %q", r)
}

// HasErrors reports whether any finding in fs has Severity == SeverityError.
// Convenience so call sites don't have to roll their own loop.
func HasErrors(fs []forgeconv.Finding) bool {
	for _, f := range fs {
		if f.Severity == forgeconv.SeverityError {
			return true
		}
	}
	return false
}

// AsResult wraps findings in a [forgeconv.Result] for callers that want
// to feed the output through the existing FormatText / combined-result
// plumbing without changing their shape.
func AsResult(fs []forgeconv.Finding) forgeconv.Result {
	return forgeconv.Result{Findings: fs}
}
