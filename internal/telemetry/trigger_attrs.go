// Copyright (c) 2025 Reliant Labs
package telemetry

// SpanNameTriggerFire is the prefix of one firing attempt's span:
// "trigger.fire <event kind>" (internal/triggers, fire_span.go). Each is the
// root of a trace of its own, linked to whatever caused it, because one cause
// (a GitHub delivery, a run's outcome) fans out to triggers of other orgs.
// The run a firing launches starts a trace of its own too, its workflow span
// linking back to the firing and carrying AttrTriggerEventID.
//
// Every trigger.fire span carries the TRIGGER's org (AttrOrgID) and owner
// (AttrUserID), set from the trigger: a firing runs outside any run scope. A
// launched one also carries the chat it launched (AttrGenAIConversationID)
// and the run (AttrAIRunID). These names are a public contract once
// released: never rename one in place, add the new name beside it.
const SpanNameTriggerFire = "trigger.fire"

// The attributes of a trigger.fire span. No payload, prompt or raw dedupe key
// is ever one of them.
const (
	AttrTriggerID   = "reliant.trigger.id"
	AttrTriggerKind = "reliant.trigger.kind"
	// AttrTriggerEventKind is the kind of the trigger_events row; the span is
	// named by it.
	AttrTriggerEventKind = "reliant.trigger.event_kind"
	// AttrTriggerEventID is the trigger_events row the firing wrote or
	// settled. A run a trigger launched carries it on its workflow span, so
	// the two join by attribute as well as by link.
	AttrTriggerEventID = "reliant.trigger.event_id"
	// AttrTriggerActivationID and AttrTriggerActivationName are set when the
	// trigger activates a workflow's declared trigger: the activation row and
	// the declared trigger's name.
	AttrTriggerActivationID   = "reliant.trigger.activation_id"
	AttrTriggerActivationName = "reliant.trigger.activation_name"
	// AttrTriggerWorkflowRef is the workflow the trigger runs.
	AttrTriggerWorkflowRef = "reliant.trigger.workflow_ref"
	// AttrTriggerDedupeKeyHash is the first 16 hex digits of the SHA-256 of
	// the event's dedupe key: enough to join, never the key itself.
	AttrTriggerDedupeKeyHash = "reliant.trigger.dedupe_key_hash"
	// AttrTriggerOriginRunID is the chat of the run whose outcome
	// (workflow_event) or write (an echoed integration event the owner may
	// see) caused the event: its id only, never its org's data.
	AttrTriggerOriginRunID = "reliant.trigger.origin_run_id"
	// AttrTriggerOutcome is the outcome the attempt left on the event
	// (launched, skipped, failed, pending). Unset when the attempt recorded
	// none: it ended in an error before one (and is retried), or the event
	// turned out not to be the trigger's to record.
	AttrTriggerOutcome = "reliant.trigger.outcome"
	// AttrTriggerReason is why the event has its outcome, as one word of a
	// closed vocabulary (TriggerReason*), never the event's outcome_detail:
	// that text can carry an error, a filter expression, a workflow or model
	// name. The detail itself is on the trigger_events row AttrTriggerEventID
	// names. A detail the vocabulary does not know is TriggerReasonOther.
	AttrTriggerReason = "reliant.trigger.reason"
	// AttrTriggerResumed marks an attempt that continued an event already
	// launched (its chat committed, its start possibly not).
	AttrTriggerResumed = "reliant.trigger.resumed"
)

// The values of AttrTriggerReason. Add a value beside the others; never
// rename one.
const (
	TriggerReasonTriggerDeleted        = "trigger_deleted"
	TriggerReasonTriggerPaused         = "trigger_paused"
	TriggerReasonCreatorLeftOrg        = "creator_left_org"
	TriggerReasonNoOrgAccount          = "no_org_account"
	TriggerReasonFilterNoMatch         = "filter_no_match"
	TriggerReasonFilterError           = "filter_error"
	TriggerReasonOriginFilterRejected  = "origin_filter_rejected"
	TriggerReasonOriginFilterError     = "origin_filter_error"
	TriggerReasonLoopGuard             = "loop_guard"
	TriggerReasonNeedsSetup            = "needs_setup"
	TriggerReasonDefinitionBroken      = "definition_broken"
	TriggerReasonConfigInvalid         = "config_invalid"
	TriggerReasonDaemonMissing         = "daemon_missing"
	TriggerReasonCapacityWaiting       = "capacity_waiting"
	TriggerReasonCapacitySuperseded    = "capacity_superseded"
	TriggerReasonCapacityPolicyRemoved = "capacity_policy_removed"
	TriggerReasonExpired               = "expired"
	TriggerReasonPoolWaiting           = "pool_waiting"
	TriggerReasonAlreadyLaunched       = "already_launched"
	TriggerReasonOther                 = "other"
)
