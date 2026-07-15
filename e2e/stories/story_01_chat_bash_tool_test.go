// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Story 01: a user creates a chat, the agent loop runs, executes a REAL bash
// tool through the local daemon execution path, the assistant completes, and
// the conversation is fully persisted in order with the workflow marked
// completed and the chat IDLE.
//
// Full stack exercised: CreateChat handler → Temporal DynamicWorkflow on the
// story's task queue → CallLLM (scripted driver) → ExecuteTools
// (LocalToolExecutor + daemon.LocalClient — the daemon runtime's own
// execution path — running a real shell command) → message/content-block
// persistence → WorkflowStatus bookkeeping.
func TestStory01_ChatRunsBashToolAndCompletes(t *testing.T) {
	t.Parallel()

	marker := "e2e-bash-" + shortID()
	finalText := "All done — the command ran successfully."

	script := NewScriptedLLM(
		// Turn 1: assistant requests a real bash execution.
		Turn{
			Text: "Let me run that command.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-bash-1", tools.ShellToolName, fmt.Sprintf(`{"command":"echo %s"}`, marker)),
			},
		},
		// Turn 2: assistant sees the tool result and finishes (no tool calls
		// → the agent loop exits).
		Turn{Text: finalText},
	)

	h := newHarness(t, script)

	created := h.CreateChat("builtin://agent", "Please echo something for me", map[string]any{
		"mode": "auto",
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.WorkflowStatusCompleted)

	// Conversation persisted in order on the root thread (thread == root
	// workflow ID).
	msgs := h.Messages(chatID, workflowID)
	require.GreaterOrEqual(t, len(msgs), 4, "want user, assistant(tool_call), tool, assistant(final); got %d", len(msgs))
	for i := 1; i < len(msgs); i++ {
		require.Greater(t, msgs[i].Ordinal, msgs[i-1].Ordinal, "ordinals must strictly ascend")
	}

	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, msgs[0].Role, "first message is the user prompt")
	assert.Contains(t, TextOf(msgs[0]), "Please echo something")

	var sawToolCall, sawToolResult, sawFinal bool
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch b.BlockType {
			case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL:
				require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, m.Role)
				require.NotNil(t, b.ToolName)
				assert.Equal(t, tools.ShellToolName, *b.ToolName)
				sawToolCall = true
			case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT:
				require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_TOOL, m.Role)
				require.NotNil(t, b.Content)
				assert.Contains(t, *b.Content, marker,
					"real bash output must round-trip into the persisted tool result")
				require.NotNil(t, b.IsError)
				assert.False(t, *b.IsError, "bash tool must succeed, got error result: %s", *b.Content)
				sawToolResult = true
			case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT:
				if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT && b.Content != nil && *b.Content == finalText {
					sawFinal = true
				}
			}
		}
	}
	require.True(t, sawToolCall, "assistant tool_call block persisted")
	require.True(t, sawToolResult, "tool result block persisted")
	require.True(t, sawFinal, "final assistant text persisted")

	// Chat is idle (not archived / wedged).
	assert.Equal(t, db.ChatStateIdle, h.Chat(chatID).State)

	// Script fully consumed, never over-consumed.
	assert.False(t, h.LLM.Exhausted(), "agent loop must not request more turns than scripted")
	streamCalls := h.LLM.StreamCalls()
	require.Len(t, streamCalls, 2, "exactly two agent-loop LLM calls")

	// The second LLM call's conversation history must contain the bash
	// result — this is the "tool output feeds the next turn" guarantee.
	var sawResultInHistory bool
	for _, hm := range streamCalls[1].Messages {
		for _, tr := range hm.ToolResults() {
			if strings.Contains(tr.Content, marker) {
				sawResultInHistory = true
			}
		}
	}
	assert.True(t, sawResultInHistory, "bash output must appear in the second call's history")
}
