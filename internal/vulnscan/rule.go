package vulnscan

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The vuln_gate rule (release.ShapeVulnGate), as pure functions. Nothing here
// refuses anything or grades a rollout: [Judge] says what the rule makes of
// one finding, [Evaluate] what it makes of a release's findings, and the
// caller decides what that verdict does.
//
// The rule, unchanged from the schema:
//
//   - An advisory on CISA's KEV list blocks (block_kev).
//   - A finding at or above block_severity blocks — with fixable_only, only
//     when a fixed version exists (grype's fix.state "fixed"; "not-fixed"
//     and "wont-fix" are reported, never gating).
//   - A finding that does not block, at or above warn_severity, warns.
//   - A finding with no severity counts as critical (fail closed).
//   - An unexpired exemption whose id is the finding's id or one of its
//     aliases takes the finding out of the gate: a CVE exemption covers the
//     GHSA of the same advisory. A scoped exemption (packages, artifacts,
//     locations) covers only the occurrences its scope names, and takes the
//     finding out only when every occurrence is covered by some exemption:
//     anywhere it is not, the finding still counts.

// Action is what the rule makes of one finding.
type Action string

// The actions, strongest first.
const (
	ActionBlock Action = "block"
	ActionWarn  Action = "warn"
	ActionNone  Action = "none"
)

