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
//	GatesFrom     the release it starts to gate in, projected from an explicit
//	              Grace declaration. Empty for rules with no warning window.
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
// ── A pin that jumps a rule's window ──────────────────────────────────────
//
// The pin alone cannot tell a project that went through a rule's warning
// release from one that jumped over it: v1.0.0 → v1.7.0 lands past the
// testwait rules' GatesFrom (v1.5.1) with no release in which they warned, so
// they gated on arrival (forge#2282). So the rollout also reads the pin the
// project came from — forge.yaml's previous forge_version, from git history
// (previousPin) — and a rule the project's previous pin predates (previous < IntroducedIn) warns for the
// whole of the current pin, then gates from the next one. A project that
// moves one release at a time sees exactly what it saw before: its previous
// pin is the release that introduced the rule.
//
// Nothing is written for it: lint stays read-only, and forge.yaml gains no
// key an older v1 forge's strict loader would refuse. A pin moved but not yet
// committed counts too (the working tree's value against HEAD's). Without
// history to read — no git, a shallow clone that stops at the pin bump —
// the previous pin is unknown and the pin alone decides, as it always did.
//
// ── What it deliberately does not do ──────────────────────────────────────
//
//   - A pin that is absent, the forge-as-its-own-first-user sentinel (0.0.0),
//     or not a version at all gates: nothing says the project is behind. So
//     does forge's own tree, which builds the forge it lints with.
//   - A pseudo-version reads as the release it leads to: v1.5.1-0.<ts>-<sha>
//     grades as v1.5.1, and v1.7.0-rc.1.0.<ts>-<sha> as v1.7.0 (rolloutPin).
//     Semver sorts a pseudo-version BELOW that release, and grading by that
//     order made one forge commit's verdict depend on how the pin was spelled:
//     control-plane pinned v1.5.1-0.…-685069ed7 and the testwait rules
//     (gates from v1.5.1) warned; the same commit tagged v1.6.0 gated them
//     (forge#2230). A pseudo-version with no tag behind it (v0.0.0-<ts>-<sha>)
//     keeps its semver order, below every release. One inconsistency is left,
//     and no comparison of versions can remove it: a row names the release
//     NEXT_VERSION predicted, so a commit that ships as v1.6.0 where the row
//     said v1.5.1 grades by the row.
//   - Only findings that would have failed the gate are softened. A warning
//     stays a warning and carries no note; a rule with no rollout deadline
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
// IntroducedIn as the release it ships in. The owner-approved v1.10.0 grace
// window (the cohort first declared as v1.9.0, which missed that tag)
// requires an explicit Grace marker; the table guard rejects other grace
// declarations.

package lint

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

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
	// PreviousPin and GatesAfter are set when the pin is already past
	// GatesFrom but the project came from a pin older than IntroducedIn:
	// the rule warns while forge_version is GatesAfter and gates from the
	// next pin. See "A pin that jumps a rule's window" in the file header.
	PreviousPin string `json:"previous_pin,omitempty"`
	GatesAfter  string `json:"gates_after,omitempty"`
}

