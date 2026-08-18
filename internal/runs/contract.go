// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"

	"go.temporal.io/api/workflowservice/v1"

	"github.com/reliant-labs/reliant/internal/db"
)

// ErrChatNotFound is returned when the chat a lifecycle call names does not
// exist.
var ErrChatNotFound = errors.New("chat not found")

// ErrNoWorkflow is returned when a chat has never started a root workflow, so
// there is no run to stop or restart.
var ErrNoWorkflow = errors.New("chat has no workflow")

// Repository is the subset of db.Repository this service needs.
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	UpdateChat(ctx context.Context, chat *db.Chat) error
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	UpdateWorkflowStatus(ctx context.Context, id string, status db.WorkflowStatus) error
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	DeleteWorkflowCheckpoint(ctx context.Context, workflowID string) error
	CascadeTerminalStatusToDescendants(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error
	CascadeTerminalStatusToThreadSubtree(ctx context.Context, workflowID string, reason db.WorkflowStopReason) error
	GetPendingQuestionByChatID(ctx context.Context, chatID string) (*db.Question, error)
	ResolveQuestion(ctx context.Context, id string, responseData *string) error
	EmitQuestionUpdate(ctx context.Context, chatID string, update db.QuestionUpdate) error
}

// TemporalClient is the subset of the Temporal SDK client this service needs.
// Temporal is the source of truth for execution state; the database row is a
// cache of it.
type TemporalClient interface {
	DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

// TemporalTerminator is implemented by Temporal clients that can hard-stop an
// execution. It stays separate from TemporalClient so tests and callers that
// only inspect or resume runs do not need to implement termination machinery.
type TemporalTerminator interface {
	TerminateWorkflow(ctx context.Context, workflowID, runID, reason string, details ...interface{}) error
}

// PauseController is the signal/reset machinery this service drives, satisfied
// by *workflow.PauseService.
//
// This service decides WHICH of these to call and what the result means to a
// caller; the controller owns the Temporal mechanics of each — signalling a
// live run, resetting a dead one, and honoring the bounded reset guard.
type PauseController interface {
	PauseWorkflow(ctx context.Context, workflowID, chatID, reason string) error
	ResumeWorkflow(ctx context.Context, workflowID, chatID string) error
	ResumeInterruptedWorkflow(ctx context.Context, workflowID, chatID string) (string, error)
	SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}) error
}

// OutcomeKind is what a resume attempt settled as, in terms a caller can
// render. It deliberately says nothing about how the outcome was reached —
// whether the run was signalled in place, reset and replayed, or found already
// gone is this package's business.
type OutcomeKind int

const (
	// OutcomeResumed means the run is executing again. ResumeOutcome.RunID
	// holds the authoritative run id, re-read from Temporal because a reset
	// mints a new one.
	OutcomeResumed OutcomeKind = iota

	// OutcomeNeedsRecovery means Temporal no longer has the execution. The
	// conversation's messages are intact but the run is gone, so the caller
	// either prompts the user to start a new conversation or starts a fresh
	// run itself.
	OutcomeNeedsRecovery

	// OutcomeUnresumable means the run is stuck: the database records it as
	// failed while Temporal still reports it running. Nothing can reconcile
	// that, so the run cannot be restarted and the user must branch.
	OutcomeUnresumable

	// OutcomeNeedsRestart means a precise resume was attempted and could not
	// be served, so the caller should fall back to starting a new run at the
	// coarse checkpoint. This is not an error: it is the documented fallback
	// for a run with no replayable history, one the reset guard has given up
	// on, or one sitting at Temporal's history cap.
	OutcomeNeedsRestart
)

// ResumeOutcome is the result of a resume attempt.
type ResumeOutcome struct {
	Kind OutcomeKind

	// WorkflowID is the run's workflow id, echoed so a caller that reached
	// this service by chat id can render it without a second lookup.
	WorkflowID string

	// RunID is the authoritative Temporal run id, set on OutcomeResumed.
	RunID string

	// HistoryLimitExceeded marks an OutcomeNeedsRestart caused by the run
	// exhausting Temporal's per-execution history limit, rather than by
	// missing history or an exhausted reset guard.
	//
	// It is surfaced because a caller must TELL the user about this one:
	// resetting cannot rescue such a run (the reset forks from inside the
	// oversized history and dies within a few events), so the chat simply
	// stops with no explanation and sending a message appears to do nothing.
	HistoryLimitExceeded bool
}

// RunState is what Temporal reports about an execution.
type RunState struct {
	// Exists is false when Temporal has no such execution — it expired past
	// retention, was lost, or never started. Every other field is meaningless
	// when it is false.
	Exists bool

	// Status is the Temporal status mapped onto our own vocabulary.
	Status db.WorkflowStatus

	// RunID is the current run id.
	RunID string

	// IsRunning reports that Temporal considers the execution open. An open
	// execution is not necessarily making progress — a run wedged with no
	// worker still reports running, which is exactly the state the stuck
	// check exists to catch.
	IsRunning bool
}

// Inspection classifies a run for a caller that must choose between several
// recovery paths rather than simply resuming.
type Inspection struct {
	// WorkflowID is the run's workflow id.
	WorkflowID string

	// Stuck reports the one state no resume can serve: the database records
	// the run as failed while Temporal still reports it running. The caller
	// must refuse and point the user at branching.
	Stuck bool

	// Recoverable reports a closed Temporal execution that reset-and-replay
	// may be able to rebuild. False means Temporal has nothing to replay — a
	// ghost, or an execution past retention — and only a fresh run at the
	// coarse checkpoint can continue the conversation.
	Recoverable bool
}

// ResumeViaSignalInput asks for a run to be woken by a domain signal instead of
// signal.resume.
//
// A run that died parked on an unanswered question does not wake on
// signal.resume — it wakes on the question's own channel. Delivering the answer
// through this path reset-replays the dead execution and re-sends the signal on
// the rebuilt run, which re-parks on the same deterministic channel and receives
// it, preserving nested inline state that a coarse restart would lose.
type ResumeViaSignalInput struct {
	// ChatID is the chat whose run id is refreshed on success.
	ChatID string

	// WorkflowID is the chat's root workflow, whose status is marked running
	// and whose run id is written back to the chat.
	WorkflowID string

	// TargetWorkflowID is the Temporal workflow the signal is addressed to.
	// For a nested parked question this is the sub-workflow that owns the
	// question channel, which is not necessarily WorkflowID.
	TargetWorkflowID string

	// SignalName is the deterministic channel name the parked run awaits.
	SignalName string

	// SignalData is the signal payload.
	SignalData interface{}
}