// Exemption is an accepted advisory, as the env declares it
// (forge.VulnExemption): time-boxed, with its reason, and the scope it
// covers the advisory in (none: everywhere).
type Exemption struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
	// Packages, Artifacts and Locations are the exemption's scope
	// (release.ShapeVulnScopedExempt), absent when it covers the advisory
	// everywhere.
	Packages  []string `json:"packages,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	Locations []string `json:"locations,omitempty"`
}

// identity is the declaration the exemption is, as
// release.ShapeVulnScopedExempt.Identity: two exemptions of one advisory with
// different scopes are two.
func (e Exemption) identity() string {
	return release.ShapeVulnScopedExempt{ID: e.ID, Packages: e.Packages, Artifacts: e.Artifacts, Locations: e.Locations}.Identity()
}

// exemptionDateLayout is how an exemption's expiry is written.
const exemptionDateLayout = "2006-01-02"

// Expired reports whether the exemption no longer applies on now's UTC date.
// An exemption is good THROUGH its expiry date. A date that does not parse
// is expired: an exemption nobody can date exempts nothing.
func (e Exemption) Expired(now time.Time) bool {
	exp, err := time.Parse(exemptionDateLayout, e.Expires)
	if err != nil {
		return true
	}
	today, _ := time.Parse(exemptionDateLayout, now.UTC().Format(exemptionDateLayout))
	return today.After(exp)
}

// Occurrences are every place f was found: one per location of its package
// in each artifact it is in. A finding whose source names no artifact, or no
// location, has occurrences that leave them empty, which no exemption scoped
// to an artifact or a location covers.
func occurrencesOf(f Finding, artifacts []string) []release.VulnOccurrence {
	if len(artifacts) == 0 {
		artifacts = []string{""}
	}
	locs := f.Locations
	if len(locs) == 0 {
		locs = []string{""}
	}
	out := make([]release.VulnOccurrence, 0, len(artifacts)*len(locs))
	for _, a := range artifacts {
		for _, l := range locs {
			out = append(out, release.VulnOccurrence{PackageName: f.Package.Name, PackagePURL: f.Package.PURL, Artifact: a, Location: l})
		}
	}
	return out
}

// exemptionFor returns the unexpired exemption that takes f out of the gate,
// if any: an unscoped one of its advisory, or else, when every occurrence of
// f is covered by some scoped one, the one covering the first. occ nil is
// occurrencesOf(f, nil).
func exemptionFor(gate release.ShapeVulnGate, f Finding, occ []release.VulnOccurrence, now time.Time) *Exemption {
	for _, x := range gate.Exemptions {
		if e := exemptionOf(release.ShapeVulnScopedExempt{ID: x.ID, Reason: x.Reason, Expires: x.Expires}); f.Names(strings.TrimSpace(e.ID)) && !e.Expired(now) {
			return &e
		}
	}
	var scoped []release.ShapeVulnScopedExempt
	for _, x := range gate.ScopedExemptions {
		if e := exemptionOf(x); f.Names(strings.TrimSpace(e.ID)) && !e.Expired(now) {
			if !x.Scoped() {
				return &e
			}
			scoped = append(scoped, x)
		}
	}
	if len(scoped) == 0 {
		return nil
	}
	if occ == nil {
		occ = occurrencesOf(f, nil)
	}
	var first *Exemption
	for _, o := range occ {
		i := slices.IndexFunc(scoped, func(x release.ShapeVulnScopedExempt) bool { return x.ScopeCovers(o) })
		if i < 0 {
			return nil
		}
		if first == nil {
			e := exemptionOf(scoped[i])
			first = &e
		}
	}
	return first
}

// exemptionOf is a declared exemption as the rule reports it.
func exemptionOf(x release.ShapeVulnScopedExempt) Exemption {
	return Exemption{
		ID: x.ID, Reason: x.Reason, Expires: x.Expires,
		Packages: slices.Clone(x.Packages), Artifacts: slices.Clone(x.Artifacts), Locations: slices.Clone(x.Locations),
	}
}

// Rule names the part of the vuln_gate rule that decided a finding's action:
// the policy field that fired, or the exemption that took it out of the gate.
type Rule string

// The rules. RuleNone is a finding nothing fired on (and a finding an
// authoritative ledger blocked without saying why).
const (
	RuleNone          Rule = ""
	RuleBlockKEV      Rule = "block_kev"
	RuleBlockSeverity Rule = "block_severity"
	RuleWarnSeverity  Rule = "warn_severity"
	RuleExemption     Rule = "exemption"
)

// Judge applies the rule to one finding. A finding an exemption covers is
// ActionNone, and the exemption is returned beside it. The gate's mode is not
// consulted: Judge says what the rule makes of the finding, which is the same
// whether the gate enforces, audits, or is off.
//
// It knows no artifact of f, so an exemption scoped to an artifact does not
// cover it; [Evaluate] judges with each finding's occurrences.
func Judge(gate release.ShapeVulnGate, f Finding, now time.Time) (Action, *Exemption) {
	a, _, e := JudgeRule(gate, f, now)
	return a, e
}

// JudgeRule is Judge, also naming the rule that decided. Like Judge, it knows
// no artifact of f.
func JudgeRule(gate release.ShapeVulnGate, f Finding, now time.Time) (Action, Rule, *Exemption) {
	return judge(gate, f, nil, now)
}

func judge(gate release.ShapeVulnGate, f Finding, occ []release.VulnOccurrence, now time.Time) (Action, Rule, *Exemption) {
	if e := exemptionFor(gate, f, occ, now); e != nil {
		return ActionNone, RuleExemption, e
	}
	if gate.BlockKEV && f.KEV {
		return ActionBlock, RuleBlockKEV, nil
	}
	if gate.BlockSeverity != "" && gate.BlockSeverity != "none" && f.Severity.AtLeast(gate.BlockSeverity) {
		if !gate.FixableOnly || f.Fixable() {
			return ActionBlock, RuleBlockSeverity, nil
		}
	}
	if gate.WarnSeverity != "" && gate.WarnSeverity != "none" && f.Severity.AtLeast(gate.WarnSeverity) {
		return ActionWarn, RuleWarnSeverity, nil
	}
	return ActionNone, RuleNone, nil
}

// Verdict is the gate's answer for a release.
type Verdict string

const (
	// VerdictPass means every artifact was scanned against a current database,
	// and nothing blocks or warns.
	VerdictPass Verdict = "pass"
	// VerdictWarn means nothing blocks; at least one finding warns.
	VerdictWarn Verdict = "warn"
	// VerdictBlock means at least one finding blocks. Reported in every mode:
	// the verdict says what was found, the mode decides whether a promotion
	// is refused.
	VerdictBlock Verdict = "block"
	// VerdictStale means a scan the verdict needs ran against a database
	// older than StaleAfter, and no current scan could replace it. Not a
	// pass, and not a block: the data cannot vouch for the release either
	// way (its blocking ids are still listed).
	VerdictStale Verdict = "stale"
	// VerdictUnscanned means nothing blocks, but an artifact has no SBOM or no
	// scan. Not a pass: "not scanned" is never clean.
	VerdictUnscanned Verdict = "unscanned"
	// VerdictOff means the env's vuln_gate is off; nothing is judged.
	VerdictOff Verdict = "off"
)

// Determined reports whether the verdict is an answer (pass, warn, block,
// off) rather than "could not determine" (stale, unscanned).
func (v Verdict) Determined() bool { return v != VerdictStale && v != VerdictUnscanned }

// Judged is a finding with the rule's answer.
type Judged struct {
	Finding
	Action Action
	// Rule is what decided Action.
	Rule Rule
	// Exemption is the exemption that took the finding out of the gate.
	Exemption *Exemption
}

// Coverage is how much of a release was scanned.
type Coverage struct {
	// Artifacts is how many artifacts of the release an SBOM applies to
	// (release.SBOMApplicable): the N of "N of N applicable".
	Artifacts int `json:"artifacts"`
	// WithSBOM is how many of them have a stored SBOM.
	WithSBOM int `json:"with_sbom"`
	// Scanned is how many of those have a scan.
	Scanned int `json:"scanned"`
	// Unscanned names each artifact with no SBOM or no scan, with why.
	Unscanned []UnscannedArtifact `json:"unscanned,omitempty"`
	// NotApplicable names each artifact of the release no SBOM describes
	// (source maps, a git source pin), with why. They are outside
	// Artifacts, and never unscanned.
	NotApplicable []NotApplicableArtifact `json:"not_applicable,omitempty"`
}

// UnscannedArtifact is an artifact the verdict could not cover.
type UnscannedArtifact struct {
	Artifact string `json:"artifact"`
	Reason   string `json:"reason"`
}

// NotApplicableArtifact is an artifact no SBOM describes, so no scan covers.
type NotApplicableArtifact struct {
	Artifact string `json:"artifact"`
	Reason   string `json:"reason"`
}

// NotApplicableOf lists rel's artifacts no SBOM describes
// (release.SBOMNotApplicable), sorted by name; nil when there are none.
func NotApplicableOf(rel *release.Release) []NotApplicableArtifact {
	if rel == nil {
		return nil
	}
	var out []NotApplicableArtifact
	for _, name := range rel.ArtifactNames() {
		if reason, ok := release.SBOMNotApplicable(name, rel.Artifacts[name]); ok {
			out = append(out, NotApplicableArtifact{Artifact: name, Reason: reason})
		}
	}
	return out
}

// Complete reports whether every artifact was scanned.
func (c Coverage) Complete() bool { return len(c.Unscanned) == 0 && c.Scanned == c.Artifacts }

// GateResult is the gate block of the vuln-list document.
type GateResult struct {
	// Mode is the env's vuln_gate mode: off, audit or enforce.
	Mode string `json:"mode"`
	// GradedAs is the mode the gate is judged at: the declared mode, except
	// that the warn-first rollout grades an enforce as audit until the
	// project's forge pin reaches the vuln-gate rule's GatesFrom release
	// (internal/cli/lint/rollout_grade.go). The caller grades; Evaluate
	// records what it was told (EvaluateInput.GradedAs).
	GradedAs string `json:"graded_as"`
	// Verdict is the gate's answer.
	Verdict Verdict `json:"verdict"`
	// Blocking are the ids of the advisories that block, sorted, each once.
	Blocking []string `json:"blocking"`
	// Determined is false when the verdict is "could not determine" (stale,
	// unscanned): the exit code is 2, and the document's notes say why.
	Determined bool `json:"determined"`
	// JudgedBy says whose judgement of each finding the verdict is:
	// "control-plane" when the env's control plane judged them (it is
	// authoritative for an env whose ledger it keeps). Absent for an env
	// whose ledger is this machine's, which forge judges by definition.
	// "local" is reserved for a verdict forge gave in a control plane's
	// stead; this forge never gives one (a control plane that cannot judge
	// the env is "could not determine").
	JudgedBy string `json:"judged_by,omitempty"`
	// Reason is the verdict's code in the promotion vocabulary:
	// vuln_gate_blocked for block, vuln_gate_unscanned for stale and
	// unscanned (could not determine is never a pass), absent otherwise.
	// It names the rule, not a refusal: whether a promotion is refused is
	// GradedAs's to decide.
	Reason string `json:"reason,omitempty"`
	// ExemptionsApplied are the unexpired exemptions that took at least one
	// finding out of the gate, sorted by id, each declaration (an id and its
	// scope: release.ShapeVulnScopedExempt.Identity) once. Absent when none
	// did, and when the gate is off (nothing is judged).
	ExemptionsApplied []Exemption `json:"exemptions_applied,omitempty"`
}

// The JudgedBy values.
const (
	JudgedByControlPlane = "control-plane"
	JudgedByLocal        = "local"
)

// Judgement is an authoritative ledger's answer for one finding: whether the
// env's vuln_gate blocks it, and the exemption that takes it out of the gate.
type Judgement struct {
	Blocking  bool
	Exemption *Exemption
}

// Evaluation is the rule applied to a release's findings.
type Evaluation struct {
	Gate GateResult
	// Findings are the inputs in their input order, each judged.
	Findings []Judged
}

// EvaluateInput is what Evaluate judges.
type EvaluateInput struct {
	// Gate is the env's vuln_gate, resolved (release.ShapeDeliveryPolicy's
	// Resolved().VulnGate: the defaults when the env declares none).
	Gate release.ShapeVulnGate
	// GradedAs is the mode the rollout grades Gate.Mode as; "" is the
	// declared mode.
	GradedAs release.DeliveryMode
	// Findings are every finding of the release, across its artifacts.
	Findings []Finding
	// Coverage says what was scanned.
	Coverage Coverage
	// Stale reports that a scan the findings came from ran against a
	// database older than StaleAfter.
	Stale bool
	// Now dates exemptions.
	Now time.Time
	// Judged, when non-nil, is an authoritative ledger's judgement of every
	// finding, by Finding.Key() (a control plane's per-finding blocking and
	// exemption): it decides which findings block and which are exempt, and
	// this rule is not consulted for either. A finding it does not name
	// neither blocks nor is exempt. The warn band, which gates nothing and
	// which such a ledger does not report, is still this rule's.
	Judged map[string]Judgement
	// Occurrences are where each finding was found, by Finding.Key(): what a
	// scoped exemption is matched against. A finding it does not name is
	// matched as found in no known artifact, at its Locations.
	Occurrences map[string][]release.VulnOccurrence
}

// judgeAuthoritative is a finding's action under in.Judged.
func judgeAuthoritative(in EvaluateInput, f Finding) (Action, Rule, *Exemption) {
	a := in.Judged[f.Key()]
	switch {
	case a.Exemption != nil:
		return ActionNone, RuleExemption, a.Exemption
	case a.Blocking:
		// The ledger says it blocks, not which field fired.
		return ActionBlock, RuleNone, nil
	}
	if local, rule, _ := judge(in.Gate, f, in.Occurrences[f.Key()], in.Now); local == ActionWarn {
		return ActionWarn, rule, nil
	}
	return ActionNone, RuleNone, nil
}

// Evaluate applies the rule to a release. Precedence: off, then stale, then
// block, then unscanned, then warn, then pass.
//
// Stale outranks block on purpose. Findings from a database older than
// StaleAfter cannot establish the release's status either way, so the
// verdict is "could not determine" whatever they say. The blocking ids are
// still listed in Gate.Blocking, and every finding keeps its action, so a
// reader sees what the old data found. A blocking finding from a current
// scan outranks an unscanned artifact: that one was found with current
// data, and the missing scan could only add to it.
func Evaluate(in EvaluateInput) Evaluation {
	ev := Evaluation{Gate: GateResult{
		Mode:     string(in.Gate.Mode),
		GradedAs: string(in.Gate.Mode),
		Blocking: []string{},
	}}
	if ev.Gate.Mode == "" {
		ev.Gate.Mode, ev.Gate.GradedAs = string(release.DeliveryAudit), string(release.DeliveryAudit)
	}
	if in.GradedAs != "" && in.Gate.Mode != release.DeliveryOff {
		ev.Gate.GradedAs = string(in.GradedAs)
	}
	off := in.Gate.Mode == release.DeliveryOff
	warned := false
	seen := map[string]bool{}
	applied := map[string]Exemption{}
	for _, f := range in.Findings {
		j := Judged{Finding: f, Action: ActionNone}
		switch {
		case !off && in.Judged != nil:
			j.Action, j.Rule, j.Exemption = judgeAuthoritative(in, f)
		case !off:
			j.Action, j.Rule, j.Exemption = judge(in.Gate, f, in.Occurrences[f.Key()], in.Now)
		case in.Judged != nil:
			j.Exemption = in.Judged[f.Key()].Exemption
		default:
			j.Exemption = exemptionFor(in.Gate, f, in.Occurrences[f.Key()], in.Now)
		}
		if !off && j.Exemption != nil {
			applied[j.Exemption.identity()] = *j.Exemption
		}
		switch j.Action {
		case ActionBlock:
			if !seen[f.ID] {
				seen[f.ID] = true
				ev.Gate.Blocking = append(ev.Gate.Blocking, f.ID)
			}
		case ActionWarn:
			warned = true
		}
		ev.Findings = append(ev.Findings, j)
	}
	sort.Strings(ev.Gate.Blocking)
	switch {
	case off:
		ev.Gate.Verdict = VerdictOff
	case in.Stale:
		ev.Gate.Verdict = VerdictStale
	case len(ev.Gate.Blocking) > 0:
		ev.Gate.Verdict = VerdictBlock
	case !in.Coverage.Complete():
		ev.Gate.Verdict = VerdictUnscanned
	case warned:
		ev.Gate.Verdict = VerdictWarn
	default:
		ev.Gate.Verdict = VerdictPass
	}
	ev.Gate.Determined = ev.Gate.Verdict.Determined()
	switch {
	case ev.Gate.Verdict == VerdictBlock:
		ev.Gate.Reason = release.RefusalVulnGateBlocked
	case !ev.Gate.Determined:
		ev.Gate.Reason = release.RefusalVulnGateUnscanned
	}
	for _, id := range sortedKeys(applied) {
		ev.Gate.ExemptionsApplied = append(ev.Gate.ExemptionsApplied, applied[id])
	}
	return ev
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
