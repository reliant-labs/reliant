// File: internal/cli/lint/lint_graph.go
//
// graph — `forge lint --graph` (composition graph PR 5). The whole-graph
// checks generate cannot make one node at a time: every component and
// provider reachable from some root, and every listener's services in the
// audience of its chain's authenticator. The engine is
// codegen.LintGraph, which judges the graph exactly as `forge generate`
// assembles it; this file renders it. Errors gate: in graph mode they are
// what the boot-time RequireComplete gate used to catch.
//
// Severity is the analyzer's, re-graded by forge.yaml's `lint.rules` — the
// same dial every forge rule answers to — so a project adopting roots one at
// a time sets `lint.rules: {forge-graph-unreachable: warn}` until the last
// root is declared, then deletes the line.

package lint

import (
	"fmt"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// graphFindings judges the graph. lint.rules grades its findings in the
// outcome engine, as it does every lane's (lint_rules.go).
func graphFindings(projectDir string, _ *config.ProjectConfig) ([]codegen.GraphFinding, bool, error) {
	return codegen.LintGraph(projectDir)
}

// collectGraphJSON maps graph findings onto the JSON contract; an error
// finding gates.
func collectGraphJSON(projectDir string, cfg *config.ProjectConfig) ([]lintJSONFinding, error) {
	findings, _, err := graphFindings(projectDir, cfg)
	if err != nil {
		return nil, fmt.Errorf("graph lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		sev := lintSevWarning
		if f.Error {
			sev = lintSevError
		}
		out = append(out, lintJSONFinding{File: f.File, Line: f.Line, Severity: sev, Rule: f.Rule, Message: f.Message, FixHint: f.Fix})
	}
	return out, nil
}
