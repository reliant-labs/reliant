// File: internal/cli/lint/lint_rollout.go
//
// Warn-first rollout — a rule that joins `forge lint`, or is tightened, must
// not turn a consumer's gate red the day the consumer pins the forge that
// ships it.
//
// ── The problem ───────────────────────────────────────────────────────────
//
// A project pins a forge version and its gate runs `forge lint` under that
// pin. Every release used to ship new rules (and promote old ones from
// warning to error) at error severity, so bumping the pin was also the moment
// the project learned about, say, 258 new findings. The per-project escape
// (`lint.rules`) existed but was applied AFTER the pin went red, by whoever
// was pinning.
//
// ── The primitive ─────────────────────────────────────────────────────────
//
// Every rule lint.rules can grade declares a ruleRollout (lint_rollout_table.go):
//
//	IntroducedIn  the forge release that shipped the rule, or tightened it
//	GatesFrom     the release it starts to gate in. Empty for a rule that has
//	              always gated; required for any rule introduced after
//	              rolloutBaseline.
//	Fix           the `forge fix <codemod>` that clears it, when one exists.
//
// The project's pinned forge_version decides. A finding that would fail the
// gate, from a rule whose GatesFrom is later than the pin, is a WARNING that
// carries a note — "is new in v1.0.0 and gates from v1.0.1" — and so does
// not fail the lane, the verdict, the exit code or the --gate-json document.
// At or past GatesFrom the same finding gates, with no other change.
//
// ── What the project can say about it ─────────────────────────────────────
//
// lint.rules wins. Naming the rule (or `*`) is an explicit dial and the
// rollout steps aside: `{rule: error}` opts in to gating early, `{rule: warn}`
// keeps the finding a warning for as long as the project likes, `{rule: off}`
// drops it. The rollout only supplies the DEFAULT for a rule the project has
// not spoken about.
//
// ── What it deliberately does not do ──────────────────────────────────────
//
//   - A pin that is absent, the forge-as-its-own-first-user sentinel (0.0.0),
//     or not a version at all gates: nothing says the project is behind. So
//     does forge's own tree, which builds the forge it lints with.
//   - A pseudo-version (v0.1.47-0.<ts>-<sha>) sorts BELOW the release it leads
//     to, so a project pinned between releases is "behind" the next one. That is
//     the right reading: it has not been through a release with the rule.
//   - Only findings that would have failed the gate are softened. A warning
//     stays a warning and carries no note; a rule at or before the baseline
//     never softens.
//
// ── Passes with no rule ID of their own ───────────────────────────────────
//
// The golangci-lint build-tag passes (#1205, #1224) report golangci-lint's own
// findings (noctx, errcheck, …), whose IDs forge does not own and lint.rules
// cannot grade. The pass itself is what is new, so its findings are tagged with
// a pseudo-rule key (rolloutGolangciBuildTags) that carries the rollout and is
// the only key lint.rules reads for them; `*` never reaches another tool's
// findings.
//
// ── Adding a rule ─────────────────────────────────────────────────────────
//
// A new rule goes in gradableRules (TestEveryEmittedRuleIsGradable) and in
// lint_rollout_table.go (TestEveryGradableRuleDeclaresItsRollout). Declare
// IntroducedIn as the release it will ship in (the one NEXT_VERSION names) and
// GatesFrom as the release after that. Both tests fail otherwise, and
// TestRolloutRowsNameTheNextRelease fails a row for a release that is neither
// shipped nor NEXT_VERSION.

package lint

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// rolloutBaseline is the last release before warn-first existed. A rule
// introduced at or before it has gated since it shipped and declares no
// GatesFrom; one introduced after it must. It is a fixed point, not a moving
// "current version": it does not change when a release is cut.
const rolloutBaseline = "v0.1.46"

// rolloutGolangciBuildTags is the pseudo-rule key of the golangci-lint
// build-tag passes (golangci_build_tags.go): the key lint.rules names to
// dial them and the key their rollout is declared under.
const rolloutGolangciBuildTags = "golangci-build-tags"

// ruleRollout is one rule's declaration. See the file header.
type ruleRollout struct {
	IntroducedIn string
	GatesFrom    string
	Fix          string
}

// ruleGrace is the note a softened finding carries, in --json as data and in
// the text report as one line.
type ruleGrace struct {
	IntroducedIn string `json:"introduced_in"`
	GatesFrom    string `json:"gates_from"`
	Fix          string `json:"fix,omitempty"`
}

// note is the line the text report prints under the finding.
func (g ruleGrace) note(rule string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "warn: [%s] is new in %s and gates from %s", rule, g.IntroducedIn, g.GatesFrom)
	if g.Fix != "" {
		fmt.Fprintf(&b, "; fix with `%s`", g.Fix)
	}
	fmt.Fprintf(&b, "; gate now with lint.rules: {%s: error}", rule)
	return b.String()
}

// gradeProjectLane is the one place a lane's findings are graded for a
// project: the rollout first (what the pin makes of each rule), then
// lint.rules (what the project says about it). Every output mode renders the
// outcomes this feeds, so none can disagree.
func gradeProjectLane(cfg *config.ProjectConfig, fs []lintJSONFinding, gated bool) ([]lintJSONFinding, bool) {
	return gradeProjectLaneWith(lintRollouts, cfg, fs, gated)
}

// gradeProjectLaneWith is gradeProjectLane over an explicit rollout table, so
// the semantics are testable without depending on any real rule's dates.
func gradeProjectLaneWith(table map[string]ruleRollout, cfg *config.ProjectConfig, fs []lintJSONFinding, gated bool) ([]lintJSONFinding, bool) {
	rules := effectiveRules(cfg)
	fs, gated = applyRollout(table, cfg, rules, fs, gated)
	return gradeLane(rules, fs, gated)
}

