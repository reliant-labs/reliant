// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Story 14: the daemon-offline breaker pauses a run whose machine dropped, and
// the machine coming back resumes it with nobody sending a message. The
// connect handler (internal/queueddelivery) calls ResumePausedForMachine; the
// run must continue where it stopped and tell the agent the machine is back.
func TestStory14_MachineReconnectResumesBreakerPausedChat(t *testing.T) {
	t.Parallel()

	toolTurn := func(id string) Turn {
		return Turn{
			Text:      "Trying to run a command.",
			ToolCalls: []message.ToolCall{ToolCall(id, tools.ShellToolName, `{"command":"echo hello"}`)},
		}
	}
	script := NewScriptedLLM(
		toolTurn("call-off-1"),
		toolTurn("call-off-2"),
		toolTurn("call-off-3"),
		// Played only after the machine reconnects.
		Turn{Text: "Machine is back — wrapping up."},
	)
	h := newHarness(t, script, WithToolExecutor(daemonOfflineExecutor{}))

	created := h.StartChat("builtin://agent", "Run a command for me", map[string]any{"mode": "auto"})
	chatID, workflowID := created.Chat.Id, created.WorkflowId

	h.WaitWorkflowStatus(workflowID, db.Paused())
	require.Len(t, h.LLM.StreamCalls(), 3)
	require.NotNil(t, h.pausedForMachineAt(chatID), "the breaker's pause must mark the chat")

	resumed, err := h.ChatSvc.ResumePausedForMachine(h.Ctx, chatID)
	require.NoError(t, err)
	require.True(t, resumed)

	h.WaitTemporalWorkflowDone(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())
	assert.Len(t, h.LLM.StreamCalls(), 4, "exactly one more LLM turn after the reconnect")
	assert.Nil(t, h.pausedForMachineAt(chatID), "the resume clears the mark")

	var sawNote, sawFinal bool
	for _, m := range h.Messages(chatID, workflowID) {
		text := TextOf(m)
		sawNote = sawNote || strings.Contains(text, "Your machine has reconnected")
		sawFinal = sawFinal || strings.Contains(text, "Machine is back — wrapping up.")
	}
	assert.True(t, sawNote, "the agent is told its machine is back")
	assert.True(t, sawFinal)

	again, err := h.ChatSvc.ResumePausedForMachine(h.Ctx, chatID)
	require.NoError(t, err)
	assert.False(t, again, "a second connect finds nothing to resume")
}

func (h *Harness) pausedForMachineAt(chatID string) *struct{} {
	h.T.Helper()
	paused, err := h.Stack.Repo.ListChatsPausedForMachine(h.Ctx, h.UserID, 100)
	require.NoError(h.T, err)
	for _, c := range paused {
		if c.ChatID == chatID {
			return &struct{}{}
		}
	}
	return nil
}
