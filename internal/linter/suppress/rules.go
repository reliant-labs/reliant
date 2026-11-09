// File: internal/linter/suppress/rules.go
//
// The repo-wide half of suppression: the `lint.rules` map in forge.yaml.
//
// This is the coarsest of the three levels and the one to reach for
// last. It exists because some rules genuinely do not apply to a whole
// project — a CLI has no handlers, an operator has no frontend — and
// making those projects annotate every file would be worse than letting
// them say so once.
//
// The severity vocabulary is `off` / `warn` / `error`, matching
// FrontendLintConfig's existing dial (lint.frontend.typecheck,
// no_important, no_inline_styles) rather than inventing a second
// spelling for the same idea. A rule set to `warn` still reports; it
// just stops gating. That middle setting is the important one: it is
// what lets a project adopt a new opinionated rule incrementally
// instead of choosing between "fails the build today" and "off".

//forge:exclude-contract: pure data/policy package — RuleSeverities is a map read from forge.yaml and its methods are ON that value; nothing to fake
package suppress

import (
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// RuleSeverities is the parsed `lint.rules` map: rule ID → configured
// severity. The zero value (nil map) means every rule keeps the
// severity its analyzer assigned, which is the default posture.
type RuleSeverities map[string]string

// SeverityOff is the spelling that disables a rule entirely.
const SeverityOff = "off"

// Override returns the effective severity for a finding under this
// config, and whether the finding should be reported at all.
//
// Precedence is deliberately simple: an exact rule ID wins over the
// wildcard, and anything unrecognized is ignored rather than being an
// error. A typo'd rule name in forge.yaml therefore does NOT silently
// disable a rule — it does nothing, and the rule keeps firing. That is
// the safe direction to fail: the alternative (treating unknown keys as
// meaningful) is how a misspelling turns into an invisible hole in the
// gate. `forge lint` also reports every key it cannot honour as an error
// (internal/cli/lint/lint_rules.go), so the typo is named, not merely
// ignored.
func (rs RuleSeverities) Override(f finding.Finding) (finding.Finding, bool) {
	if len(rs) == 0 {
		return f, true
	}
	spec, ok := rs[strings.ToLower(f.Rule)]
	if !ok {
		spec, ok = rs[Wildcard]
	}
	if !ok {
		return f, true
	}
	if strings.EqualFold(strings.TrimSpace(spec), SeverityOff) {
		return f, false
	}
	sev, valid := finding.ParseSeverity(spec)
	if !valid {
		// Unparseable severity: keep the analyzer's own. Same
		// fail-safe direction as an unknown rule ID.
		return f, true
	}
	f.Severity = sev
	return f, true
}

// ApplyAll runs the repo-wide rule config over a batch of findings.
func (rs RuleSeverities) ApplyAll(findings []finding.Finding) []finding.Finding {
	if len(rs) == 0 {
		return findings
	}
	out := make([]finding.Finding, 0, len(findings))
	for _, f := range findings {
		adjusted, keep := rs.Override(f)
		if !keep {
			continue
		}
		out = append(out, adjusted)
	}
	return out
}

// ValidSeverity reports whether a `lint.rules` value is one forge
// understands. Used by config validation so a misspelled severity is
// reported at load time rather than silently ignored at lint time.
func ValidSeverity(spec string) bool {
	if strings.EqualFold(strings.TrimSpace(spec), SeverityOff) {
		return true
	}
	_, ok := finding.ParseSeverity(spec)
	return ok
}
