package core

// The pipeline's decision vocabularies
// (db/migrations/20261126154939_pipeline_decisions.up.sql). Each All* set is mirrored by a CHECK that
// declares it with forge:closed-set, so `forge lint --closed-sets` fails the
// moment the two drift: a value added here needs a migration widening its
// CHECK.

// PipelineDecisionSubjectKind is what a pipeline decision is about.
type PipelineDecisionSubjectKind string

const (
	// PipelineDecisionSubjectPRHead binds to one head of a PR: a push voids it.
	PipelineDecisionSubjectPRHead   PipelineDecisionSubjectKind = "pr_head"
	PipelineDecisionSubjectPR       PipelineDecisionSubjectKind = "pr"
	PipelineDecisionSubjectRepo     PipelineDecisionSubjectKind = "repo"
	PipelineDecisionSubjectRelease  PipelineDecisionSubjectKind = "release"
	PipelineDecisionSubjectIncident PipelineDecisionSubjectKind = "incident"
)

// AllPipelineDecisionSubjectKinds is every PipelineDecisionSubjectKind
// (pipeline_decisions_subject_kind).
var AllPipelineDecisionSubjectKinds = []PipelineDecisionSubjectKind{
	PipelineDecisionSubjectPRHead,
	PipelineDecisionSubjectPR,
	PipelineDecisionSubjectRepo,
	PipelineDecisionSubjectRelease,
	PipelineDecisionSubjectIncident,
}

// PipelineDecisionKind is what a pipeline decision decides.
type PipelineDecisionKind string

const (
	PipelineDecisionApproveBreakingProto  PipelineDecisionKind = "approve_breaking_proto"
	PipelineDecisionApproveBreaksRollback PipelineDecisionKind = "approve_breaks_rollback"
	PipelineDecisionApproveProdFix        PipelineDecisionKind = "approve_prod_fix"
	PipelineDecisionHold                  PipelineDecisionKind = "hold"
	PipelineDecisionReleaseHold           PipelineDecisionKind = "release_hold"
	PipelineDecisionPriority              PipelineDecisionKind = "priority"
	PipelineDecisionEmergencyLand         PipelineDecisionKind = "emergency_land"
	PipelineDecisionReleaseNow            PipelineDecisionKind = "release_now"
	PipelineDecisionReleasePromote        PipelineDecisionKind = "release_promote"
	PipelineDecisionPauseQueue            PipelineDecisionKind = "pause_queue"
	PipelineDecisionResolveNeedsPerson    PipelineDecisionKind = "resolve_needs_person"
	PipelineDecisionClosePR               PipelineDecisionKind = "close_pr"
)

// AllPipelineDecisionKinds is every PipelineDecisionKind
// (pipeline_decisions_kind).
var AllPipelineDecisionKinds = []PipelineDecisionKind{
	PipelineDecisionApproveBreakingProto,
	PipelineDecisionApproveBreaksRollback,
	PipelineDecisionApproveProdFix,
	PipelineDecisionHold,
	PipelineDecisionReleaseHold,
	PipelineDecisionPriority,
	PipelineDecisionEmergencyLand,
	PipelineDecisionReleaseNow,
	PipelineDecisionReleasePromote,
	PipelineDecisionPauseQueue,
	PipelineDecisionResolveNeedsPerson,
	PipelineDecisionClosePR,
}

// PipelineRequestableDecisionKinds are the kinds an agent may request (insert
// in state requested); every other kind only a person decides. Mirrored by
// pipeline_decisions_requestable, a CHECK the closed-set lint cannot read
// (it is not a plain IN list); TestPipelineDecisionsConstraints pins the two
// equal.
var PipelineRequestableDecisionKinds = []PipelineDecisionKind{
	PipelineDecisionEmergencyLand,
	PipelineDecisionResolveNeedsPerson,
}

// PipelineDecisionState is where a pipeline decision is in its life.
type PipelineDecisionState string

const (
	// PipelineDecisionRequested is an agent's request nobody has decided.
	PipelineDecisionRequested PipelineDecisionState = "requested"
	// PipelineDecisionApproved is where a decision a person made directly starts.
	PipelineDecisionApproved PipelineDecisionState = "approved"
	PipelineDecisionDenied   PipelineDecisionState = "denied"
	// PipelineDecisionExpired lapsed: an approval past expires_at, or a
	// request nobody decided.
	PipelineDecisionExpired PipelineDecisionState = "expired"
	PipelineDecisionApplied PipelineDecisionState = "applied"
	PipelineDecisionRevoked PipelineDecisionState = "revoked"
)

// AllPipelineDecisionStates is every PipelineDecisionState
// (pipeline_decisions_state).
var AllPipelineDecisionStates = []PipelineDecisionState{
	PipelineDecisionRequested,
	PipelineDecisionApproved,
	PipelineDecisionDenied,
	PipelineDecisionExpired,
	PipelineDecisionApplied,
	PipelineDecisionRevoked,
}

// PipelineDecidedVia is how a pipeline decision was decided: a signed-in
// person's surface, or the project's policy.
type PipelineDecidedVia string

const (
	PipelineDecidedViaWeb     PipelineDecidedVia = "web"
	PipelineDecidedViaDesktop PipelineDecidedVia = "desktop"
	// PipelineDecidedViaPolicy is an emergency_land the project's auto_allow
	// policy approved.
	PipelineDecidedViaPolicy PipelineDecidedVia = "policy"
)

// AllPipelineDecidedVia is every PipelineDecidedVia
// (pipeline_decisions_decided_via).
var AllPipelineDecidedVia = []PipelineDecidedVia{
	PipelineDecidedViaWeb,
	PipelineDecidedViaDesktop,
	PipelineDecidedViaPolicy,
}
