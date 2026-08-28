// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// An interrupted stream must CANCEL the tool calls it opened, durably.
//
// Reproduced from chat 8bbd2429: the user paused at 19:39:07 while a tool call
// was mid-input. The pause path reported cancelledToolCalls=0 (it looks for
// dispatched tool EXECUTIONS, and this call had never been dispatched), and
// CleanupActivity.cancelOrphanedToolCalls — the durable repair — runs only
// from handleWorkflowCompletion, which a pause never reaches.
//
// So nothing server-side recorded the cancellation. The client was left to
// INFER it from the stream ending, and the `tool_cancelled` delta it listens
// for was declared but never emitted by any Go code. Where that inference got
// grafted onto a message the server had not persisted, no later snapshot could
// correct it and the card span forever.
//
// These tests pin the server half: the interrupt is what knows which calls
// died, so the interrupt is what has to say so.
func TestInterruptedStream_CancelsItsInFlightToolCalls(t *testing.T) {
	f := setupDurableStatusFixture(t)
	repo := f.h.Repo()
	ctx := context.Background()

	activityInstance := &CallLLMActivity{repo: repo}
	state := &streamProcessingState{blockStates: NewBlockStreamState()}

	// The provider opens a tool call. Input is still empty at tool_use_start,
	// which is precisely why state.toolCalls cannot be the record here.
	const callID = "toolu_interrupted_midinput"
	activityInstance.handleToolUseStart(ctx, f.chatID, f.chatID, llm.DriverEvent{
		Type:     llm.EventToolUseStart,
		ToolCall: &message.ToolCall{ID: callID, Name: "edit"},
	}, state)

	require.Len(t, state.streamingToolCalls, 1,
		"the open call must be tracked; it is the only record of what the UI is showing")
	require.Empty(t, state.toolCalls,
		"precondition: a call interrupted mid-input never reaches state.toolCalls")

	// The user pauses. The activity context is dead by the time we unwind,
	// exactly as it is in production.
	dead, cancel := context.WithCancel(ctx)
	cancel()

	activityInstance.cancelInFlightStreamingToolCalls(dead, f.chatID, f.chatID, state)

	// The durable half: a reload, a late-joining client, and the orphan-repair
	// pass must all agree that this call is over.
	got, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err, "a cancelled call must leave a durable row behind")
	require.NotNil(t, got)
	require.Equal(t, core.ToolCallStatusCancelled, got.Status,
		"the interrupted call is still not terminal — the UI has nothing to correct its "+
			"inference with, and a card grafted onto an unpersisted message spins forever")
}

// A tool that genuinely finished must keep its real outcome. Cancelling a
// sibling must never repaint it.
func TestInterruptedStream_DoesNotCancelCallsThatCompleted(t *testing.T) {
	f := setupDurableStatusFixture(t)
	repo := f.h.Repo()
	ctx := context.Background()

	activityInstance := &CallLLMActivity{repo: repo}
	state := &streamProcessingState{blockStates: NewBlockStreamState()}

	const finishedID = "toolu_finished_before_pause"
	const orphanedID = "toolu_still_open_at_pause"

	for _, tc := range []struct{ id, name string }{
		{finishedID, "bash"},
		{orphanedID, "edit"},
	} {
		activityInstance.handleToolUseStart(ctx, f.chatID, f.chatID, llm.DriverEvent{
			Type:     llm.EventToolUseStart,
			ToolCall: &message.ToolCall{ID: tc.id, Name: tc.name},
		}, state)
	}

	// The first call finished describing itself, so it lands in state.toolCalls
	// the way handleComplete would put it there. It belongs to the turn that
	// gets persisted and re-dispatched — not to what the interrupt orphaned.
	state.toolCalls = []message.ToolCall{
		{ID: finishedID, Name: "bash", Input: `{"command":"ls"}`},
	}

	dead, cancel := context.WithCancel(ctx)
	cancel()
	activityInstance.cancelInFlightStreamingToolCalls(dead, f.chatID, f.chatID, state)

	orphan, err := repo.GetToolCall(ctx, orphanedID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusCancelled, orphan.Status,
		"the call that was still open when the stream died must be cancelled")

	// The completed one must not have been given a cancelled row at all.
	finished, err := repo.GetToolCall(ctx, finishedID)
	if err == nil && finished != nil {
		require.NotEqual(t, core.ToolCallStatusCancelled, finished.Status,
			"a tool that finished before the interrupt was repainted as cancelled — "+
				"cancelling one tool must never take its siblings down with it")
	}
}

// The terminal-status guard is the safety net for the race that actually
// happens: a tool completes microseconds before the interrupt unwinds.
func TestInterruptedStream_RespectsAnAlreadyTerminalRow(t *testing.T) {
	f := setupDurableStatusFixture(t)
	repo := f.h.Repo()
	ctx := context.Background()

	activityInstance := &CallLLMActivity{repo: repo}
	state := &streamProcessingState{blockStates: NewBlockStreamState()}

	const callID = "toolu_completed_in_the_race_window"
	activityInstance.handleToolUseStart(ctx, f.chatID, f.chatID, llm.DriverEvent{
		Type:     llm.EventToolUseStart,
		ToolCall: &message.ToolCall{ID: callID, Name: "bash"},
	}, state)

	// It completed for real, just before the interrupt reached us.
	now := time.Now().UTC()
	require.NoError(t, upsertCompletedToolCall(ctx, repo, f.chatID, callID, now))

	dead, cancel := context.WithCancel(ctx)
	cancel()
	activityInstance.cancelInFlightStreamingToolCalls(dead, f.chatID, f.chatID, state)

	got, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusCompleted, got.Status,
		"a real completion was overwritten by an inferred cancel — the durable row must win")
}

// upsertCompletedToolCall seeds a call that finished normally.
func upsertCompletedToolCall(
	ctx context.Context,
	repo db.Repository,
	chatID, callID string,
	at time.Time,
) error {
	return db.UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusCompleted,
		StartedAt:   &at,
		CompletedAt: &at,
		RequestedAt: at,
		CreatedAt:   at,
		UpdatedAt:   at,
	})
}
