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
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// Approvals is what the approval sweep reads and writes: approvals nothing
// waits on any more, and closed approvals whose chat feed still says pending
// (approval_sweep.go). store.ApprovalExtStore reads them; approvalclose.Closer
// closes and announces.
type Approvals interface {
	ListStaleApprovals(ctx context.Context, limit int) ([]store.StaleApproval, error)
	ListUnannouncedApprovalResolutions(ctx context.Context, limit int) ([]*db.Approval, error)
	ResolvePendingApproval(ctx context.Context, id string, status int32, reason, actionTaken string) (bool, error)
	AnnounceApprovalResolution(ctx context.Context, a *db.Approval) error
}

// Questions reads a chat's open blocking questions: the progress watchdog
// never terminates a run that is parked on one. store.QuestionExtStore
// satisfies it.
type Questions interface {
	ListPendingQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
}

// Mailbox writes the placeholder report the stranded-background-spawn sweep
// leaves for a spawn whose report never arrived, and announces it to the
// chat's clients. *agentmailbox.Mailbox satisfies it.
type Mailbox interface {
	EnqueueAgentMessageIfAbsent(ctx context.Context, msg *db.AgentMessage) (bool, error)
}

// Orphans resolves the queued mail of threads that are already terminal.
// store.AgentMessageExtStore satisfies it.
type Orphans interface {
	ListThreadsWithOrphanedAgentMessages(ctx context.Context) ([]string, error)
	MarkQueuedAgentMessagesUndeliveredForThread(ctx context.Context, toThreadID string) (int64, error)
}

// Deps is what a Reconciler is built from.
type Deps struct {
	// Store is everything a reconcile pass reads and writes.
	Store ReconcilerStore
	// Questions lists the chat's pending questions.
	Questions Questions
	// Mailbox writes the sweep's placeholder reports.
	Mailbox Mailbox
	// Orphans resolves the mail a finished thread left queued.
	Orphans Orphans
	// Temporal is the Temporal client the drift scan and the resets use.
	Temporal client.Client
	// Config holds the pass's thresholds; nil, or a zero field, is the
	// DefaultConfig value.
	Config *ReconcilerConfig
	// Workflows reads a workflow row by id.
	Workflows WorkflowLoader
	// Daemons reads the daemon a backgrounded process runs on.
	Daemons DaemonReader
	// Chats reads a chat's owner, activity, main thread and run, with no
	// owner check. store.ChatsWithActivityExtStore satisfies it.
	Chats Chats
	// ToolCalls lists the stranded spawns and backgrounded processes the
	// sweeps repair. store.ToolCallExtStore satisfies it.
	ToolCalls ToolCallLists
	// RunEvents records a repaired run's terminal event, inside the repair's
	// transaction (runevents.NewStore).
	RunEvents runevents.Store
	// Runs stops one exact execution of a chat's run: the stall watchdog's
	// enforce mode (runs.Service.TerminateExecution). nil: enforce reports.
	// forge:optional-dep
	Runs RunTerminator
	// Observations is where the stall watchdog records each stalled run
	// (node_observations, source "reconciler"). nil: logs and metrics only.
	// forge:optional-dep
	Observations StallRecorder
	// DataConverter decodes a run's start input, from which the stall
	// watchdog learns the run id its record counts under: the Temporal
	// client's own converter, claim-check store included. nil: a converter
	// with no store, which decodes every input below the claim-check
	// threshold and fails (the record then names no run) on one above it.
	// forge:optional-dep
	DataConverter converter.DataConverter
}

// RunTerminator stops exactly one execution of a chat's run through the run
// lifecycle's cancel path (its finally: runs), refusing with
// runs.ErrExecutionNotCurrent when runID is no longer the chat's current,
// running execution. runs.Service satisfies it.
type RunTerminator interface {
	TerminateExecution(ctx context.Context, chatID, workflowID, runID string) error
}

// StallRecorder writes the stall watchdog's durable record of a stalled
// run, one row per run, streak and outcome. *db.Repo satisfies it.
type StallRecorder interface {
	RecordNodeObservation(ctx context.Context, obs *db.NodeObservation, maxPerRun int) (db.NodeObservationWrite, error)
}

// ToolCallLists lists the tool-call rows the stranded-spawn and
// backgrounded-process sweeps act on.
type ToolCallLists interface {
	ListStrandedSpawnToolCalls(ctx context.Context) ([]*db.ToolCall, error)
	ListStrandedBackgroundSpawnToolCalls(ctx context.Context) ([]*db.StrandedBackgroundSpawn, error)
	ListBackgroundedProcessToolCalls(ctx context.Context) ([]*db.BackgroundedProcessCall, error)
}

// Chats reads a chat with its computed state.
type Chats interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// WorkflowLoader reads a workflow row by id. store.WorkflowExtStore
// satisfies it.
type WorkflowLoader interface {
	LoadWorkflow(ctx context.Context, id string) (*db.Workflow, error)
}

// DaemonReader reads a daemon row by id. store.DaemonExtStore satisfies it.
type DaemonReader interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// ReconcilerStore is everything a reconcile pass reads and writes.
type ReconcilerStore interface {
	// Workflow rows: the drift scan, CAS terminal transitions, and the
	// checkpoint a terminal repair keeps or drops.
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
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)

	// The tool-call rows stranded spawns and backgrounded processes close.
	UpsertToolCallResult(ctx context.Context, chatID string, result *db.ToolCallResult) error
	EmitToolCallUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error

	// GetToolCall and SaveMessageToThread are called directly too (and GetChat,
	// through handlers.ChatTransitioner);
	// the roles below already declare them.

	// handlers.TransitionChatOnCompletion, EmitTransitionMessage and
	// WriteWorkflowError.
	handlers.ChatTransitioner
	handlers.TransitionMessageWriter
	handlers.WorkflowErrorWriter
	// db.UpsertToolCallStatus, for the stranded and backgrounded calls.
	db.ToolCallStatusStore
}
