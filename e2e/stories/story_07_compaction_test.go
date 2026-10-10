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

// Story 07: compaction triggers mid-conversation and the conversation
// continues. We force it by setting a tiny compaction threshold on the model
// input; a scripted turn reports a token usage far above it, so the
// agent-loop edge `execute_tools.thread_token_count > compaction_threshold`
// routes into the compact node. Compact generates a summary via the shared
// llm_request path (SendMessages on the injected resolver), saves it as a
// new-context-window system message, and the next LLM turn runs against the
// compacted history.
//
// The context has to GROW past the threshold, not open above it. Since #662
// the compact node skips a compaction that cannot reclaim at least 10% of the
// context (message.CompactionMinReclaimFraction): what a window opened with —
// system prompt, tools, memory — survives compaction, so compacting it only
// loops. The first turn's usage is the window's floor; the second reports far
// above it, and that growth is what the compaction reclaims.
func TestStory07_CompactionTriggersAndConversationContinues(t *testing.T) {
	t.Parallel()

	marker := "e2e-compact-" + shortID()
	script := NewScriptedLLM(
		// Iteration 1: a tool call reporting a usage below the threshold, so
		// no compaction yet. This count is the window's floor.
		Turn{
			Text: "Working on it.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-bash-1", tools.ShellToolName, fmt.Sprintf(`{"command":"echo %s"}`, marker)),
			},
			TokenCount: 50,
		},
		// Iteration 2: a tool call with a huge reported usage → after
		// execute_tools, thread tokens exceed the threshold, nearly all of it
		// growth since the floor → compact runs.
		Turn{
			Text: "Still working.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-bash-2", tools.ShellToolName, fmt.Sprintf(`{"command":"echo %s-again"}`, marker)),
			},
			TokenCount: 5000,
		},
		// Iteration 3: runs AFTER compaction, finishes the conversation.
		Turn{Text: "All finished after compaction."},
	)
	h := newHarness(t, script)

	created := h.StartChat("builtin://agent", "Do something token-heavy", map[string]any{
		"mode": "auto",
		// compaction_threshold rides on the model input object (see
		// agent.yaml: args.compaction_threshold ← inputs.model.compaction_threshold).
		"model": map[string]any{"id": "mock", "compaction_threshold": 100},
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	// The compaction summary message must be persisted as a system message.
	msgs := h.Messages(chatID, workflowID)
	var compactionMsg *MessageWithBlocks
	for i := range msgs {
		if strings.Contains(TextOf(msgs[i]), "This session is being continued from a previous conversation") {
			compactionMsg = &msgs[i]
		}
	}
	require.NotNil(t, compactionMsg, "compaction summary message must be persisted; messages: %d", len(msgs))
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, compactionMsg.Role)

	// Compaction opens a NEW context window: the summary's context window id
	// must differ from the first message's.
	require.NotEmpty(t, msgs[0].ContextWindowID)
	require.NotEmpty(t, compactionMsg.ContextWindowID)
	assert.NotEqual(t, msgs[0].ContextWindowID, compactionMsg.ContextWindowID,
		"compaction must start a new context window")

	// The summary itself came from the compaction LLM request (the driver's
	// streaming path with the "summarizing conversations" prompt), and the
	// canned summary text must be embedded in the persisted message.
	require.Len(t, h.LLM.CompactionCalls(), 1, "compaction must make exactly one summary LLM request")
	assert.Contains(t, TextOf(*compactionMsg), CompactionSummaryText)

	// The post-compaction turn ran: exactly three agent-loop calls, and only
	// the third one's history contains the compaction summary text instead
	// of a dangling raw history.
	streamCalls := h.LLM.StreamCalls()
	require.Len(t, streamCalls, 3)
	summaryInHistory := func(call LLMCall) bool {
		for i := range call.Messages {
			if strings.Contains(call.Messages[i].Content().Text, "This session is being continued from a previous conversation") {
				return true
			}
		}
		return false
	}
	assert.False(t, summaryInHistory(streamCalls[1]), "the turn that tripped the threshold ran before compaction")
	assert.True(t, summaryInHistory(streamCalls[2]), "third LLM call must run against the compacted history")

	// Conversation continued to a clean end.
	var sawFinal bool
	for _, m := range msgs {
		if strings.Contains(TextOf(m), "All finished after compaction.") {
			sawFinal = true
		}
	}
	assert.True(t, sawFinal, "post-compaction assistant message must be persisted")
	assert.False(t, h.LLM.Exhausted())
	assert.Equal(t, db.ChatStateIdle, h.Chat(chatID).State)
}