// note is the line the text report prints under the finding.
func (g ruleGrace) note(rule string) string {
	var b strings.Builder
	if g.GatesAfter != "" {
		fmt.Fprintf(&b, "warn: [%s] is new in %s, after this project's previous forge pin %s, so it warns at %s and gates from the next pin",
			rule, g.IntroducedIn, g.PreviousPin, g.GatesAfter)
	} else {
		fmt.Fprintf(&b, "warn: [%s] is new in %s and gates from %s", rule, g.IntroducedIn, g.GatesFrom)
	}
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
	prev, prevKnown := "", false
	if known {
		prev, prevKnown = previousPin(cfg, pin)
	}
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
		if declared && !dialed && known && f.Severity == lintSevError && ro.GatesFrom != "" {
			if g := graceFor(ro, pin, prev, prevKnown, cfg.ForgeVersion); g != nil {
				f.Severity = lintSevWarning
				f.Grace = g
				touched = true
			}
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
	return rolloutPin(cfg.ForgeVersion)
}

// rolloutPin is a forge_version pin as the release the rollout grades it as,
// and whether it says anything about how far behind the project is: a
// pseudo-version reads as the release it leads to (see the file header).
func rolloutPin(raw string) (string, bool) {
	pin := strings.TrimSpace(raw)
	if pin == "" || pin == "0.0.0" {
		return "", false
	}
	if !strings.HasPrefix(pin, "v") {
		pin = "v" + pin
	}
	if !semver.IsValid(pin) || pin == "v0.0.0" {
		return "", false
	}
	if module.IsPseudoVersion(pin) {
		if base, err := module.PseudoVersionBase(pin); err == nil && base != "" {
			canon := semver.Canonical(pin)
			pin = strings.TrimSuffix(canon, semver.Prerelease(canon))
		}
	}
	return pin, true
}

// graceFor is the note a gating finding of a rule declared ro carries under
// pin, or nil when the finding gates. prev is the pin the project came from
// (prevKnown false when forge never saw an earlier one); raw is the pin as
// forge.yaml spells it, for the note.
func graceFor(ro ruleRollout, pin, prev string, prevKnown bool, raw string) *ruleGrace {
	g := &ruleGrace{IntroducedIn: ro.IntroducedIn, GatesFrom: ro.GatesFrom, Fix: ro.Fix}
	if semver.Compare(pin, ro.GatesFrom) < 0 {
		return g
	}
	if prevKnown && semver.Compare(prev, ro.IntroducedIn) < 0 {
		g.PreviousPin, g.GatesAfter = prev, strings.TrimSpace(raw)
		return g
	}
	return nil
}

// previousPin is the pin the project came from, as the rollout grades it:
// the forge_version forge.yaml held before it held pin, read from git history
// (previousPinFromHistory). ok=false when history cannot say — no git, a
// shallow clone that stops at the pin bump, a pin never changed, a previous
// value that is not a version — and the pin alone then decides, as it always
// did.
func previousPin(cfg *config.ProjectConfig, pin string) (string, bool) {
	if cfg == nil {
		return "", false
	}
	return previousPinFromHistory(cfg.ProjectDir(), pin)
}

// previousPinFromHistory is gitPreviousPin; a variable so the grading tests
// need no git repository.
var previousPinFromHistory = gitPreviousPin

// gitPreviousPin reads, newest first, the forge_version values forge.yaml in
// projectDir has held — the working tree's, then each commit that changed
// the line (`git log -G`) — and returns the first that is a different
// release from pin.
//
// History, not a record forge writes: anything written into forge.yaml is a
// key an older v1 forge's strict loader refuses, and a separate file would be
// one more piece of state to commit. The history is already committed. A
// clone too shallow to reach the pin bump (actions/checkout's default depth
// of 1) grades by the pin alone, which is never stricter than before.
func gitPreviousPin(projectDir, pin string) (string, bool) {
	if projectDir == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", projectDir, "log", "-n", "50",
		"--format=%x00commit", "-p", "--unified=0", "-G", "^forge_version:", "--", "forge.yaml").Output()
	if err != nil {
		return "", false
	}
	// Per commit the diff lists the removed value before the added one;
	// newest first, the added value is the later state, so it is read
	// first.
	for _, chunk := range strings.Split(string(out), "\x00commit") {
		var added, removed string
		for _, line := range strings.Split(chunk, "\n") {
			switch {
			case strings.HasPrefix(line, "+forge_version:"):
				added = pinValue(strings.TrimPrefix(line, "+forge_version:"))
			case strings.HasPrefix(line, "-forge_version:"):
				removed = pinValue(strings.TrimPrefix(line, "-forge_version:"))
			}
		}
		for _, v := range []string{added, removed} {
			if v == "" {
				continue
			}
			prev, ok := rolloutPin(v)
			if !ok {
				return "", false
			}
			if prev != pin {
				return prev, true
			}
		}
	}
	return "", false
}

// pinValue is a forge_version line's value, without quotes or a trailing
// comment.
func pinValue(raw string) string {
	v := strings.TrimSpace(raw)
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return strings.Trim(v, `"'`)
}

// GatingRule is one rule whose warn-first grace a pin bump ends: it warned
// under the old pin and gates under the new one.
type GatingRule struct {
	Rule         string
	IntroducedIn string
	GatesFrom    string
	Fix          string
}

