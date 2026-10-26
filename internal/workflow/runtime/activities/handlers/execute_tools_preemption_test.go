// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// waitingExecutor stands in for the real executor. A call the activity armed
// for preemption parks — as shell_wait does — until its preemption fires, then
// answers with onPreempt; past budget it gives up as if its wait ran out. Any
// other call returns at once.
type waitingExecutor struct {
	budget    time.Duration
	onPreempt func() (*toolexec.ToolResult, error)

	mu    sync.Mutex
	armed map[string]bool
	// started closes when the first armed call is parked.
	started   chan struct{}
	startOnce sync.Once
}

func newWaitingExecutor(budget time.Duration) *waitingExecutor {
	return &waitingExecutor{
		budget: budget,
		onPreempt: func() (*toolexec.ToolResult, error) {
			return &toolexec.ToolResult{Success: true, Content: "Stopped waiting on process p1 after 1s. It is STILL RUNNING and has not been killed."}, nil
		},
		armed:   map[string]bool{},
		started: make(chan struct{}),
	}
}

func (e *waitingExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	preempt := tools.PreemptionFrom(ctx)
	e.mu.Lock()
	e.armed[req.ToolCallID] = preempt != nil
	e.mu.Unlock()
	if preempt == nil {
		return &toolexec.ToolResult{Success: true, Content: "ran to completion"}, nil
	}

	e.startOnce.Do(func() { close(e.started) })
	select {
	case <-preempt:
		return e.onPreempt()
	case <-time.After(e.budget):
		return &toolexec.ToolResult{Success: true, Content: "waited out its budget"}, nil
	}
}

func (e *waitingExecutor) Close() error { return nil }

func (e *waitingExecutor) wasArmed(toolCallID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.armed[toolCallID]
}

func (f *durableStatusFixture) runBatch(t *testing.T, executor toolexec.ToolExecutor, calls ...message.ToolCall) *ExecuteToolsOutput {
	t.Helper()
	var output ExecuteToolsOutput
	err := f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), executor).Execute, ExecuteToolsInput{
		ChatID:    f.chatID,
		Thread:    f.chatID,
		ToolCalls: calls,
	}, &output)
	require.NoError(t, err)
	return &output
}

// queueMessage puts a message in the thread's mailbox, the way every sender
// does — SendMessage to a live run, SendAgentMessage, spawn_send, a sub-agent's
// report.
func (f *durableStatusFixture) queueMessage(t *testing.T) string {
	t.Helper()
	id := uuid.New().String()
	require.NoError(t, f.h.Repo().EnqueueAgentMessage(context.Background(), &core.AgentMessage{
		ID:           id,
		ChatID:       f.chatID,
		FromThreadID: f.chatID,
		ToThreadID:   f.chatID,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         "stop waiting and look at this",
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now(),
	}))
	return id
}

func waitCall() message.ToolCall {
	return message.ToolCall{ID: "toolu_" + uuid.New().String(), Name: tools.ToolShellWait, Input: `{"process_id":"p1"}`}
}

// THE FEATURE. A message that arrives while the thread waits ends the wait
// within a poll, and the tool's own answer comes back as a completed result
// saying why it came early. The message itself is left queued: delivering it
// is the next turn's job, not preemption's.
func TestPreemption_AMessageEndsAWaitEarly(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	executor := newWaitingExecutor(20 * time.Second)
	call := waitCall()
	// The id comes back on a channel: the batch sees the queued message
	// through the database, which gives the race detector no happens-before
	// edge to the goroutine's write.
	messageIDs := make(chan string, 1)
	go func() {
		<-executor.started
		messageIDs <- f.queueMessage(t)
	}()

	start := time.Now()
	output := f.runBatch(t, executor, call)
	elapsed := time.Since(start)

	require.Len(t, output.ToolResults, 1)
	got := output.ToolResults[0]
	assert.Less(t, elapsed, 10*time.Second, "the wait must end when the message arrives, not when its budget runs out")
	assert.False(t, got.GetIsError(), "being preempted is not a failure: %s", got.GetContent())
	assert.Contains(t, got.GetContent(), "STILL RUNNING", "the tool's own answer is kept")
	assert.Contains(t, got.GetContent(), preemptedNote, "and says why it came early")

	row, err := f.h.Repo().GetToolCall(ctx, call.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCompleted, row.Status)
	result := getToolCallResult(t, f.h, call.ID)
	require.NotNil(t, result)
	assert.Equal(t, got.GetContent(), result.Content, "the durable result is what the agent saw")

	msg, err := f.h.Repo().GetAgentMessage(ctx, <-messageIDs)
	require.NoError(t, err)
	assert.Equal(t, core.AgentMessageStatusQueued, msg.Status, "preemption must not consume the message; the next turn delivers it")
}

