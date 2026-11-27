package prpolicy

import (
	"slices"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// IncidentSource is the detector that opened an incident (design §4.5,
// `incidents.source`).
type IncidentSource string

const (
	IncidentSourceMain      IncidentSource = "main"
	IncidentSourceRelease   IncidentSource = "release"
	IncidentSourceProdAlert IncidentSource = "prod_alert"
	IncidentSourceObsIssue  IncidentSource = "obs_issue"
	IncidentSourceNightly   IncidentSource = "nightly"
	IncidentSourceFlake     IncidentSource = "flake"
	IncidentSourceInfra     IncidentSource = "infra"
	IncidentSourceRecurring IncidentSource = "recurring"
	// IncidentSourceVuln is a new blocking advisory against a deployed
	// release's SBOM (owner decision D16).
	IncidentSourceVuln IncidentSource = "vuln"
)

// IncidentSources is every IncidentSource.
var IncidentSources = []IncidentSource{
	IncidentSourceMain, IncidentSourceRelease, IncidentSourceProdAlert, IncidentSourceObsIssue,
	IncidentSourceNightly, IncidentSourceFlake, IncidentSourceInfra, IncidentSourceRecurring, IncidentSourceVuln,
}

// IncidentState is an incident's lifecycle state (`incidents.state`).
type IncidentState string

const (
	IncidentStateOpen        IncidentState = "open"
	IncidentStateResponding  IncidentState = "responding"
	IncidentStateMitigating  IncidentState = "mitigating"
	IncidentStateResolved    IncidentState = "resolved"
	IncidentStateNeedsPerson IncidentState = "needs_person"
)

// IncidentStates is every IncidentState.
var IncidentStates = []IncidentState{
	IncidentStateOpen, IncidentStateResponding, IncidentStateMitigating, IncidentStateResolved, IncidentStateNeedsPerson,
}

// IncidentSeverity is how bad an incident is, lowest first.
type IncidentSeverity string

const (
	IncidentSeverityLow      IncidentSeverity = "low"
	IncidentSeverityMedium   IncidentSeverity = "medium"
	IncidentSeverityHigh     IncidentSeverity = "high"
	IncidentSeverityCritical IncidentSeverity = "critical"
)

// IncidentSeverities is every IncidentSeverity, lowest first.
var IncidentSeverities = []IncidentSeverity{
	IncidentSeverityLow, IncidentSeverityMedium, IncidentSeverityHigh, IncidentSeverityCritical,
}

// rank orders severities from 1 (low); an unknown severity ranks 0, below
// every known one, so it never clears a threshold.
func (s IncidentSeverity) rank() int {
	return slices.Index(IncidentSeverities, s) + 1
}

// AtLeast reports whether s is a known severity at or above min.
func (s IncidentSeverity) AtLeast(min IncidentSeverity) bool {
	return s.rank() > 0 && s.rank() >= min.rank()
}

// LinkedIncident is the incident whose `fix_record_id` names the PR record
// being judged. The caller reads it from the incidents table and nowhere
// else: the link is written by `respond`'s server action, never by the PR's
// author, so neither a title nor a body nor an agent's claim can make a PR an
// incident fix. A nil *LinkedIncident means no incident names the record.
type LinkedIncident struct {
	Source   IncidentSource
	State    IncidentState
	Severity IncidentSeverity
}

// Open reports whether the incident is in a known state other than
// resolved. An unknown state is not open.
func (i LinkedIncident) Open() bool {
	switch i.State {
	case IncidentStateOpen, IncidentStateResponding, IncidentStateMitigating, IncidentStateNeedsPerson:
		return true
	}
	return false
}

// EmergencyApproval is who approves an eligible emergency-land request.
type EmergencyApproval string

const (
	// EmergencyApprovalAsk puts the request in a person's Inbox; it lands
	// only on their recorded decision.
	EmergencyApprovalAsk EmergencyApproval = "ask"
	// EmergencyApprovalAutoAllow lets the server approve at once, recording
	// the policy change that allowed it.
	EmergencyApprovalAutoAllow EmergencyApproval = "auto_allow"
)

// EmergencyReason says why a head is or is not eligible for an emergency
// land.
type EmergencyReason string

const (
	EmergencyEligible EmergencyReason = "eligible"
	// EmergencyDailyCapReached: eligible, but auto_allow has used the day's
	// cap, so a person is asked.
	EmergencyDailyCapReached EmergencyReason = "daily_cap_reached"
	// EmergencyNotPassed: the head has no review pass. An emergency skips the
	// batching and the gate wait, never the review.
	EmergencyNotPassed    EmergencyReason = "not_passed"
	EmergencyNoIncident   EmergencyReason = "no_incident"
	EmergencyIncidentDone EmergencyReason = "incident_not_open"
	EmergencyLowSeverity  EmergencyReason = "severity_below_high"
	// EmergencyMainIncident: a main incident is answered by a revert, which
	// restores a tree the gate already passed; it never needs an emergency.
	EmergencyMainIncident EmergencyReason = "main_incident"
	EmergencyUnknownSrc   EmergencyReason = "unknown_source"
	// EmergencyPolicyOff: the project refuses emergency lands; the PR goes
	// through the normal queue (at incident-fix priority).
	EmergencyPolicyOff EmergencyReason = "policy_off"
)

// EmergencyMinSeverity is the lowest incident severity whose fix may be
// landed as an emergency (design §6.1).
const EmergencyMinSeverity = IncidentSeverityHigh

// EmergencyInput is everything EvaluateEmergency reads.
type EmergencyInput struct {
	// Head is the PR head the request would land.
	Head string
	// VerdictPassed is true when Head itself has a review pass (a pass for
	// an earlier head does not count).
	VerdictPassed bool
	// Incident is the incident whose fix_record_id is this PR, or nil.
	Incident *LinkedIncident
	// Policy is the project's emergency_land_policy. Empty means the default,
	// ask.
	Policy core.PipelineEmergencyLandPolicy
	// AutoApprovedToday counts the project's emergency lands the auto_allow
	// policy approved today; DailyCap is emergency_land_daily_cap.
	AutoApprovedToday int
	DailyCap          int
}

// EmergencyEligibility is EvaluateEmergency's answer. Approval is set only
// when Eligible.
type EmergencyEligibility struct {
	Eligible bool
	// Head is the head the answer binds to: a push makes it stale.
	Head     string
	Approval EmergencyApproval
	Reason   EmergencyReason
}

// EvaluateEmergency decides whether `pr.next` may return `emergency` for a
// head (design §3.8, §6.1): only a head with a review pass that is the linked
// fix of an open incident of severity high or above, other than a main
// incident, and only when the project's policy is not off. Every input it
// does not recognise (a severity, a state, a source) makes it ineligible.
//
// Under auto_allow the server may approve, until the day's cap: at or past
// DailyCap the request falls back to ask. A cap of 0 or less always asks.
// An unrecognised policy also asks, the safe answer: a person decides.
func EvaluateEmergency(in EmergencyInput) EmergencyEligibility {
	no := func(r EmergencyReason) EmergencyEligibility {
		return EmergencyEligibility{Head: in.Head, Reason: r}
	}
	switch {
	case !in.VerdictPassed || in.Head == "":
		return no(EmergencyNotPassed)
	case in.Incident == nil:
		return no(EmergencyNoIncident)
	case !in.Incident.Open():
		return no(EmergencyIncidentDone)
	case in.Incident.Source == IncidentSourceMain:
		return no(EmergencyMainIncident)
	case !slices.Contains(IncidentSources, in.Incident.Source):
		return no(EmergencyUnknownSrc)
	case !in.Incident.Severity.AtLeast(EmergencyMinSeverity):
		return no(EmergencyLowSeverity)
	}
	yes := EmergencyEligibility{Eligible: true, Head: in.Head, Approval: EmergencyApprovalAsk, Reason: EmergencyEligible}
	switch in.Policy {
	case core.PipelineEmergencyLandOff:
		return no(EmergencyPolicyOff)
	case core.PipelineEmergencyLandAutoAllow:
		// A cap of 0 or less, or a count that cannot be true (negative), never
		// authorizes an automatic land: the safe answer is a person.
		if in.DailyCap <= 0 || in.AutoApprovedToday < 0 || in.AutoApprovedToday >= in.DailyCap {
			yes.Reason = EmergencyDailyCapReached
			return yes
		}
		yes.Approval = EmergencyApprovalAutoAllow
	}
	return yes
}
