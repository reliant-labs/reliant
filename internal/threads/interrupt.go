package threads

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

const interruptToolCancelReason = "user interrupted the agent"

var (
	// ErrInvalidArgument means the interrupt request was malformed.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrNotFound means the addressed chat or thread does not exist for the caller.
	ErrNotFound = errors.New("not found")
	// ErrInterruptUndeliverable means the workflow could not be told to observe the interrupt.
	ErrInterruptUndeliverable = errors.New("interrupt undeliverable")
)

// ServiceOption configures a threads Service.
type ServiceOption func(*Service)

// TemporalSignaler is the Temporal signal operation needed to wake a thread.
type TemporalSignaler interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, signalArg interface{}) error
}

// ToolCanceler is the daemon operation needed to cancel one running tool execution.
type ToolCanceler interface {
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error
}

// WithTemporalSignaler wires the workflow signal delivery used by InterruptThread.
func WithTemporalSignaler(signaler TemporalSignaler) ServiceOption {
	return func(s *Service) {
		s.temporalSignaler = signaler
	}
}

// WithToolCanceler wires the daemon cancel delivery used by InterruptThread.
func WithToolCanceler(canceler ToolCanceler) ServiceOption {
	return func(s *Service) {
		s.toolCanceler = canceler
	}
}

// InterruptThreadOpts contains the target thread and authenticated caller.
type InterruptThreadOpts struct {
	UserID   string
	ChatID   string
	ThreadID string
}

// InterruptThreadResult reports what the interrupt actually stopped.
type InterruptThreadResult struct {
	CancelledToolCalls     int
	UndeliverableToolCalls []string
}

type validationError struct{ message string }

func (e validationError) Error() string { return e.message }
func (e validationError) Unwrap() error { return ErrInvalidArgument }

type notFoundError struct{ message string }

func (e notFoundError) Error() string { return e.message }
func (e notFoundError) Unwrap() error { return ErrNotFound }

// InterruptThread stops the work in flight on a thread so the agent reads its
// mailbox now rather than after finishing what it started.
//
// # Why this is not "pause, then send"
//
// Pause looks like it would do the job -- pauseCoordinator.cancelAll() cancels
// every in-flight activity at any nesting depth, immediately, from a background
// goroutine. But it cancels the whole ExecuteTools ACTIVITY, which then returns
// a temporal.CanceledError, and StepExecutor.handleActivityCompletion takes an
// early return on that error BEFORE running the step's save_message. The
// tool_results are never persisted, and the thread is left holding an assistant
// message with tool_calls and no results -- the exact dangling state that
// deadlocks providers, and the reason InterruptedToolResultContent exists.
//
// Pause survives that because resume re-runs the whole step. Interrupt cannot:
// the point is to abandon the work, not repeat it.
//
// So interrupt cancels each tool INDIVIDUALLY, exactly as CancelToolCall does.
// The activity keeps running, every tool -- cancelled or not -- produces a
// durable outcome and a real tool_result, and the activity returns normally so
// save_message persists them. History stays valid by construction. This is
// pinned by TestInterrupt_* in
// internal/workflow/runtime/activities/handlers/execute_tools_interrupt_test.go.
//
// # Why it takes no message id
//
// Interrupt is an action on the THREAD. It stops the work; the next call_llm
// delivers whatever the mailbox holds, oldest first. Modelling it as a property
// of one queued message would mean updating that row, which races the delivery
// the update is trying to beat -- and would be ambiguous about the other
// messages already queued. Queue first, then interrupt, and the ordering the
// user typed is preserved with no window to lose.
func (s *Service) InterruptThread(ctx context.Context, opts InterruptThreadOpts) (InterruptThreadResult, error) {
	if opts.ChatID == "" {
		return InterruptThreadResult{}, validationError{message: "chat_id is required"}
	}
	if opts.ThreadID == "" {
		return InterruptThreadResult{}, validationError{message: "thread_id is required"}
	}
	if opts.UserID == "" {
		return InterruptThreadResult{}, validationError{message: "user_id is required"}
	}

	// Ownership: the same two checks, and the same indistinguishable NotFound,
	// as SendAgentMessage and ClaimQueuedAgentMessages. Revealing that a thread
	// exists but belongs to another chat is an enumeration leak.
	chat, err := s.repo.GetChatWithUserCheck(ctx, opts.ChatID, opts.UserID)
	if err != nil || chat == nil {
		return InterruptThreadResult{}, notFoundError{message: "chat not found"}
	}
	target, err := s.repo.GetThread(ctx, opts.ThreadID)
	if err != nil || target == nil {
		return InterruptThreadResult{}, notFoundError{message: "thread not found"}
	}
	if target.ChatID != opts.ChatID {
		return InterruptThreadResult{}, notFoundError{message: "thread not found"}
	}

	// Kill the tools BEFORE freeing the workflow to re-dispatch. Signalling
	// first would let the successor step start while the predecessor's
	// process is still provably alive on the daemon -- tools are not
	// idempotent, so a re-entered call is a correctness bug, not a race we
	// can shrug off. Read in-flight calls, push the daemon cancels, and only
	// then tell the workflow it may proceed.
	executing, err := s.inFlightToolCallsForThread(ctx, opts.ChatID, opts.ThreadID)
	if err != nil {
		logging.Error("Failed to read executing tool calls for interrupt",
			"error", err, "chatID", opts.ChatID, "threadID", opts.ThreadID)
		return InterruptThreadResult{}, fmt.Errorf("failed to read tool calls: %w", err)
	}

	outcome := s.cancelToolCalls(ctx, opts.UserID, executing, interruptToolCancelReason)
	result := InterruptThreadResult{
		CancelledToolCalls:     outcome.CancelledToolCalls,
		UndeliverableToolCalls: outcome.UndeliverableToolCalls,
	}

	// A cancel push that failed to reach the daemon must not strand the
	// workflow un-interrupted -- signal regardless of outcome above.
	if err := s.signalThreadInterrupt(ctx, chat, opts.ThreadID); err != nil {
		return InterruptThreadResult{}, err
	}

	logging.Info("Interrupted thread",
		"chatID", opts.ChatID,
		"threadID", opts.ThreadID,
		"cancelledToolCalls", result.CancelledToolCalls,
		"undeliverable", len(result.UndeliverableToolCalls),
	)

	// Nothing here delivers the mailbox. The next call_llm does that, as it
	// does on every turn -- interrupt's only job is to make that call happen
	// sooner by ending the work in front of it.
	return result, nil
}

