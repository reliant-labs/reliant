package lint

import (
	"github.com/reliant-labs/forge/internal/commitpolicy"
)

// The commit-policy step: generated code a compiler needs must be committed,
// and neither tool-derived outputs (gen/forge_descriptor/) nor machine-local
// materializations (.forge-kcl/, a frontend's dev public/config.js) may be. See internal/commitpolicy for why this is a
// gating check rather than a template default — a project keeps the
// .gitignore it was scaffolded with, and `forge ci verify-generated` cannot
// see an ignored file.

// collectCommitPolicyJSON is the --json arm. Every violation gates.
func collectCommitPolicyJSON(root string) ([]lintJSONFinding, bool, error) {
	vs, err := commitpolicy.Check(root)
	if err != nil {
		return nil, false, err
	}
	out := make([]lintJSONFinding, 0, len(vs))
	for _, v := range vs {
		out = append(out, lintJSONFinding{
			Rule:     v.Rule,
			Severity: "error",
			File:     v.Path,
			Message:  v.Why,
			FixHint:  v.Fix,
		})
	}
	return out, len(out) > 0, nil
}
