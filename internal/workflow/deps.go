// forge:deps — consumer-declared interfaces for this package's Deps.
// PauseStore is this package's own declaration of what PauseService writes;
// mock_gen.go mocks it. *db.Repo satisfies it, and the composition root hands
// it to NewPauseService. Workflow rows are read through WorkflowLoader.
package workflow

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
)

// PauseStore is what pausing, resuming and terminating a run writes to the
// database alongside its Temporal signal.
type PauseStore interface {
	UpdateWorkflowStatus(ctx context.Context, id string, status db.WorkflowStatus) error
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	PauseRunningWorkflowsByChat(ctx context.Context, chatID string) error
	ResumeWorkflowsByChat(ctx context.Context, chatID string) error
	SetChatPausedForMachine(ctx context.Context, chatID string, paused bool) (bool, error)
	ReviveSubtreeLiveAt(ctx context.Context, rootWorkflowID string, at time.Time) (workflowsRevived, threadsRevived int64, err error)
	CascadeTerminalStatusToDescendants(ctx context.Context, parentWorkflowID string, reason db.WorkflowStopReason) error
	CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
}

// WorkflowLoader reads a workflow row by id. store.WorkflowExtStore
// satisfies it.
type WorkflowLoader interface {
	LoadWorkflow(ctx context.Context, id string) (*db.Workflow, error)
}
