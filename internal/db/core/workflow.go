package core

import (
	"database/sql"
	"time"
)

// Workflow represents a workflow execution in the hierarchy.
type Workflow struct {
	ID              string         `json:"id"`
	ParentID        *string        `json:"parent_id"`
	ChatID          string         `json:"chat_id"`
	WorkflowName    string         `json:"workflow_name"`
	Thread          string         `json:"thread"`
	Status          WorkflowStatus `json:"status"`
	SpawnedByNodeID *string        `json:"spawned_by_node_id"`
	LoopIteration   *int64         `json:"loop_iteration"`
	CreatedAt       time.Time      `json:"created_at"`
	CompletedAt     *time.Time     `json:"completed_at,omitempty"`
	WorkerStartedAt *time.Time     `json:"worker_started_at,omitempty"`
	WorkerStoppedAt *time.Time     `json:"worker_stopped_at,omitempty"`
	// StartFailedAt is when the last attempt to start this PENDING run in
	// Temporal failed, and nil while no failed attempt is outstanding. It is
	// what tells a stranded launch (PENDING, set) from a start in flight
	// (PENDING, nil): the launcher sets it on a failed start and clears it
	// when a retry of that start begins, and the reconciler sets it on a
	// PENDING root whose run never reached Temporal within its bound. It means
	// nothing once the run has left PENDING.
	StartFailedAt *time.Time `json:"start_failed_at,omitempty"`
	// OwnerUserID is who this run belongs to.
	//
	// A run has to know its own owner: activities need an identity to load API
	// keys and attribute spend, and today they get one by re-reading the chat
	// (call_llm.go, compact.go). That only works because every run has a chat,
	// which is the coupling the engine split removes — a triggered or
	// API-started run has no conversation to borrow from.
	//
	// nil on rows written before this column existed, and on runs whose chat
	// was deleted. Readers must therefore still fall back to the chat while
	// both sources exist; see the migration for the ordering.
	OwnerUserID *string `json:"owner_user_id,omitempty"`

	// Outcome is the run's own verdict — "success" or "failure" — stamped by
	// the terminal node it reached (Node.outcome in the workflow YAML).
	// Orthogonal to Status: Status is the Temporal-owned lifecycle and is
	// reconciled against Temporal, so it cannot hold a workflow-semantic
	// judgement. nil means the workflow declared no outcome, which is NOT a
	// failure — most workflows declare nothing.
	Outcome *string `json:"outcome,omitempty"`
}

// WorkflowCheckpoint records the position a workflow run has reached: the last
// top-level node it entered and, for loop nodes, the loop iteration in flight.
// It is the position truth used to resume an interrupted (failed/terminated)
// run at position when the next user message starts a fresh Temporal run.
// One row per workflow ID (workflow IDs are reused across runs for a chat).
type WorkflowCheckpoint struct {
	WorkflowID    string `json:"workflow_id"`
	ChatID        string `json:"chat_id"`
	NodeID        string `json:"node_id"`
	LoopIteration int64  `json:"loop_iteration"`
	// CatalogPin is the workflow catalog the run at this position started
	// with (db.Repo.PinProjectCatalog); "" for a run with no pin.
	CatalogPin string    `json:"catalog_pin,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// StepExecution represents a single execution of a workflow step.
type StepExecution struct {
	ID            string         `json:"id"`
	WorkflowID    string         `json:"workflow_id"`
	StepID        string         `json:"step_id"`
	ActivityName  string         `json:"activity_name"`
	OutputJSON    sql.NullString `json:"output_json"`
	ExitCode      sql.NullInt64  `json:"exit_code"`
	Success       sql.NullBool   `json:"success"`
	DurationMs    sql.NullInt64  `json:"duration_ms"`
	LoopNodeID    sql.NullString `json:"loop_node_id"`
	LoopIteration sql.NullInt64  `json:"loop_iteration"`
	CreatedAt     time.Time      `json:"created_at"`

	// SavedMessageID is the message a "-save" step wrote, derived by the
	// database from OutputJSON (a generated column — see migration
	// 20260929162546). Read-only: it is ignored on write, because the
	// database computes it.
	//
	// It exists so a reader can answer "did this step save a message, and
	// which one" without loading OutputJSON, which is TOASTed and can be
	// megabytes per row.
	SavedMessageID sql.NullString `json:"saved_message_id"`

	// The debugging record of this attempt (migration 20261007011605): what
	// the step was given and how it ended. NULL on rows written before it.
	//
	// InputJSON is the node's resolved args as JSON, bounded by the writer;
	// NULL for activities that are not graph nodes.
	InputJSON sql.NullString `json:"input_json"`
	// ErrorMessage is why this attempt failed; NULL when it did not.
	ErrorMessage sql.NullString `json:"error_message"`
	// Attempt is Temporal's 1-based attempt number. A retried step writes one
	// row per attempt.
	Attempt sql.NullInt32 `json:"attempt"`
	// NodePath is the node's dotted graph position ("agent.agent_loop.call_llm").
	NodePath sql.NullString `json:"node_path"`
}

// ChatStepExecution is one step of one workflow of a chat, as the chat's
// execution tree needs it.
//
// A separate type from StepExecution on purpose, because OutputJSON means
// something narrower here: it is populated only for user-facing activities,
// and is deliberately absent for the internal-plumbing steps that make up
// virtually all of the rows (see model.InternalActivities). Reusing
// StepExecution would make "is OutputJSON populated?" depend on which query
// produced the value, with nothing in the type to say so.
type ChatStepExecution struct {
	ID            string
	WorkflowID    string
	StepID        string
	ActivityName  string
	ExitCode      sql.NullInt64
	Success       sql.NullBool
	DurationMs    sql.NullInt64
	LoopNodeID    sql.NullString
	LoopIteration sql.NullInt64
	CreatedAt     time.Time

	// SavedMessageID is the message a "-save" step wrote, if it wrote one.
	SavedMessageID sql.NullString

	// OutputJSON is the step's raw output, populated ONLY for user-facing
	// activities. Empty for every internal activity by design — not because
	// the row had no output. See model.InternalActivities.
	OutputJSON string
}

// ReapedWorkflow is one workflow ReapOrphanedWorkflowDescendants closed: the
// workflow, its chat, and the stop reason it inherited from its terminal
// ancestor.
type ReapedWorkflow struct {
	WorkflowID string
	ChatID     string
	StopReason WorkflowStopReason
}
