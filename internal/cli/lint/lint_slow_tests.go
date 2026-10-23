// File: internal/cli/lint/lint_slow_tests.go
//
// slow-tests — `forge lint --slow-tests`.
//
// The static half of the fast/slow test tiers (forge/pkg/testkit.Slow): a
// test that execs a toolchain (go, forge, kcl, node, npm, git, docker, …),
// drives containers or dials a real external host must be marked slow, so it
// leaves the blocking `go test -short` tier. The analyzer, its reach model
// and what it deliberately does not flag live in internal/linter/slowtest.
//
// It runs in-process over the same package load the contract lane uses, test
// variants included. A finding is silenced with
// `// forge:lint-disable-next-line forge-slow-test-unmarked: <why>` above the
// test, like every other forge lint rule.
//
// GATES: an unmarked slow test is the defect the tiers exist to prevent — a
// blocking tier that is slow, or a cached pass that is stale.

package lint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"

	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/slowtest"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// slowTestsFixHint is the remediation every unmarked-slow finding carries.
const slowTestsFixHint = "call testkit.Slow(t, \"<what makes it slow>\") first (forge/pkg/testkit; " +
	"add testkit.Inputs(\"<glob>\", …) to name the files it depends on), or guard the slow work with " +
	"`if testing.Short() { t.Skip(\"<why>\") }` — it then runs in the full tier (go test without -short)"

// runSlowTestAnalysis loads paths and runs the slowtest analyzer in-process,
// returning position-sorted findings with suppression directives applied.
func runSlowTestAnalysis(ctx context.Context, paths []string) ([]finding.Finding, error) {
	pkgs, err := loadPackagesForAnalysis(ctx, paths)
	if err != nil {
		return nil, err
	}
	graph, err := checker.Analyze([]*analysis.Analyzer{slowtest.Analyzer}, pkgs, nil)
	if err != nil {
		return nil, fmt.Errorf("slow-test analysis: %w", err)
	}

	cwd, _ := os.Getwd()
	seen := map[string]bool{}
	var findings []finding.Finding
	for act := range graph.All() {
		if !act.IsRoot || act.Analyzer != slowtest.Analyzer {
			continue
		}
		if act.Err != nil {
			return nil, fmt.Errorf("%s on %s: %w", act.Analyzer.Name, act.Package.PkgPath, act.Err)
		}
		for _, d := range act.Diagnostics {
			pos := act.Package.Fset.Position(d.Pos)
			file := pos.Filename
			if cwd != "" {
				if rel, rerr := filepath.Rel(cwd, file); rerr == nil && !strings.HasPrefix(rel, "..") {
					file = filepath.ToSlash(rel)
				}
			}
			// Tests:true loads a package both plain and as its test
			// variant; the test files' diagnostics come from the latter
			// only, but dedupe anyway so a future loader change cannot
			// double-report.
			key := fmt.Sprintf("%s|%d|%d|%s", file, pos.Line, pos.Column, d.Message)
			if seen[key] {
				continue
			}
			seen[key] = true
			hint := slowTestsFixHint
			if d.Category == slowtest.RuleInputs {
				hint = "write the globs as string literals in the call: testkit.Slow(t, \"<why>\", testkit.Inputs(\"deploy/**\"))"
			}
			findings = append(findings, finding.Finding{
				Rule:        d.Category,
				Severity:    finding.SeverityError,
				File:        file,
				Line:        pos.Line,
				Message:     d.Message,
				Remediation: hint,
			})
		}
	}

	applied := suppress.ApplyFiles(cwd, findings, os.ReadFile)
	out := make([]finding.Finding, 0, len(applied.Kept)+len(applied.Violations))
	out = append(out, applied.Kept...)
	out = append(out, applied.Violations...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// collectSlowTestsJSON is the structured collector (--json, --quiet, --scope).
func collectSlowTestsJSON(ctx context.Context, paths []string) ([]lintJSONFinding, bool, error) {
	fs, err := runSlowTestAnalysis(ctx, paths)
	if err != nil {
		return nil, false, err
	}
	out := make([]lintJSONFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: string(f.Severity),
			Rule:     f.Rule,
			Message:  f.Message,
			FixHint:  f.Remediation,
		})
	}
	return out, anyErrorFinding(out), nil
}
