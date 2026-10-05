// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Story 12: builtin://agent in manual mode gates every tool turn on an
// approval node, and the production ApprovalService is what opens or closes
// that gate.
//
// Each component already has its own test (ApprovalCreate persists a row,
// Approve signals, the runtime waits on the signal). This pins down the loop
// those pieces form together, which none of them can see alone: the run
// blocks with the tool NOT executed, and the user's decision — not a timeout,
// not a default — decides whether the tool ever runs.

// TestStory12_ManualModeApprovalRunsToolOnlyAfterApprove: approve → the
// gated tool executes and its output feeds the next turn.
func TestStory12_ManualModeApprovalRunsToolOnlyAfterApprove(t *testing.T) {
	t.Parallel()

	marker := "e2e-approved-" + shortID()

	script := NewScriptedLLM(
		Turn{
			Text: "I need to run a command.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-approve-1", tools.ShellToolName, fmt.Sprintf(`{"command":"echo %s"}`, marker)),
			},
		},
		Turn{Text: "The approved command ran."},
	)

	h := newHarness(t, script)

	created := h.StartChat("builtin://agent", "Run a command, but ask me first", map[string]any{
		"mode": "manual",
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// 1. The run blocks on the approval gate, with the tool not yet run.
	approval := h.WaitPendingApproval(chatID)
	assert.Equal(t, int32(reliantv1.ApprovalType_APPROVAL_TYPE_WORKFLOW_STEP), approval.ApprovalType)
	assert.Equal(t, "Approve tool execution?", approval.Title)
	assert.Equal(t, workflowID, approval.TemporalWorkflowID,
		"the approval must name the execution the Approve signal is delivered to")

	wf, err := h.Stack.Repo.GetWorkflow(h.Ctx, workflowID)
	require.NoError(t, err)
	require.Equal(t, db.Active(), wf.Status, "an approval blocks inside a running workflow")
	require.Len(t, h.LLM.StreamCalls(), 1, "no second LLM turn may run while the gate is closed")
	requireNoToolResult(t, h, chatID, workflowID)

	// 2. Approve through the production handler.
	_, err = h.ApprovalSvc.Approve(h.Ctx, connect.NewRequest(&reliantv1.ApproveRequest{
		RequestId: approval.ID,
	}))
	require.NoError(t, err, "Approve")

	// 3. The tool runs, the agent finishes, and the run completes.
	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	resolved, err := h.Stack.Repo.GetApproval(h.Ctx, approval.ID)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED), resolved.Status)

	result := onlyToolResult(t, h, chatID, workflowID)
	assert.Contains(t, *result.Content, marker, "the approved command's real output is persisted")
	require.NotNil(t, result.IsError)
	assert.False(t, *result.IsError, "approved tool must succeed, got: %s", *result.Content)

	calls := h.LLM.StreamCalls()
	require.Len(t, calls, 2, "exactly two agent-loop LLM calls")
	assert.True(t, historyHasToolResult(calls[1].Messages, marker),
		"the approved tool's output must reach the next turn")
	assert.False(t, h.LLM.Exhausted(), "agent loop must not request more turns than scripted")
}

// TestStory12_ManualModeApprovalDenyNeverRunsTool: deny → the tool never
// executes, the model is told it was denied, and the run still finishes
// cleanly instead of wedging on a tool_use with no result.
func TestStory12_ManualModeApprovalDenyNeverRunsTool(t *testing.T) {
	t.Parallel()

	// A side effect the test can observe directly: if the gate leaks, this
	// file exists.
	sentinel := filepath.Join(t.TempDir(), "denied-"+shortID())

	script := NewScriptedLLM(
		Turn{
			Text: "I need to create a file.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-deny-1", tools.ShellToolName, fmt.Sprintf(`{"command":"touch %s"}`, sentinel)),
			},
		},
		Turn{Text: "Understood, I will not create it."},
	)

	h := newHarness(t, script)

	created := h.StartChat("builtin://agent", "Create a file, but ask me first", map[string]any{
		"mode": "manual",
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	approval := h.WaitPendingApproval(chatID)

	denialReason := "e2e-denied-" + shortID()
	_, err := h.ApprovalSvc.Deny(h.Ctx, connect.NewRequest(&reliantv1.DenyRequest{
		RequestId:    approval.ID,
		DenialReason: &denialReason,
	}))
	require.NoError(t, err, "Deny")

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	resolved, err := h.Stack.Repo.GetApproval(h.Ctx, approval.ID)
	require.NoError(t, err)
	assert.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED), resolved.Status)

	_, statErr := os.Stat(sentinel)
	assert.True(t, os.IsNotExist(statErr), "a denied tool must never execute (sentinel stat: %v)", statErr)

	// The tool_use is answered — by the denial, not by an execution — so the
	// thread is well-formed for the next turn.
	result := onlyToolResult(t, h, chatID, workflowID)
	assert.Equal(t, denialReason, *result.Content)
	require.NotNil(t, result.IsError)
	assert.True(t, *result.IsError, "a denied tool call is reported to the model as an error")

	calls := h.LLM.StreamCalls()
	require.Len(t, calls, 2, "the model gets one more turn to react to the denial")
	assert.True(t, historyHasToolResult(calls[1].Messages, denialReason),
		"the model must be told the call was denied")
	assert.False(t, h.LLM.Exhausted(), "agent loop must not request more turns than scripted")
}

// onlyToolResult returns the single tool_result block on the chat's root
// thread, failing if there is not exactly one.
func onlyToolResult(t *testing.T, h *Harness, chatID, thread string) *db.MessageContentBlock {
	t.Helper()
	var results []*db.MessageContentBlock
	for _, m := range h.Messages(chatID, thread) {
		for _, b := range m.Blocks {
			if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
				results = append(results, b)
			}
		}
	}
	require.Len(t, results, 1, "exactly one tool_result for the one gated tool call")
	require.NotNil(t, results[0].Content)
	return results[0]
}

func requireNoToolResult(t *testing.T, h *Harness, chatID, thread string) {
	t.Helper()
	for _, m := range h.Messages(chatID, thread) {
		for _, b := range m.Blocks {
			require.NotEqual(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT, b.BlockType,
				"a tool result exists before the approval was decided")
		}
	}
}

func historyHasToolResult(history []message.Message, want string) bool {
	for _, m := range history {
		for _, tr := range m.ToolResults() {
			if strings.Contains(tr.Content, want) {
				return true
			}
		}
	}
	return false
}
