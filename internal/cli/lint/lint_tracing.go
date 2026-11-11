// File: internal/cli/lint/lint_tracing.go
//
// The tracing lane: `forge lint --tracing`, and the same rules in the full
// sweep. The rules live in internal/linter/tracinglint; this file wires them
// to forge.yaml (lint.tracing.log_wrappers, lint.rules), to the suppression
// directives, and to forge's own instrument decision for contract packages.
//
// Autofix. forge-tracing-log-without-ctx and forge-tracing-contract-
// uninstrumented have mechanical fixes, applied only under --fix (lint is a
// gate and reports by default):
//
//   - by `forge lint --tracing --fix` at any severity but off — naming the
//     lane is the request to migrate;
//   - by the full sweep (`forge lint --fix`) only where the rule is at error,
//     so pinning a forge that adds the rule to a repo that dialled it to warn
//     rewrites nothing until that repo runs the migration itself.
//
// A finding a suppression directive covers is never rewritten.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/internal/linter/tracinglint"
)

// tracingRun is one evaluation of the tracing rules.
type tracingRun struct {
	findings []finding.Finding
	fixed    []tracinglint.Fixed
}

// tracingFindings runs the rules over the project at cwd. fix enables the
// autofix; explicit says the lane was named (`--tracing`), which fixes at
// warn too.
func tracingFindings(cwd string, cfg *config.ProjectConfig, fix, explicit bool) (tracingRun, error) {
	var lintCfg config.LintConfig
	contracts, libraryOnly := true, false
	if cfg != nil {
		lintCfg = cfg.Lint
		contracts = cfg.Features.ContractsEnabled()
		// No observability: the project's own processes record no trace
		// (a CLI, a library), so only its importable code can cut one.
		libraryOnly = !cfg.Features.ObservabilityEnabled()
	}
	rules := lintCfg.EffectiveRules()

	wrappers := make([]tracinglint.LogWrapper, 0, len(lintCfg.Tracing.LogWrappers))
	for i, w := range lintCfg.Tracing.LogWrappers {
		lw := tracinglint.LogWrapper{Call: w.Call, ContextCall: w.ContextCall}
		if err := tracinglint.CheckLogWrapper(lw); err != nil {
			return tracingRun{}, fmt.Errorf("forge.yaml lint.tracing.log_wrappers[%d]: %w", i, err)
		}
		wrappers = append(wrappers, lw)
	}

	opts := tracinglint.Options{
		LogWrappers: wrappers,
		LibraryOnly: libraryOnly,
		Fix:         fix,
		FixFilter: func(f finding.Finding) bool {
			adjusted, keep := rules.Override(f)
			if !keep || (!explicit && adjusted.Severity != finding.SeverityError) {
				return false
			}
			content, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(f.File)))
			if err != nil {
				return false
			}
			return len(suppress.Apply(string(content), []finding.Finding{f}).Kept) == 1
		},
	}
	if contracts {
		opts.Instrumented = instrumentedComponent
	}
	// The log rewrite adds and drops imports, so it lands through a goFixSet
	// and is verified: a rewrite that leaves its package failing to
	// type-check is taken back (lint_fix_guard.go).
	var set *goFixSet
	if fix {
		set = newGoFixSet(cwd)
		opts.Write = set.write
	}
	res, err := tracinglint.Lint(cwd, opts)
	if err != nil {
		return tracingRun{}, fmt.Errorf("tracing lint: %w", err)
	}
	if set != nil {
		if reverted := set.settle(fixTypecheck); len(reverted) > 0 {
			printRevertedGoFixes(os.Stdout, "tracing lint", reverted)
			// Re-read the tree: the reverted sites are findings again.
			opts.Fix, opts.Write = false, nil
			fixed := res.Fixed[:0:0]
			for _, f := range res.Fixed {
				if set.applied(filepath.Join(cwd, filepath.FromSlash(f.File))) {
					fixed = append(fixed, f)
				}
			}
			if res, err = tracinglint.Lint(cwd, opts); err != nil {
				return tracingRun{}, fmt.Errorf("tracing lint: %w", err)
			}
			res.Fixed = fixed
		}
	}
	// lint.rules grades the findings in the outcome engine (lint_rules.go);
	// here it only decides which ones --fix rewrites.
	applied := suppress.ApplyFiles(cwd, res.Findings, os.ReadFile)
	kept := append(slices.Clone(applied.Kept), applied.Violations...)
	return tracingRun{findings: kept, fixed: res.Fixed}, nil
}

// instrumentedComponent is forge generate's instrument decision for the
// package in dir: the same function the compose assembler consults, so the
// lint and the generated wiring cannot disagree.
func instrumentedComponent(dir string) bool {
	ctorType, err := codegen.DetectConstructorType(dir)
	if err != nil || ctorType == "" {
		return false
	}
	iface := codegen.DetectServiceInterfaceName(dir)
	if iface == "" {
		iface = "Service"
	}
	return codegen.ShouldInstrumentComponent(dir, ctorType, iface)
}

// describeTracingFix says what a rewrite did to one file, in the words of
// the rule that made it: a fix run renames constructions as well as
// rewriting log calls, and a report that calls both "log calls" misleads.
func describeTracingFix(f tracinglint.Fixed) string {
	switch f.Rule {
	case tracinglint.RuleContractUninstrumented:
		return fmt.Sprintf("%d contract construction(s) renamed to match the package's decorator (New ↔ NewInstrumented)", f.Count)
	case tracinglint.RuleLogWithoutCtx:
		return fmt.Sprintf("%d log call(s) rewritten to pass the request context", f.Count)
	default:
		return fmt.Sprintf("%d %s finding(s) rewritten", f.Count, f.Rule)
	}
}

// tracingAutoFixable reports whether `forge lint --tracing --fix` rewrites a
// finding of rule itself.
func tracingAutoFixable(rule string) bool {
	return rule == tracinglint.RuleLogWithoutCtx || rule == tracinglint.RuleContractUninstrumented
}

// collectTracingJSON is the lane. Under --fix the full sweep rewrites the
// findings whose rule is at error, and a named run (`forge lint --tracing
// --fix`, the migration) its warnings too; each rewrite is reported as it
// lands. --json refuses --fix, so it is detect-only. Error findings gate.
func collectTracingJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	run, err := tracingFindings(rc.cwd, rc.cfg, rc.fix, rc.targeted)
	if err != nil {
		return nil, false, err
	}
	for _, f := range run.fixed {
		fmt.Printf("  ✎ %s: %s\n", f.File, describeTracingFix(f))
	}
	out := findingsToJSON(run.findings)
	if !rc.targeted {
		for i, f := range out {
			if f.Severity != lintSevError && tracingAutoFixable(f.Rule) && out[i].FixHint == "" {
				out[i].FixHint = "`forge lint --tracing --fix` rewrites it"
			}
		}
	}
	return out, anyErrorFinding(out), nil
}

// hasGoModule reports whether the tracing lane has Go source to judge.
func hasGoModule(cwd string) bool {
	_, err := os.Stat(filepath.Join(cwd, "go.mod"))
	return err == nil
}
