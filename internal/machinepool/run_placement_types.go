// Copyright (c) 2025 Reliant Labs
package machinepool

// A workflow's `daemon: {pool: <name>}` (workflow or node level) is placed
// ONCE, by the workflow runtime, through the
// PlaceRunActivityName activity: the same Decide a pool-bound trigger's fire
// uses (least-loaded member, catalog agreement), the pool's slot guard, and a
// placement record (run_machine_placements) that makes the activity
// idempotent and counts the placement among the member's live runs.

// PlaceRunActivityName is the activity that places a run, or one node of it,
// on a machine pool's member.
const PlaceRunActivityName = "PlaceRunOnMachinePool"

// PoolNotFoundErrorType is the non-retryable ApplicationError type
// PlaceRunActivityName fails with when the run's owner has no pool of the
// requested name. The runtime fails the run with the error's message.
const PoolNotFoundErrorType = "MachinePoolNotFound"

// PlaceRunInput asks for one placement.
type PlaceRunInput struct {
	// ChatID is the run's chat; its user owns the pool and its project is
	// the one whose checkout and catalog the member must hold.
	ChatID string `json:"chat_id"`
	// WorkflowID is the run's workflow id (the workflows row). A placement
	// counts as a live run on its member while that workflow is live.
	WorkflowID string `json:"workflow_id"`
	// Scope is "" for the workflow-level selector, or the node's path for a
	// node-level one. (WorkflowID, Scope) is the placement's identity: asking
	// again returns the recorded member, so a retried activity or a
	// continue-as-new successor never re-places.
	Scope string `json:"scope,omitempty"`
	// Pool is the pool's name. (Labels-only selectors keep their v1
	// per-call meaning and are never placed; an explicit durable-label
	// selector can add a field here later.)
	Pool string `json:"pool,omitempty"`
	// Exclude names members the placement must not use: machines that
	// failed a node's preflight (Node.preflight). A placement recorded on an
	// excluded member moves to another member, keeping its identity and its
	// slot; one on any other member stands. A worker older than Exclude
	// ignores it and answers the recorded member, which the caller's bound
	// on machines tried ends.
	Exclude []string `json:"exclude,omitempty"`
}

// PlaceRunOutput is one placement answer. Exactly one of DaemonID and
// Waiting is set; neither is set only by a stub that places nothing (a
// scenario), and the runtime then leaves the selector unpinned.
type PlaceRunOutput struct {
	// DaemonID is the member the run (or node) is placed on.
	DaemonID string `json:"daemon_id,omitempty"`
	// DaemonName is the member's name, for logs and the run's messages.
	DaemonName string `json:"daemon_name,omitempty"`
	// PoolID and PoolName name the pool.
	PoolID   string `json:"pool_id,omitempty"`
	PoolName string `json:"pool_name,omitempty"`
	// Waiting: no member can take it now (Decision Queue, or Wake, whose
	// wake was asked for). The runtime waits and asks again.
	Waiting bool `json:"waiting,omitempty"`
	// Reason says why it waits (Decision.Reason: WaitingPrefix…), or why
	// the pool is Exhausted.
	Reason string `json:"reason,omitempty"`
	// Exhausted: the pool has members, and every one is in Exclude. No
	// wait helps; the caller fails with Reason.
	Exhausted bool `json:"exhausted,omitempty"`
}
