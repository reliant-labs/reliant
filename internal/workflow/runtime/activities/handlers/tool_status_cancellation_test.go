// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// A tool call's terminal status is written on exactly the paths where the
// request context is already dead: user cancellation, workflow termination,
// activity timeout. Writing it with that dead context fails with "context
// canceled" and the row stays at EXECUTING forever -- the UI then shows a
// spinner on a tool that finished long ago.
//
// Observed in production: 34 "Failed to persist tool call" / context-canceled
// errors in a single worker log, leaving 46 rows stuck at EXECUTING. 38 of
// those had already written their tool_result content block, proving the tool
// itself finished and only the bookkeeping was lost.
func TestTerminalStatusSurvivesCancelledContext(t *testing.T) {
	f := setupDurableStatusFixture(t)
	repo := f.h.Repo()
	base := context.Background()

	const callID = "toolu_cancelled_terminal_write"
	now := time.Now()

	// The call is mid-flight, as it would be when a workflow is torn down.
	require.NoError(t, db.UpsertToolCallStatus(base, repo, &core.ToolCall{
		ID: callID, ChatID: f.chatID, ToolName: "bash",
		Status: core.ToolCallStatusExecuting, StartedAt: &now,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}), "seed executing row")

	// The activity's context dies BEFORE the terminal write is attempted.
	cancelled, cancel := context.WithCancel(base)
	cancel()

	detached, release := detachedForTerminalWrite(cancelled)
	defer release()

	completed := time.Now()
	err := db.UpsertToolCallStatus(detached, repo, &core.ToolCall{
		ID: callID, ChatID: f.chatID, ToolName: "bash",
		Status: core.ToolCallStatusCancelled, StartedAt: &now, CompletedAt: &completed,
		RequestedAt: now, CreatedAt: now, UpdatedAt: completed,
	})
	require.NoError(t, err, "terminal write must survive a cancelled parent context")

	got, err := repo.GetToolCall(base, callID)
	require.NoError(t, err)
	require.NotEqual(t, core.ToolCallStatusExecuting, got.Status,
		"tool call is still EXECUTING -- the terminal write was lost and the UI would show it running forever")
	require.Equal(t, core.ToolCallStatusCancelled, got.Status)
}

// Backgrounded is deliberately NOT terminal: that process is still running and
// will report a real outcome later, so it must not be force-written past a
// cancellation that is genuinely ending the work.
func TestIsTerminalExcludesBackgrounded(t *testing.T) {
	for status, want := range map[core.ToolCallStatus]bool{
		core.ToolCallStatusCompleted:    true,
		core.ToolCallStatusFailed:       true,
		core.ToolCallStatusCancelled:    true,
		core.ToolCallStatusBackgrounded: false,
		core.ToolCallStatusExecuting:    false,
		core.ToolCallStatusPending:      false,
		core.ToolCallStatusUnspecified:  false,
	} {
		require.Equalf(t, want, status.IsTerminal(), "status %v", status)
	}
}
