// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package runtime

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// ActivityStore is what the ActivityWrapper writes and reads around every
// activity it runs: the liveness write, the step row, the node event and the
// error event. The stopped-run check reads through WorkflowRows.
type ActivityStore interface {
	EnsureWorkflowRunning(ctx context.Context, workflowID, chatID string)
	CreateStepExecution(ctx context.Context, exec *db.StepExecution) error
	EmitNodeExecutionEvent(ctx context.Context, eventType string, state *db.NodeExecutionState) error
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}

// WorkflowRows reads a workflow row by id: the stopped-run check and the
// database-outage probe. store.WorkflowExtStore satisfies it.
type WorkflowRows interface {
	LoadWorkflow(ctx context.Context, id string) (*db.Workflow, error)
}
