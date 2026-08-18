// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE FOUNDATION OF THREAD INTERRUPT (specs/thread-interrupt.md)
//
// Interrupt is built from PER-TOOL cancel, not from pause's cancelAll(). The
// difference is not stylistic, and these tests are what pin it:
//
//   - Per-tool cancel leaves the activity running. Every tool -- the cancelled
//     one included -- produces a durable outcome and a real tool_result, so the
//     activity returns normally and the step's save_message persists
//     tool_results. History stays valid BY CONSTRUCTION.
//
//   - Pause's cancelAll() kills the whole activity. It returns a
//     temporal.CanceledError, and StepExecutor.handleActivityCompletion takes an
//     early return on that error BEFORE executeSaveMessage runs -- so
//     tool_results are never persisted and the thread is left with an assistant
//     message carrying tool_calls and no results row. Pause gets away with it
//     because resume re-runs the whole step. Interrupt cannot: it abandons the
//     tools and moves on.
//
// If these tests ever fail, the interrupt design is invalid -- it would be
// leaving dangling tool calls behind and leaning on repairMessageHistory to
// synthesize "outcome unknown" for tools the user deliberately stopped.

// blockingToolExecutor lets one tool park until the test releases it, so a
// cancel can be aimed at a genuinely in-flight call while a sibling completes.
// The real thing this models is a long bash command: the user hits interrupt
// while it is still running, and the sibling read that already returned must
// keep its output.
type blockingToolExecutor struct {
	mu      sync.Mutex
	results map[string]*toolexec.ToolResult

	// blockUntil parks the named tool call until the channel closes.
	blockUntil map[string]chan struct{}
	// started is closed when the named tool call has entered ExecuteTool, so
	// the test can cancel at a point where the call is provably in flight
	// rather than racing the goroutine pool.
	started map[string]chan struct{}
}

func newBlockingToolExecutor() *blockingToolExecutor {
	return &blockingToolExecutor{
		results:    make(map[string]*toolexec.ToolResult),
		blockUntil: make(map[string]chan struct{}),
		started:    make(map[string]chan struct{}),
	}
}

