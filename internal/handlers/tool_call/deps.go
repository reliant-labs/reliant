// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package tool_call

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Repository is the slice of *db.Repo the tool_call handlers use. It
// includes db.ToolCallStatusStore (the durable status write) and
// temporal.SpawnWorkflowReconciler (the spawn stopper's reconcile).
type Repository interface {
	GetMessage(ctx context.Context, id string) (*db.Message, error)
	GetContentBlockByToolCallID(ctx context.Context, toolCallID string) (*db.MessageContentBlock, error)
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	CancelStoppedSpawn(ctx context.Context, workflowID string) (bool, error)
	GetToolCall(ctx context.Context, id string) (*db.ToolCall, error)
	ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*db.ToolCall, error)
	UpsertToolCall(ctx context.Context, call *db.ToolCall) error
	EmitToolCallCancelledUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	EmitToolCallBackgroundedUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
}

// Chats reads the chat a tool call belongs to: its owner for the access check,
// its workflow id for a spawn's stop. store.ChatExtStore satisfies it.
type Chats interface {
	GetChatRow(ctx context.Context, id string) (*core.ChatRow, error)
}

// ExecutionSignaler reaches the daemon running one tool execution, addressed
// by tool call id. toolexec.DaemonRouter implements it.
type ExecutionSignaler interface {
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error
	SendToolExecutionBackground(ctx context.Context, userID, requestID, toolCallID string) error
}