// applyRollout softens the findings the pin has not yet been through a
// release with, and applies lint.rules to the findings tagged with a
// pseudo-rule key (gradeLane cannot: their rule ID is another tool's).
func applyRollout(table map[string]ruleRollout, cfg *config.ProjectConfig, rules suppress.RuleSeverities, fs []lintJSONFinding, gated bool) ([]lintJSONFinding, bool) {
	if len(fs) == 0 {
		return fs, gated
	}
	gradable := gradableRuleSet()
	pin, known := comparablePin(cfg)
	out := make([]lintJSONFinding, 0, len(fs))
	errorsBefore, raised, touched := false, false, false
	for _, f := range fs {
		errorsBefore = errorsBefore || f.Severity == lintSevError
		key, tagged := f.Rule, f.rollout != ""
		if tagged {
			key = f.rollout
		}

		if tagged {
			if _, exact := rules[key]; exact {
				adjusted, keep := rules.Override(finding.Finding{Rule: key, Severity: finding.Severity(f.Severity)})
				touched = true
				if !keep {
					continue
				}
				if adjusted.Severity == finding.SeverityError && f.Severity != lintSevError {
					raised = true
				}
				f.Severity = string(adjusted.Severity)
				out = append(out, f)
				continue
			}
		}

		ro, declared := table[key]
		dialed := false
		if _, exact := rules[key]; exact {
			dialed = true
		} else if _, wild := rules[suppress.Wildcard]; wild && !tagged && gradable[key] {
			dialed = true
		}
		if declared && !dialed && known && f.Severity == lintSevError && ro.GatesFrom != "" &&
			semver.Compare(pin, ro.GatesFrom) < 0 {
			f.Severity = lintSevWarning
			f.Grace = &ruleGrace{IntroducedIn: ro.IntroducedIn, GatesFrom: ro.GatesFrom, Fix: ro.Fix}
			touched = true
		}
		out = append(out, f)
	}
	if !touched {
		return fs, gated
	}
	return out, raised || (gated && (anyErrorFinding(out) || !errorsBefore))
}

// comparablePin is the project's pinned forge_version as a comparable semver,
// and whether the pin says anything about how far behind the project is. See
// "What it deliberately does not do" in the file header.
func comparablePin(cfg *config.ProjectConfig) (string, bool) {
	if cfg == nil || cfg.ModulePath == forgecompat.ModulePath {
		return "", false
	}
	pin := strings.TrimSpace(cfg.ForgeVersion)
	if pin == "" || pin == "0.0.0" {
		return "", false
	}
	if !strings.HasPrefix(pin, "v") {
		pin = "v" + pin
	}
	if !semver.IsValid(pin) || pin == "v0.0.0" {
		return "", false
	}
	return pin, true
}

// tagRollout marks the findings a pass added with the pseudo-rule key whose
// rollout covers it. A "could not run" headline is left alone: the gap policy
// grades it, by rule, after this.
func tagRollout(fs []lintJSONFinding, key string) []lintJSONFinding {
	out := make([]lintJSONFinding, len(fs))
	for i, f := range fs {
		if !isGapRule(f.Rule) {
			f.rollout = key
		}
		out[i] = f
	}
	return out
}

// mergeRolledOutFindings is mergeFindings for a pass whose findings carry a
// rollout key: a finding the default pass already reported absorbs its tagged
// twin (the tag is not part of identity), and what is new is tagged.
func mergeRolledOutFindings(base, more []lintJSONFinding, key string) []lintJSONFinding {
	return mergeFindings(base, tagRollout(withoutRollout(more, base), key))
}

// withoutRollout drops the findings of more that base already holds.
func withoutRollout(more, base []lintJSONFinding) []lintJSONFinding {
	seen := make(map[lintJSONFinding]bool, len(base))
	for _, f := range base {
		seen[f] = true
	}
	var out []lintJSONFinding
	for _, f := range more {
		if !seen[f] {
			out = append(out, f)
		}
	}
	return out
}

// gracedRule is one rule's softened findings in a run, for the verdict line.
type gracedRule struct {
	rule      string
	gatesFrom string
	n         int
}

// gracedFindings tallies the softened findings of outcomes by rule, in rule
// order.
func gracedFindings(outcomes []lintStepOutcome) []gracedRule {
	byRule := map[string]*gracedRule{}
	for _, o := range outcomes {
		if !o.ran {
			continue
		}
		for _, f := range o.findings {
			if f.Grace == nil {
				continue
			}
			key := f.rollout
			if key == "" {
				key = f.Rule
			}
			g := byRule[key]
			if g == nil {
				g = &gracedRule{rule: key, gatesFrom: f.Grace.GatesFrom}
				byRule[key] = g
			}
			g.n++
		}
	}
	out := make([]gracedRule, 0, len(byRule))
	for _, g := range byRule {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rule < out[j].rule })
	return out
}

// describeGraced renders the verdict clause: what warned only because of the
// rollout, and when it starts to gate.
func describeGraced(graced []gracedRule) string {
	total := 0
	parts := make([]string, 0, len(graced))
	for _, g := range graced {
		total += g.n
		parts = append(parts, fmt.Sprintf("%s ×%d, gates from %s", g.rule, g.n, g.gatesFrom))
	}
	return fmt.Sprintf("%s warn until they gate (%s)", plural(total, "new-rule finding"), strings.Join(parts, "; "))
}