// RulesStartingToGate lists, by rule ID, the rules that warn under the pin
// from and gate under the pin to, for the project cfg describes. A rule the
// project's lint.rules names (or `*` reaches) is left out: lint.rules, not
// the pin, decides its severity. Nothing starts gating when either pin says
// nothing about the project's place (unpinned, not a version, forge's own
// tree), because such a pin already gates every rule or does not move.
func RulesStartingToGate(cfg *config.ProjectConfig, from, to string) []GatingRule {
	return rulesStartingToGate(lintRollouts, cfg, from, to)
}

func rulesStartingToGate(table map[string]ruleRollout, cfg *config.ProjectConfig, from, to string) []GatingRule {
	if cfg != nil && cfg.ModulePath == forgecompat.ModulePath {
		return nil
	}
	oldPin, okOld := rolloutPin(from)
	newPin, okNew := rolloutPin(to)
	if !okOld || !okNew {
		return nil
	}
	rules := effectiveRules(cfg)
	_, wild := rules[suppress.Wildcard]
	gradable := gradableRuleSet()
	// Before the move the project came from whatever history says; after
	// it, from the old pin.
	oldPrev, oldPrevKnown := previousPin(cfg, oldPin)
	// After the move lint reads the previous pin the same way: the first
	// value in forge.yaml's history that grades differently from the new
	// pin. That is the old pin, unless both grade as one release (two
	// pseudo-versions leading to v1.7.1), and then it is whatever the old
	// pin's own previous was. Using the old pin there regardless reported
	// the same rules as starting to gate on two consecutive bumps, while
	// lint under the first of them still warned (upgrade-report defect 4).
	newPrev, newPrevKnown := oldPin, true
	if oldPin == newPin {
		newPrev, newPrevKnown = oldPrev, oldPrevKnown
	}
	var out []GatingRule
	for id, ro := range table {
		if ro.GatesFrom == "" {
			continue
		}
		if _, exact := rules[id]; exact || (wild && gradable[id]) {
			continue
		}
		warnedBefore := graceFor(ro, oldPin, oldPrev, oldPrevKnown, from) != nil
		gatesAfter := graceFor(ro, newPin, newPrev, newPrevKnown, to) == nil
		if warnedBefore && gatesAfter {
			out = append(out, GatingRule{Rule: id, IntroducedIn: ro.IntroducedIn, GatesFrom: ro.GatesFrom, Fix: ro.Fix})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
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

// GatesWhen is when a softened finding's rule gates, worded once for every
// surface that says it — lint's verdict line and `forge project upgrade`'s
// "Gating soon" — so the two cannot disagree (they did: "gates from v1.8.1"
// beside "gates from the pin after v1.8.1-0.…"). gatesAfter is set when the
// pin jumped the rule's window (ruleGrace.GatesAfter): the rule then gates
// from the pin after that one, whatever its GatesFrom says.
func GatesWhen(gatesFrom, gatesAfter string) string {
	if gatesAfter != "" {
		return "gates from the next forge pin after " + gatesAfter
	}
	return "gates from " + gatesFrom
}

// isRawToolLine reports whether f is a raw sub-tool line — rule "external"
// with no file — the output under the finding before it, not a finding.
func isRawToolLine(f lintJSONFinding) bool {
	return f.Rule == "external" && f.File == ""
}

// gracedRule is one rule's softened findings in a run, for the verdict line.
type gracedRule struct {
	rule string
	// gatesFrom says when it gates ("gates from v1.0.1").
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
			// A raw sub-tool line (a formatter's diff under the finding it
			// belongs to) is that finding's output, not a finding: counting
			// it doubled forge-proto-format (60 for 30 files).
			if f.Grace == nil || isRawToolLine(f) {
				continue
			}
			key := f.rollout
			if key == "" {
				key = f.Rule
			}
			g := byRule[key]
			if g == nil {
				g = &gracedRule{rule: key, gatesFrom: GatesWhen(f.Grace.GatesFrom, f.Grace.GatesAfter)}
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
		parts = append(parts, fmt.Sprintf("%s ×%d, %s", g.rule, g.n, g.gatesFrom))
	}
	return fmt.Sprintf("%s warn until they gate (%s)", plural(total, "new-rule finding"), strings.Join(parts, "; "))
}
