// Copyright (c) 2025 Reliant Labs
//
//go:build replayfixtures

package replaytest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// TestGenerateFixture_InterruptDuringToolBatch pins "Interrupt & send now"
// landing while a turn's tool batch is in flight. The batch holds a fetch call
// that runs until it is stopped and an activation call (manage_machine), whose
// AutomationApprovalPreview the workflow runs before ExecuteTools. The user
// queues a message through the production SendAgentMessage handler and
// interrupts the thread through the production threads.InterruptThread, which
// records the running call Cancelled and signals `interrupt_thread`. The
// interrupt cancels ExecuteTools; the workflow settles the batch with one
// ExecuteTools dispatch that runs nothing (`interrupt-settles-tool-step`
// version marker), saves the tool results, and the next CallLLM delivers the
// queued message and completes.
func TestGenerateFixture_InterruptDuringToolBatch(t *testing.T) {
	blockCall := ToolCall("call-block-1", tools.ToolFetch, `{"url":"https://example.com","format":"text"}`)
	machineCall := ToolCall("call-machine-1", names.ToolManageMachine, `{"action":"start"}`)
	script := NewScriptedLLM(
		Turn{Text: "Fetching the page and starting the machine.", ToolCalls: []message.ToolCall{blockCall, machineCall}},
		Turn{Text: "Stopped, as you asked; switching to your request."},
	)
	s := requireStack(t)
	blocker := newFirstCallBlocker(blockCall.ID, newLocalDaemonExecutor(tools.NewToolsFactory(&tools.ToolsOptions{Repo: s.Repo})))
	h := newHarnessWith(t, script, blocker.wrap)

	created := h.StartChat("builtin://agent", "Fetch the page, then start the machine", map[string]any{
		"mode":  "auto",
		"tools": []any{tools.ToolFetch, names.ToolManageMachine},
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	select {
	case <-blocker.started:
	case <-time.After(waitTimeout):
		t.Fatal("the blocking fetch call never started")
	}

	resp, err := h.SendAgentMessage(chatID, workflowID, "stop that and do this instead")
	require.NoError(t, err)
	require.True(t, resp.Success, "SendAgentMessage refused: %s", resp.Message)

	interrupter := threads.NewService(h.Stack.Repo, threads.WithTemporalSignaler(h.Stack.Temporal))
	_, err = interrupter.InterruptThread(h.Ctx, threads.InterruptThreadOpts{UserID: h.UserID, ChatID: chatID, ThreadID: workflowID})
	require.NoError(t, err, "InterruptThread")

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())
	assert.False(t, h.LLM.Exhausted())

	h.ExportHistory(workflowID, "interrupt_during_tool_batch")
}

// firstCallBlocker runs one tool call until its context ends (the interrupt
// stopping it) and passes every other call to the hermetic local executor.
type firstCallBlocker struct {
	callID  string
	started chan struct{}
	once    sync.Once
	inner   toolexec.ToolExecutor
	wrap    toolexec.ToolExecutor
}

func newFirstCallBlocker(callID string, inner toolexec.ToolExecutor) *firstCallBlocker {
	b := &firstCallBlocker{callID: callID, started: make(chan struct{}), inner: inner}
	b.wrap = blockingExecutor{b}
	return b
}

type blockingExecutor struct{ b *firstCallBlocker }

func (e blockingExecutor) ExecuteTool(ctx context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	b := e.b
	if req.ToolCallID == b.callID {
		blocked := false
		b.once.Do(func() { blocked = true })
		if blocked {
			close(b.started)
			start := time.Now()
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Minute):
			}
			return &toolexec.ToolResult{IsError: true, Content: "stopped", StartTime: start, EndTime: time.Now()}, nil
		}
	}
	return b.inner.ExecuteTool(ctx, req)
}

func (e blockingExecutor) Close() error { return nil }
