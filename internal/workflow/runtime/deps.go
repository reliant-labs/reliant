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
// activity it runs: the stopped-run check, the liveness write, the step row,
// the node event and the error event.
type ActivityStore interface {
	// GetWorkflow is lifecycle.WorkflowReader: the store is handed to
	// lifecycle.MayExecuteWork for the stopped-run check.
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	EnsureWorkflowRunning(ctx context.Context, workflowID, chatID string)
	CreateStepExecution(ctx context.Context, exec *db.StepExecution) error
	EmitNodeExecutionEvent(ctx context.Context, eventType string, state *db.NodeExecutionState) error
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
}
