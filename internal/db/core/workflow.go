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
	UpdateWorkflowName(ctx context.Context, id string, workflowName string) error
	CompleteChildWorkflows(ctx context.Context, parentWorkflowID string) error
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
	DeleteStepExecutionsByWorkflow(ctx context.Context, workflowID string) error

	ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error)
	AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
	RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
}