// A preempted call whose answer was lost — a daemon too old to send it back,
// one that never answered — still reports a SUCCESSFUL stopped-early result.
// The call did what it promised; an error would tell the agent its wait failed.
func TestPreemption_ALostAnswerIsStillAStoppedEarlyResult(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	executor := newWaitingExecutor(20 * time.Second)
	executor.onPreempt = func() (*toolexec.ToolResult, error) {
		return &toolexec.ToolResult{Success: false, IsError: true, Content: "Tool execution cancelled", ErrorCode: "CANCELLED"}, nil
	}
	call := waitCall()
	go func() {
		<-executor.started
		f.queueMessage(t)
	}()

	output := f.runBatch(t, executor, call)

	require.Len(t, output.ToolResults, 1)
	assert.False(t, output.ToolResults[0].GetIsError())
	assert.Equal(t, preemptedResultContent, output.ToolResults[0].GetContent())
	row, err := f.h.Repo().GetToolCall(context.Background(), call.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusCompleted, row.Status)
}

// One message ends EVERY wait in the batch. The next turn waits for the whole
// batch, so a sibling left waiting would hold the message just as long. This
// also covers mail already queued when the batch starts.
func TestPreemption_OneMessageEndsEveryWaitInTheBatch(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	f.queueMessage(t)
	executor := newWaitingExecutor(20 * time.Second)
	first, second := waitCall(), waitCall()

	start := time.Now()
	output := f.runBatch(t, executor, first, second)

	assert.Less(t, time.Since(start), 10*time.Second)
	require.Len(t, output.ToolResults, 2)
	for _, got := range output.ToolResults {
		assert.Contains(t, got.GetContent(), preemptedNote, "call %s", got.GetToolCallId())
	}
}

// Only preemptable tools are armed. Preempting anything else would cut short
// work that has effects — a `shell` command stopped halfway.
func TestPreemption_OnlyPreemptableToolsAreArmed(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	executor := newWaitingExecutor(0)
	wait := waitCall()
	view := message.ToolCall{ID: "toolu_" + uuid.New().String(), Name: tools.ToolView, Input: `{"file_path":"/tmp/x"}`}

	f.runBatch(t, executor, wait, view)

	assert.True(t, executor.wasArmed(wait.ID), "shell_wait is preemptable")
	assert.False(t, executor.wasArmed(view.ID), "view is not")
}

// A message preempts once. One the drain cannot deliver stays queued, and if
// its presence alone ended every wait, the agent would loop on one-second
// waits and LLM turns for as long as it stayed. Already spent, it lets the
// wait run.
func TestPreemption_AMessagePreemptsOnlyOnce(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	f.queueMessage(t)
	marked, err := f.h.Repo().MarkQueuedAgentMessagesPreempting(ctx, f.chatID, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, marked)

	executor := newWaitingExecutor(2500 * time.Millisecond)
	output := f.runBatch(t, executor, waitCall())

	require.Len(t, output.ToolResults, 1)
	assert.Equal(t, "waited out its budget", output.ToolResults[0].GetContent(),
		"a message that already preempted one wait must not preempt another")
}

func TestPreemptedOutcome(t *testing.T) {
	t.Parallel()

	t.Run("keeps a clean answer and says why it came early", func(t *testing.T) {
		got := preemptedOutcome(&toolexec.ToolResult{Success: true, Content: "still running", Metadata: `{"process_id":"p1"}`}, nil)
		assert.True(t, got.Success)
		assert.Equal(t, "still running"+preemptedNote, got.Content)
		assert.JSONEq(t, `{"process_id":"p1","preempted":true}`, got.Metadata)
	})

	t.Run("stands in for a lost answer", func(t *testing.T) {
		for name, res := range map[string]*toolexec.ToolResult{
			"cancelled":  {Success: false, IsError: true, Content: "Tool execution cancelled"},
			"tool error": {Success: true, IsError: true, Content: "Failed to list processes: context canceled"},
			"no result":  nil,
		} {
			got := preemptedOutcome(res, nil)
			assert.True(t, got.Success, name)
			assert.False(t, got.IsError, name)
			assert.Equal(t, preemptedResultContent, got.Content, name)
			var meta map[string]any
			require.NoError(t, json.Unmarshal([]byte(got.Metadata), &meta), name)
			assert.Equal(t, true, meta["preempted"], name)
		}
	})

	t.Run("leaves metadata that is not an object alone", func(t *testing.T) {
		assert.Equal(t, "not json", withPreemptedMetadata("not json"))
	})
}