func (m *blockingToolExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	m.mu.Lock()
	block := m.blockUntil[req.ToolCallID]
	started := m.started[req.ToolCallID]
	result := m.results[req.ToolCallID]
	m.mu.Unlock()

	if started != nil {
		close(started)
	}

	if block != nil {
		// A real cancelled tool observes its context dying. Whichever happens
		// first, the call stops here.
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if result != nil {
		return result, nil
	}
	return &toolexec.ToolResult{Success: true, Content: "ok"}, nil
}

func (m *blockingToolExecutor) Close() error { return nil }

func (m *blockingToolExecutor) SetResult(toolCallID string, r *toolexec.ToolResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results[toolCallID] = r
}

// Block makes toolCallID park. Returns the release func and a channel that
// closes once the call has actually entered execution.
func (m *blockingToolExecutor) Block(toolCallID string) (release func(), started <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	blockCh := make(chan struct{})
	startedCh := make(chan struct{})
	m.blockUntil[toolCallID] = blockCh
	m.started[toolCallID] = startedCh
	var once sync.Once
	return func() { once.Do(func() { close(blockCh) }) }, startedCh
}

// GAP: this used to pin that cancelling ONE tool mid-flight (via the
// in-process shell.CancelSignal) left a completing sibling's real output
// intact while still reporting the cancelled call as cancelled. That signal
// was a package-level map in the API server process; the worker that runs
// this activity is a SEPARATE process and could never read it (see
// specs/fast-cancel-briefing.md, "shell.GetCancelSignal() IS DEAD CODE").
// So the scenario this test modelled never actually happened in production --
// it exercised a mechanism no cross-process request could reach.
//
// What genuinely selective (single-tool, siblings-unaffected) cancellation
// requires is a per-tool signal that crosses the worker/API-server boundary.
// None exists today. The daemon-side per-execution cancel (SendToolExecutionCancel)
// only reaches daemon-routed tools and stops the underlying process; it does
// not mark this activity's result as cancelled if the executor call still
// returns success. TestInterrupt_ContextCancellationDoesNotClaimCompletedSiblings
// still covers the "shared activity context died" half of sibling-safety.
func TestInterrupt_CancelOneToolLeavesSiblingResultIntact(t *testing.T) {
	t.Skip("gap: selective single-tool cancellation with sibling output intact " +
		"has no surviving in-process or cross-process mechanism now that " +
		"shell.CancelSignal (dead cross-process code) is removed; see comment above")
}

// The sibling-cancellation regression, pinned against the ACTIVITY CONTEXT
// dying rather than the per-tool signal.
//
// All of a turn's tool calls run as parallel goroutines sharing ONE activity
// context. So when anything cancels that context, every sibling's
// handleToolExecutionResult sees ctx.Err() != nil -- including siblings that
// already returned real output. Checking ctx.Err() BEFORE consulting the result
// already in hand is what once reported completed tools as cancelled and threw
// their output away.
//
// This is the case the guard `(execResult == nil || !execResult.Success)`
// exists for. Removing that guard must fail this test.
func TestInterrupt_ContextCancellationDoesNotClaimCompletedSiblings(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	finishedID := "toolu_" + uuid.New().String()
	activityInstance := NewExecuteToolsActivity(f.h.Repo(), newBlockingToolExecutor())

	// Temporal's TestActivityEnvironment exposes no way to cancel a running
	// activity's context from outside, and calling into activity code with a
	// bare context.Background() panics in activity.GetLogger. So the probe runs
	// INSIDE a real activity and derives a cancelled child of the activity's
	// own context: the activity values survive, but ctx.Err() != nil — which is
	// precisely what a sibling goroutine sees the instant one tool's
	// cancellation kills the context they all share.
	probe := func(actCtx context.Context) (siblingProbe, error) {
		deadCtx, cancel := context.WithCancel(actCtx)
		cancel()
		if deadCtx.Err() == nil {
			return siblingProbe{}, assertFailedErr("context must be dead for this probe to mean anything")
		}

		tec := &toolExecutionContext{
			chatID:     f.chatID,
			thread:     f.chatID,
			toolName:   "view",
			toolInput:  `{"file_path":"/tmp/x"}`,
			toolCallID: finishedID,
			chat:       &db.Chat{ID: f.chatID},
		}

		// The tool SUCCEEDED. The context is dead only because a sibling was
		// cancelled.
		finished := &toolexec.ToolResult{
			Success: true,
			Content: "completed before the context died",
		}

		res := activityInstance.handleToolExecutionResult(
			deadCtx, tec, finished, nil, time.Now().Add(-time.Second))
		return siblingProbe{Content: res.Content, IsError: res.IsError}, nil
	}

	got := runSiblingProbe(t, f.h, probe)

	// THE ASSERTION THAT MATTERS: a dead context is not evidence about a tool
	// that already produced output. Removing the
	// `(execResult == nil || !execResult.Success)` guard fails right here.
	assert.Equal(t, "completed before the context died", got.Content,
		"a tool that finished successfully must keep its output even though a "+
			"sibling's cancellation killed the shared activity context")
	assert.False(t, got.IsError,
		"a completed tool must not be reported as cancelled because a sibling was")

	_ = ctx
}

// siblingProbe carries the one tool result out of the probe activity. A struct
// rather than two returns because Temporal activities return (value, error).
type siblingProbe struct {
	Content string
	IsError bool
}

type assertFailedErr string

func (e assertFailedErr) Error() string { return string(e) }

// runSiblingProbe executes a zero-arg probe activity and decodes its result.
// The helper's ExecuteActivity always passes an input argument, which a
// zero-arg activity rejects, so this goes to the env directly.
func runSiblingProbe(
	t *testing.T,
	h *IdempotencyTestHelper,
	probe func(context.Context) (siblingProbe, error),
) siblingProbe {
	t.Helper()
	h.env.RegisterActivity(probe)
	val, err := h.env.ExecuteActivity(probe)
	require.NoError(t, err)
	var got siblingProbe
	require.NoError(t, val.Get(&got))
	return got
}

// A CALL CANCELLED WHILE PENDING MUST NEVER REACH THE EXECUTOR.
//
// The failure this pins is real: on chat b7cd65c6 a spawn_status(wait:true)
// sat PENDING for over three minutes and then ran for 8m44s after the user
// had "cancelled" it. PENDING means recorded but not yet dispatched, and it
// is a genuine window -- execute_tools writes PENDING immediately before
// handing the call to the executor.
//
// This was previously guarded by shell.GetCancelSignal(), an in-memory
// singleton written by the API server and read by the worker -- different
// processes, so it never actually fired in production. It was deleted, and
// this test was skipped while the protection was genuinely absent.
//
// It is now provided durably: InterruptThread writes a terminal Cancelled row
// (and its result) for every IN-FLIGHT call, which includes PENDING ones, at
// the moment the user asks. checkPriorTerminalResult then finds a terminal row
// and returns the recorded cancellation instead of executing. The carrier is
// the database, so it works across the API-server/worker split that defeated
// the old signal.
func TestInterrupt_CancelWhilePendingNeverDispatches(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	toolCallID := "toolu_pending_" + uuid.NewString()
	threadID := f.chatID
	now := time.Now().UTC()

	// The call is PENDING: recorded, not yet dispatched. This is the window
	// execute_tools opens when it upserts PENDING immediately before handing
	// the call to the executor.
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: f.chatID, ThreadID: &threadID,
		ToolName: "spawn_status", Status: core.ToolCallStatusPending,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	// The user interrupts. threads.InterruptThread records the cancellation
	// durably for every IN-FLIGHT call -- which includes PENDING ones -- at the
	// moment it is asked, rather than waiting for the activity to notice.
	completedAt := now.Add(time.Millisecond)
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: f.chatID, ThreadID: &threadID,
		ToolName: "spawn_status", Status: core.ToolCallStatusCancelled,
		RequestedAt: now, CompletedAt: &completedAt, CreatedAt: now, UpdatedAt: completedAt,
	}))
	require.NoError(t, f.h.Repo().UpsertToolCallResult(ctx, &db.ToolCallResult{
		ToolCallID: toolCallID, Content: "Tool execution cancelled by user",
		IsError: true, CreatedAt: completedAt, UpdatedAt: completedAt,
	}))

	// Now the dispatch proceeds, through the real activity. It must return the
	// recorded cancellation and NEVER reach the executor.
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{Success: true, Content: "should never run"})

	output := f.executeTool(t, toolCallID, "spawn_status", `{"wait":true}`, executor)

	require.Len(t, output.ToolResults, 1)
	assert.True(t, output.ToolResults[0].IsError,
		"the model must see this call did not produce a real result")
	assert.Equal(t, 0, executor.GetExecutionCount(toolCallID),
		"the tool must NEVER execute -- tools are not idempotent and the user cancelled this one")
}

// GAP: same shell.CancelSignal removal as above. This pinned that a cancel
// landing after the tool already completed does not discard the real result
// -- it still marks the row CANCELLED (recording user intent) but keeps the
// actual output rather than a synthetic "cancelled" message. With the signal
// gone, execute_tools no longer has any post-completion cancel check at all:
// a tool that finishes always reports "completed" on its own merits now,
// which is arguably more correct (it never discards real output) but no
// longer records that the user asked to stop it after it had already
// finished. Low severity: the call already produced its real, correct
// result by the time this would fire.
func TestInterrupt_CancelAfterCompletionDoesNotDiscardRealOutput(t *testing.T) {
	t.Skip("gap: post-completion cancel marking is gone along with " +
		"shell.CancelSignal (dead cross-process code); a tool that finishes " +
		"now always reports its real completed result, see comment above")
}
