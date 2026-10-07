package core

import (
	"context"
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
	WorkflowID    string    `json:"workflow_id"`
	ChatID        string    `json:"chat_id"`
	NodeID        string    `json:"node_id"`
	LoopIteration int64     `json:"loop_iteration"`
	UpdatedAt     time.Time `json:"updated_at"`
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

// WorkflowStore is the shared contract for workflow persistence across drivers.
type WorkflowStore interface {
	CreateWorkflow(ctx context.Context, workflow *Workflow) error
	GetWorkflow(ctx context.Context, id string) (*Workflow, error)
	GetWorkflowByThread(ctx context.Context, chatID, thread string) (*Workflow, error)
	ListWorkflowsByChat(ctx context.Context, chatID string) ([]*Workflow, error)
	ListChildWorkflows(ctx context.Context, parentID string) ([]*Workflow, error)
	ListRootWorkflows(ctx context.Context, chatID string) ([]*Workflow, error)
	GetRootWorkflowStatusForChats(ctx context.Context, chatIDs []string) (map[string]WorkflowStatus, error)
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus WorkflowStatus) (bool, error)
	UpdateWorkflowStatus(ctx context.Context, id string, status WorkflowStatus) error
	SetWorkflowOutcome(ctx context.Context, id string, outcome string) error
	UpdateWorkflowName(ctx context.Context, id string, workflowName string) error
	CascadeTerminalStatusToDescendants(ctx context.Context, parentWorkflowID string, reason WorkflowStopReason) error
	ReapOrphanedWorkflowDescendants(ctx context.Context) (int64, error)
	// ReviveSubtreeLiveAt moves every descendant of a root that was live at
	// a reset point back to active, along with the threads those rows own
	// and the root's own thread, and reports how many of each it moved. The
	// inverse of CascadeTerminalStatusToDescendants (and its thread twin)
	// for a subtree a reset-and-replay is bringing back — see
	// queries/workflows.sql for why the predicate is a time window and why
	// both halves are one statement.
	ReviveSubtreeLiveAt(ctx context.Context, rootWorkflowID string, at time.Time) (workflowsRevived, threadsRevived int64, err error)
	DeleteWorkflow(ctx context.Context, id string) error
	DeleteWorkflowsByChat(ctx context.Context, chatID string) error
	ListWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error)
	ListRootWorkflowsByStatus(ctx context.Context, status WorkflowStatus) ([]*Workflow, error)
	PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error
	ResumeWorkflowsByChat(ctx context.Context, chatID string) error
	UpdateWorkflowWorkerStarted(ctx context.Context, workflowID string) error
	UpdateWorkflowWorkerStopped(ctx context.Context, workflowID string) error

	// Position checkpoints (resume-at-position support).
	// GetWorkflowCheckpoint returns (nil, nil) when no checkpoint exists.
	UpsertWorkflowCheckpoint(ctx context.Context, checkpoint *WorkflowCheckpoint) error
	GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*WorkflowCheckpoint, error)
	DeleteWorkflowCheckpoint(ctx context.Context, workflowID string) error

	CreateStepExecution(ctx context.Context, exec *StepExecution) error
	GetStepExecution(ctx context.Context, id string) (*StepExecution, error)
	GetStepExecutionsByWorkflow(ctx context.Context, workflowID string) ([]*StepExecution, error)
	GetStepExecutionsByStep(ctx context.Context, workflowID, stepID string) ([]*StepExecution, error)
	// GetStepExecutionsForChat returns every step of every workflow of one
	// chat in a single query, without OutputJSON. It replaces a
	// per-workflow loop over GetStepExecutionsByWorkflow.
	GetStepExecutionsForChat(ctx context.Context, chatID string) ([]*ChatStepExecution, error)
	// GetBasicStepExecutionsForChat returns only the steps the chat timeline
	// renders: user-facing steps plus their "-save" siblings that recorded a
	// message. The BASIC view of GetWorkflowExecutions.
	GetBasicStepExecutionsForChat(ctx context.Context, chatID string) ([]*ChatStepExecution, error)
	// ListStepExecutionRecordsForChat returns full rows — inputs, output and
	// error — newest first, at most limit. A non-empty nodePath scopes them to
	// that node and everything that ran inside it.
	ListStepExecutionRecordsForChat(ctx context.Context, chatID, nodePath string, limit int) ([]*StepExecution, error)
	DeleteStepExecutionsByWorkflow(ctx context.Context, workflowID string) error

	ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error)
	AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
	RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
}
