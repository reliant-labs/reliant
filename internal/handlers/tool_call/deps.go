// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package tool_call

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the tool_call handlers use. It
// includes db.ToolCallStatusStore (the durable status write) and
// temporal.SpawnWorkflowReconciler (the spawn stopper's reconcile).
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	GetMessage(ctx context.Context, id string) (*db.Message, error)
	GetContentBlockByToolCallID(ctx context.Context, toolCallID string) (*db.MessageContentBlock, error)
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	GetToolCall(ctx context.Context, id string) (*db.ToolCall, error)
	ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*db.ToolCall, error)
	UpsertToolCall(ctx context.Context, call *db.ToolCall) error
	EmitToolCallCancelledUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	EmitToolCallBackgroundedUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
}

// ExecutionSignaler reaches the daemon running one tool execution, addressed
// by tool call id. toolexec.DaemonRouter implements it.
type ExecutionSignaler interface {
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error
	SendToolExecutionBackground(ctx context.Context, userID, requestID, toolCallID string) error
}
