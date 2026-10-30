// forge:deps — consumer-declared interfaces for this package's Deps.
// ReconcilerStore is this package's own declaration of what the Reconciler
// reads and writes; mock_gen.go mocks it. *db.Repo satisfies it, and the
// composition root hands it to NewReconciler.
//
// It lists the methods the reconciler calls directly, then embeds the role of
// every callee it hands the store to (the activities/handlers completion
// helpers, runevents.EmitTerminal, db.UpsertToolCallStatus), so a callee that
// needs more points the compiler here.
package reconciliation

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// ReconcilerStore is everything a reconcile pass reads and writes.
type ReconcilerStore interface {
	// Workflow rows: the drift scan, CAS terminal transitions, and the
	// checkpoint a terminal repair keeps or drops.
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	ListWorkflowsByStatus(ctx context.Context, status db.WorkflowStatus) ([]*db.Workflow, error)
	ListWorkflowsByChat(ctx context.Context, chatID string) ([]*db.Workflow, error)
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*db.WorkflowCheckpoint, error)
	DeleteWorkflowCheckpoint(ctx context.Context, workflowID string) error
	CascadeTerminalStatusToDescendants(ctx context.Context, parentWorkflowID string, reason db.WorkflowStopReason) error
	CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error

	// Launches whose start never reached Temporal.
	ListStrandedWorkflowStarts(ctx context.Context, cutoff time.Time, limit int) ([]*db.Workflow, error)
	MarkWorkflowStartFailed(ctx context.Context, id string) (bool, error)

	// Orphan reaps.
	ReapOrphanedWorkflowDescendants(ctx context.Context) ([]db.ReapedWorkflow, error)
	ReapOrphanedThreads(ctx context.Context) ([]db.ReapedThread, error)

	// Chats, threads and the progress watchdog's user-input waits.
	GetThread(ctx context.Context, id string) (*db.Thread, error)
	ListPendingQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)

	// Stranded spawns, backgrounded processes and the tool-call rows they
	// close.
	ListStrandedSpawnToolCalls(ctx context.Context) ([]*db.ToolCall, error)
	ListStrandedBackgroundSpawnToolCalls(ctx context.Context) ([]*db.StrandedBackgroundSpawn, error)
	ListBackgroundedProcessToolCalls(ctx context.Context) ([]*db.BackgroundedProcessCall, error)
	UpsertToolCallResult(ctx context.Context, chatID string, result *db.ToolCallResult) error
	EmitToolCallUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	// Which chats owning a still-running backgrounded process are held on a
	// provider usage limit (reassertHeldRunWork).
	ChatsHeldOnUsageLimit(ctx context.Context, chatIDs []string, since time.Time) ([]string, error)

	// The agent-message mailbox.
	EnqueueAgentMessageIfAbsent(ctx context.Context, msg *db.AgentMessage) (bool, error)
	ListThreadsWithOrphanedAgentMessages(ctx context.Context) ([]string, error)
	MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, toThreadID string) (int64, error)

	// GetChat, GetToolCall and SaveMessageToThread are called directly too;
	// the roles below already declare them.

	// handlers.TransitionChatOnCompletion, EmitTransitionMessage and
	// WriteWorkflowError.
	handlers.ChatTransitioner
	handlers.TransitionMessageWriter
	handlers.WorkflowErrorWriter
	// runevents.EmitTerminal, inside the terminal transition's transaction.
	runevents.Store
	// db.UpsertToolCallStatus, for the stranded and backgrounded calls.
	db.ToolCallStatusStore
}
