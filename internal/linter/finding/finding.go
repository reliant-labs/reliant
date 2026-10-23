// Package finding is the single canonical home for the lint-finding
// vocabulary shared by every internal linter (forgeconv, scaffolds,
// migrationlint).
//
// Before this package existed, each linter re-declared its own
// Severity/Finding/Result trio, and — worse — the Severity enum values
// DISAGREED across packages: some linters spelled their warning level
// "warn" while others spelled it "warning". That single inconsistency
// forced `forge lint --json` to carry a normalizeLintSeverity() shim
// whose only job was to collapse "warn" onto "warning" before emitting
// the JSON contract.
//
// Canonicalizing here fixes the spelling at the source: there is exactly
// ONE Severity type with ONE spelling per level ("error" / "warning" /
// "info"), matching the `forge lint --json` output contract documented
// in internal/cli/lint_json.go. The Finding struct is a SUPERSET of the
// fields the four linters need, so each linter can keep emitting the
// fields it cares about (and leaving the rest zero) while sharing one
// type. FindingsToJSON (in the cli package) is then a single mapper over
// this one type instead of four near-identical copies.
//
//forge:exclude-contract: pure data vocabulary (Severity/Finding/Result) shared by every internal linter
package finding

import "strings"

// Severity classifies a Finding. Errors fail `forge lint` (they gate the
// build); warnings and infos are surfaced but never gate. There is
// exactly one spelling per level — this is the whole point of the
// package. The values match the `forge lint --json` severity contract.
type Severity string

// Severity enum values — the canonical, single-spelling set.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Finding is the superset lint diagnostic shared by every internal
// linter. Each linter populates the subset of fields it produces:
//
//   - forgeconv:     Rule, Severity, File, Line, Message, Remediation
//   - scaffolds:     Rule, Severity, Path, Message
//   - migrationlint: Rule, Severity, File, Line, Message
//
// The json tags preserve the historical per-package wire shapes (Path
// and Remediation/omitempty etc.) so any consumer that ever marshals a
// Finding directly keeps the same output. The authoritative machine
// contract, however, is the projection done by FindingsToJSON in the cli
// package — see internal/cli/lint_json.go.
type Finding struct {
	Rule        string   `json:"rule"`
	Severity    Severity `json:"severity"`
	File        string   `json:"file,omitempty"`
	Line        int      `json:"line,omitempty"` // 1-indexed; 0 if file-level/unknown
	Message     string   `json:"message"`
	Path        string   `json:"path,omitempty"`        // scaffolds: file path (no line)
	Remediation string   `json:"remediation,omitempty"` // forgeconv: actionable fix hint
}

// Result aggregates findings from a single lint run. Each linter embeds
// this (or aliases it) and hangs its own FormatText on top — the human
// rendering differs per linter, but the finding vocabulary is shared.
type Result struct {
	Findings []Finding `json:"findings"`
}

// HasErrors reports whether any finding is at error severity. `forge
// lint` uses this to decide exit status.
func (r Result) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ParseSeverity maps a free-form severity string (as it appears in
// forge.yaml rule config, e.g. migration_safety levels) onto the
// canonical Severity. Input is deliberately lenient: forge.yaml spells
// the warning level "warn" — that is the spelling forge itself writes
// into generated config, documents in the MigrationSafetyConfig yaml
// tags, and normalizes "warning" onto in config.effectiveSeverity — so
// both spellings parse to SeverityWarning. The single-spelling
// invariant is a property of the Severity type and the `forge lint
// --json` contract (the OUTPUT), not of what this parser accepts.
// Anything unrecognized — including "off" — returns ("", false) so
// callers can treat the rule as disabled.
func ParseSeverity(value string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "error":
		return SeverityError, true
	case "warn", "warning":
		return SeverityWarning, true
	case "info":
		return SeverityInfo, true
	default:
		return "", false
	}
}
