package prpolicy

import "time"

// Queue priorities (design §10.1). The queue picks entries by priority,
// highest first, then by enqueue time; an entry at PriorityUrgent or above
// goes alone, in the next batch (§3.2).
const (
	PriorityMin = 0
	PriorityMax = 100
	// PriorityDefault is an ordinary PR's priority when it is enqueued.
	PriorityDefault = 50
	// PriorityAgingCap is as far as waiting alone lifts an ordinary PR: one
	// point per full hour in the queue, up to here.
	PriorityAgingCap = 70
	// PriorityRelease is a PR the release flow opened.
	PriorityRelease = 80
	// PriorityUrgent is the threshold at which an entry goes alone.
	PriorityUrgent = 90
	// PriorityIncidentFix is the PR an open incident names as its fix.
	PriorityIncidentFix = 100
	// PriorityRevert is a revert the queue itself makes.
	PriorityRevert = 100
)

// PrioritySource names the rule that produced a priority.
type PrioritySource string

const (
	PrioritySourceDefault     PrioritySource = "default"
	PrioritySourceAging       PrioritySource = "aging"
	PrioritySourceRelease     PrioritySource = "release"
	PrioritySourceIncidentFix PrioritySource = "incident_fix"
	PrioritySourceRevert      PrioritySource = "revert"
	PrioritySourceDecision    PrioritySource = "decision"
)

// PriorityDecision is an approved `priority` decision for the record, which a
// signed-in user made in the app (`pipeline_decisions`, kind `priority`).
type PriorityDecision struct {
	// Value is value_json's priority.
	Value int
}

// PriorityInputs is everything ComputePriority reads. Each field comes from
// a server-written record, never from a PR's title, body, labels or an
// agent's output: agents cannot raise priority (design §4.6), so there is no
// field through which they could.
type PriorityInputs struct {
	// Decision is the record's approved priority decision, or nil.
	Decision *PriorityDecision
	// Revert is true for an entry the queue created by `queue.revert@1`.
	Revert bool
	// Incident is the incident whose fix_record_id names this record, or nil.
	// It counts only while the incident is open.
	Incident *LinkedIncident
	// Release is true for a PR the release server actions opened.
	Release bool
	// EnqueuedAt is when the entry joined the queue; zero when it has not.
	EnqueuedAt time.Time
	// Now is the time to compute at.
	Now time.Time
}

// ComputePriority returns an entry's priority and the rule that set it. The
// precedence is fixed, first match wins:
//
//  1. a user's priority decision, whatever its value (it may lower as well as
//     raise), clamped to [PriorityMin, PriorityMax];
//  2. a queue revert: PriorityRevert;
//  3. the fix of an open incident: PriorityIncidentFix;
//  4. a release PR: PriorityRelease;
//  5. aging: PriorityDefault plus one per full hour since EnqueuedAt, capped
//     at PriorityAgingCap;
//  6. PriorityDefault.
//
// Aging applies only to rule 5: it never lifts an ordinary PR past the cap,
// which sits below PriorityRelease and PriorityUrgent, so a long wait never
// makes a PR urgent.
func ComputePriority(in PriorityInputs) (int, PrioritySource) {
	switch {
	case in.Decision != nil:
		return min(max(in.Decision.Value, PriorityMin), PriorityMax), PrioritySourceDecision
	case in.Revert:
		return PriorityRevert, PrioritySourceRevert
	case in.Incident != nil && in.Incident.Open():
		return PriorityIncidentFix, PrioritySourceIncidentFix
	case in.Release:
		return PriorityRelease, PrioritySourceRelease
	}
	if in.EnqueuedAt.IsZero() || !in.Now.After(in.EnqueuedAt) {
		return PriorityDefault, PrioritySourceDefault
	}
	hours := int(in.Now.Sub(in.EnqueuedAt) / time.Hour)
	if hours == 0 {
		return PriorityDefault, PrioritySourceDefault
	}
	return min(PriorityDefault+hours, PriorityAgingCap), PrioritySourceAging
}

// IsUrgent reports whether an entry at priority p goes alone in the next
// batch.
func IsUrgent(p int) bool { return p >= PriorityUrgent }