func (s *Service) signalThreadInterrupt(ctx context.Context, chat *db.Chat, threadID string) error {
	if s.temporalSignaler == nil {
		return nil
	}
	workflowID := chat.ID
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		workflowID = *chat.WorkflowID
	}
	if err := s.temporalSignaler.SignalWorkflow(ctx, workflowID, "", v2.InterruptThreadSignalName, v2.InterruptThreadSignal{
		ThreadID: threadID,
	}); err != nil {
		return fmt.Errorf("%w: signal workflow %s: %v", ErrInterruptUndeliverable, workflowID, err)
	}
	return nil
}

// CancelledToolResultContent is what a cancelled call reports as its result.
// The LLM reads this, so it says plainly that the user stopped the tool and
// that its effects are unknown -- a tool cancelled mid-flight may have done
// some, all or none of its work.
const CancelledToolResultContent = "Tool execution cancelled by user. The tool was stopped before it reported a result; its effects are unknown, so verify before re-running it."

// recordToolCallCancelled writes the terminal Cancelled status AND the
// cancelled result in one transaction, so no reader ever sees a call marked
// terminal with no result to show.
//
// Best-effort: a bookkeeping failure must not stop the daemon push that
// actually kills the tool. The activity still writes its own outcome when it
// unwinds, so a failure here costs immediacy, not correctness.
//
// The context is detached from cancellation for the same reason
// execute_tools' terminal writes are: this runs on a request path the caller
// may abandon the instant it has its response, and a write that loses its
// context leaves the row EXECUTING forever -- the exact state this exists to
// prevent.
func (s *Service) recordToolCallCancelled(ctx context.Context, call *db.ToolCall, reason string) {
	if s.repo == nil || call == nil {
		return
	}

	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelRecordTimeout)
	defer cancel()

	now := time.Now().UTC()
	errMsg := reason
	updated := *call
	updated.Status = core.ToolCallStatusCancelled
	updated.CompletedAt = &now
	updated.UpdatedAt = now
	if errMsg != "" {
		updated.ErrorMessage = &errMsg
	}

	if err := s.repo.RunTx(detached, func(txCtx context.Context) error {
		if err := s.repo.UpsertToolCall(txCtx, &updated); err != nil {
			return err
		}
		return s.repo.UpsertToolCallResult(txCtx, &db.ToolCallResult{
			ToolCallID: call.ID,
			Content:    CancelledToolResultContent,
			IsError:    true,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}); err != nil {
		logging.Warn("Could not record tool cancellation durably; the activity will write it when it unwinds",
			"error", err, "toolCallID", call.ID)
	}
}

// cancelRecordTimeout bounds the durable cancel write. Short: two upserts on a
// path the user is waiting on.
const cancelRecordTimeout = 5 * time.Second

// inFlightToolCallsForThread returns the thread's tool calls that are still
// in flight as far as the user is concerned -- PENDING (recorded, not yet
// dispatched) or EXECUTING. PENDING counts: execute_tools writes it
// immediately before handing the call to the executor, and EXECUTING is
// written only after, so there is a real window between the two writes. A
// call sitting in that window (e.g. a queued spawn_status(wait:true)) is work
// the user asked to stop just as much as one already running -- skipping it
// left interrupts that landed there cancelling nothing (chat b7cd65c6).
//
// Scoped to the thread, not the chat: a chat can have several threads working
// at once (spawned sub-agents run inline alongside the root), and interrupting
// one must not stop the others. A call whose ThreadID is nil is skipped rather
// than assumed to belong here -- ThreadID is nullable because a call can be
// recorded before the assistant message carrying it is finalized, and guessing
// would cancel work on a thread the user did not interrupt.
func (s *Service) inFlightToolCallsForThread(ctx context.Context, chatID, threadID string) ([]*db.ToolCall, error) {
	calls, err := s.inFlightToolCallsForChat(ctx, chatID)
	if err != nil {
		return nil, err
	}

	var inFlight []*db.ToolCall
	for _, call := range calls {
		if call.ThreadID == nil || *call.ThreadID != threadID {
			continue
		}
		inFlight = append(inFlight, call)
	}
	return inFlight, nil
}

// inFlightToolCallsForChat returns every tool call across every thread of the
// chat that is still in flight -- PENDING or EXECUTING. See
// inFlightToolCallsForThread for why both statuses count. This is the
// chat-scoped sibling: pause's scope is the whole chat (all threads), where
// interrupt's is one thread -- the only difference the spec calls for.
func (s *Service) inFlightToolCallsForChat(ctx context.Context, chatID string) ([]*db.ToolCall, error) {
	calls, err := s.repo.ListToolCallsByChat(ctx, chatID)
	if err != nil {
		return nil, err
	}

	var inFlight []*db.ToolCall
	for _, call := range calls {
		if call.Status != core.ToolCallStatusPending && call.Status != core.ToolCallStatusExecuting {
			continue
		}
		inFlight = append(inFlight, call)
	}
	return inFlight, nil
}

// cancelOutcome is the shared result of pushing a cancel to a batch of tool
// calls, before it is wrapped in the caller's own result type
// (InterruptThreadResult, CancelChatToolCallsResult).
type cancelOutcome struct {
	CancelledToolCalls     int
	UndeliverableToolCalls []string
}

// cancelToolCalls pushes a daemon cancel for each call, individually -- the
// mechanism both InterruptThread and CancelChatToolCalls use, so pause and
// interrupt genuinely share it rather than each keeping their own copy of
// the loop.
func (s *Service) cancelToolCalls(ctx context.Context, userID string, calls []*db.ToolCall, reason string) cancelOutcome {
	var outcome cancelOutcome
	for _, call := range calls {
		// Record the cancellation durably FIRST, before the daemon push.
		//
		// The cancel is immediate and the resume is not: nobody knows when, or
		// whether, the activity that owns this call will next run to notice it
		// stopped. Waiting for that to write the row means the user stares at a
		// tool still spinning long after they asked it to stop -- and under
		// pause, possibly forever.
		//
		// Writing it here also closes the re-entry hole. The row becomes
		// TERMINAL (Cancelled), which is exactly what checkPriorTerminalResult
		// looks for, so a re-dispatch of this SAME tool_call_id returns the
		// recorded cancellation instead of running the tool a second time. That
		// is what stops an interrupted spawn_status(wait) from restarting its
		// whole wait. A genuinely NEW call from a later LLM turn carries a new
		// tool_call_id with no row, so it runs normally -- the model deciding to
		// retry is its business, not ours to block.
		//
		// The durable row is also the only carrier that works across processes:
		// this runs in the API server, the guard reads in the worker.
		s.recordToolCallCancelled(ctx, call, reason)

		delivered := true
		if s.toolCanceler != nil {
			if err := s.toolCanceler.SendToolExecutionCancel(ctx, userID, call.ID, reason); err != nil {
				// Not fatal, and not silently swallowed either. The tool may run to
				// completion and report a real outcome rather than a cancel.
				logging.Warn("Could not deliver tool cancel to daemon",
					"error", err, "toolCallID", call.ID)
				delivered = false
			}
		}
		if delivered {
			outcome.CancelledToolCalls++
		} else {
			outcome.UndeliverableToolCalls = append(outcome.UndeliverableToolCalls, call.ID)
		}
	}
	return outcome
}
