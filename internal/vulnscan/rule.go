package vulnscan

import (
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
//     GHSA of the same advisory.

// Action is what the rule makes of one finding.
type Action string

// The actions, strongest first.
const (
	ActionBlock Action = "block"
	ActionWarn  Action = "warn"
	ActionNone  Action = "none"
)

// Exemption is an accepted advisory, as the env declares it
// (forge.VulnExemption): time-boxed, with its reason.
type Exemption struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
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

// exemptionFor returns the unexpired exemption that covers f, if any.
func exemptionFor(gate release.ShapeVulnGate, f Finding, now time.Time) *Exemption {
	for _, x := range gate.Exemptions {
		e := Exemption{ID: x.ID, Reason: x.Reason, Expires: x.Expires}
		if f.Names(strings.TrimSpace(e.ID)) && !e.Expired(now) {
			return &e
		}
	}
	return nil
}

// Judge applies the rule to one finding. A finding an exemption covers is
// ActionNone, and the exemption is returned beside it. The gate's mode is not
// consulted: Judge says what the rule makes of the finding, which is the same
// whether the gate enforces, audits, or is off.
func Judge(gate release.ShapeVulnGate, f Finding, now time.Time) (Action, *Exemption) {
	if e := exemptionFor(gate, f, now); e != nil {
		return ActionNone, e
	}
	if gate.BlockKEV && f.KEV {
		return ActionBlock, nil
	}
	if gate.BlockSeverity != "" && gate.BlockSeverity != "none" && f.Severity.AtLeast(gate.BlockSeverity) {
		if !gate.FixableOnly || f.Fixable() {
			return ActionBlock, nil
		}
	}
	if gate.WarnSeverity != "" && gate.WarnSeverity != "none" && f.Severity.AtLeast(gate.WarnSeverity) {
		return ActionWarn, nil
	}
	return ActionNone, nil
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
	// Exemption is the exemption that took the finding out of the gate.
	Exemption *Exemption
}

// Coverage is how much of a release was scanned.
type Coverage struct {
	// Artifacts is how many artifacts the release has.
	Artifacts int `json:"artifacts"`
	// WithSBOM is how many of them have a stored SBOM.
	WithSBOM int `json:"with_sbom"`
	// Scanned is how many of those have a scan.
	Scanned int `json:"scanned"`
	// Unscanned names each artifact with no SBOM or no scan, with why.
	Unscanned []UnscannedArtifact `json:"unscanned,omitempty"`
}

// UnscannedArtifact is an artifact the verdict could not cover.
type UnscannedArtifact struct {
	Artifact string `json:"artifact"`
	Reason   string `json:"reason"`
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
}

// judgeAuthoritative is a finding's action under in.Judged.
func judgeAuthoritative(in EvaluateInput, f Finding) (Action, *Exemption) {
	a := in.Judged[f.Key()]
	switch {
	case a.Exemption != nil:
		return ActionNone, a.Exemption
	case a.Blocking:
		return ActionBlock, nil
	}
	if local, _ := Judge(in.Gate, f, in.Now); local == ActionWarn {
		return ActionWarn, nil
	}
	return ActionNone, nil
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
	for _, f := range in.Findings {
		j := Judged{Finding: f, Action: ActionNone}
		switch {
		case !off && in.Judged != nil:
			j.Action, j.Exemption = judgeAuthoritative(in, f)
		case !off:
			j.Action, j.Exemption = Judge(in.Gate, f, in.Now)
		case in.Judged != nil:
			j.Exemption = in.Judged[f.Key()].Exemption
		default:
			j.Exemption = exemptionFor(in.Gate, f, in.Now)
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
	return ev
}
