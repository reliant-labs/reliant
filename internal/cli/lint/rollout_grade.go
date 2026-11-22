package lint

// Grading a rule that is not a `forge lint` finding.
//
// Some rules forge rolls out warn-first judge something other than source: a
// promotion (the vuln_gate and provenance_sbom refusals), or a `forge ci`
// gate step (vuln-sbom). They have no finding for applyRollout to soften, but
// they owe a project the same promise every lint rule does — they must not
// refuse anything the day the project pins the forge that ships them — and
// the project dials them with the same vocabulary (`lint.rules: {vuln-gate:
// error}` opts in early, `{vuln-gate: warn}` extends the grace period).
//
// GradeRule is that promise for such a rule: the rule's row in
// lint_rollout_table.go, the project's pin (and the pin it came from), then
// lint.rules, read exactly as applyRollout reads them for a finding.

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// The rules that grade a verdict outside `forge lint`. Their rollout rows are
// in lint_rollout_table.go; lint.rules accepts them like any other rule.
const (
	// RuleVulnGate grades an env's `vuln_gate = enforce`: while it warns,
	// enforce is judged as audit (it records "would block" and refuses
	// nothing), at the cut, at promote, and in the vuln-sbom gate step.
	RuleVulnGate = "vuln-gate"
	// RuleSBOMRequired grades a declared `provenance_sbom = enforce` (the
	// schema default is audit): a promotion whose artifacts have no SBOM.
	RuleSBOMRequired = "sbom-required"
)

// RuleGrade is what a project makes of one rule.
type RuleGrade struct {
	Rule string
	// Gates reports that the rule's failing verdict refuses (or fails the
	// gate step). False means it is recorded and warned, never enforced.
	Gates bool
	// IntroducedIn and GatesFrom are the rule's rollout row.
	IntroducedIn string
	GatesFrom    string
	// GatesAfter is set when the project's pin jumped the rule's window
	// (ruleGrace.GatesAfter): it warns at this pin and gates from the next.
	GatesAfter string
	// Dial is the lint.rules value that decided ("error", "warn", "off"),
	// "" when the rollout did.
	Dial string
}

// GradeRule grades rule for the project cfg describes. A nil cfg (no
// forge.yaml) gates: nothing says the project is behind.
func GradeRule(cfg *config.ProjectConfig, rule string) RuleGrade {
	return gradeRuleWith(lintRollouts, cfg, rule)
}

func gradeRuleWith(table map[string]ruleRollout, cfg *config.ProjectConfig, rule string) RuleGrade {
	ro := table[rule]
	g := RuleGrade{Rule: rule, IntroducedIn: ro.IntroducedIn, GatesFrom: ro.GatesFrom, Gates: true}
	rules := effectiveRules(cfg)
	spec, dialed := rules[rule]
	if !dialed {
		spec, dialed = rules[suppress.Wildcard]
	}
	if dialed {
		spec = strings.ToLower(strings.TrimSpace(spec))
		if spec == suppress.SeverityOff {
			g.Gates, g.Dial = false, spec
			return g
		}
		// An unparseable value is ignored, the fail-safe direction
		// RuleSeverities.Override takes for a finding: the rollout decides.
		if sev, ok := finding.ParseSeverity(spec); ok {
			g.Gates, g.Dial = sev == finding.SeverityError, spec
			return g
		}
	}
	if ro.GatesFrom == "" {
		return g
	}
	pin, known := comparablePin(cfg)
	if !known {
		return g
	}
	prev, prevKnown := previousPin(cfg, pin)
	if grace := graceFor(ro, pin, prev, prevKnown, cfg.ForgeVersion); grace != nil {
		g.Gates, g.GatesAfter = false, grace.GatesAfter
	}
	return g
}

// Note says why a rule that does not gate does not: "new in v1.10.0; gates
// from v1.11.0", or the lint.rules dial that lowered it. "" when it gates.
func (g RuleGrade) Note() string {
	switch {
	case g.Gates:
		return ""
	case g.Dial != "":
		return fmt.Sprintf("lint.rules sets %s to %s", g.Rule, g.Dial)
	case g.GatesAfter != "":
		return fmt.Sprintf("new in %s; %s", g.IntroducedIn, GatesWhen(g.GatesFrom, g.GatesAfter))
	default:
		return fmt.Sprintf("new in %s; gates from %s", g.IntroducedIn, g.GatesFrom)
	}
}

// OptIn is the lint.rules line that makes the rule gate now; "" when it
// already gates.
func (g RuleGrade) OptIn() string {
	if g.Gates {
		return ""
	}
	return fmt.Sprintf("lint.rules: {%s: error}", g.Rule)
}

// Predates reports whether forgeVersion — the forge that cut a release — is a
// release older than the one that introduced the rule. A release such a
// forge cut could not carry what the rule asks for (an SBOM it never
// generated), so its ABSENCE is never refused: a rule forge did not know
// does not block what that forge made. An unknown or unparseable version
// (a development build) does not predate anything.
func (g RuleGrade) Predates(forgeVersion string) bool {
	v, ok := rolloutPin(forgeVersion)
	if !ok || g.IntroducedIn == "" {
		return false
	}
	return semver.Compare(v, g.IntroducedIn) < 0
}

// ForgeRelease is a forge version as the release the rollout grades it as
// (a pseudo-version reads as the release it leads to), and whether it names
// a release at all: "", 0.0.0 and a development build do not.
func ForgeRelease(v string) (string, bool) { return rolloutPin(v) }
