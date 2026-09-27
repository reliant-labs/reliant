// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func terminalReport(chatID, from, to, toolCallID string, kind core.AgentMessageKind, body string) *AgentMessage {
	return &AgentMessage{
		ID: uuid.New().String(), ChatID: chatID, FromThreadID: from, ToThreadID: to,
		Kind: kind, Body: body, ToolCallID: &toolCallID,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
	}
}

// TestEnqueueTerminalAgentReport_ReplacesReconcilerStandIn is the incident on
// chat e6c09159. The reconciler's stranded-spawn sweep wrote an UNDELIVERED
// stand-in ("this report was never delivered") into a spawn's one terminal-
// report slot. A resume then reset-and-replayed the run, the spawn finished
// for real, and its report died on idx_agent_messages_one_terminal_report_per_spawn
// (SQLSTATE 23505) — so the parent never saw what the sub-agent produced.
//
// The real report must replace the stand-in and be queued for the parent.
func TestEnqueueTerminalAgentReport_ReplacesReconcilerStandIn(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	toolCallID := "tc-standin-replaced"

	standIn := terminalReport(chatID, childThreadID, parentThreadID, toolCallID,
		core.AgentMessageKindFailed, "Sub-agent finished, but the thread that spawned it had already exited")
	standIn.Status = core.AgentMessageStatusUndelivered
	inserted, err := repo.EnqueueAgentMessageIfAbsent(ctx, standIn)
	require.NoError(t, err)
	require.True(t, inserted)

	real := terminalReport(chatID, childThreadID, parentThreadID, toolCallID,
		core.AgentMessageKindCompletion, "found the bug: the thread row was never revived")
	rowID, err := repo.EnqueueTerminalAgentReport(ctx, real)
	require.NoError(t, err, "a real report must never fail on a reconciler stand-in")
	require.Equal(t, standIn.ID, rowID,
		"the report is written into the stand-in's row, so the id returned must be the row's, not the caller's")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, parentThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the parent must have exactly one report for this spawn, queued for delivery")
	require.Equal(t, "found the bug: the thread row was never revived", queued[0].Body)
	require.Equal(t, core.AgentMessageKindCompletion, queued[0].Kind)

	var synthesized bool
	var deliveredAtSet bool
	require.NoError(t, rawDB.QueryRowContext(ctx,
		`SELECT synthesized, delivered_at IS NOT NULL FROM agent_messages WHERE id = $1`, standIn.ID,
	).Scan(&synthesized, &deliveredAtSet))
	require.False(t, synthesized, "a replaced stand-in carries a real report now; it must not be replaceable again")
	require.False(t, deliveredAtSet)
}

// TestEnqueueTerminalAgentReport_KeepsExistingRealReport guards the other
// collision, which wants the opposite outcome. When the slot already holds a
// REAL report — a retry of the same activity, or a replay re-executing a
// report that already landed — the existing row stands and the call is a
// quiet no-op. Overwriting it could re-queue a report the parent already read.
func TestEnqueueTerminalAgentReport_KeepsExistingRealReport(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	toolCallID := "tc-real-kept"

	first := terminalReport(chatID, childThreadID, parentThreadID, toolCallID,
		core.AgentMessageKindCompletion, "first real report")
	rowID, err := repo.EnqueueTerminalAgentReport(ctx, first)
	require.NoError(t, err)
	require.Equal(t, first.ID, rowID, "a fresh report lands under its own id")

	replay := terminalReport(chatID, childThreadID, parentThreadID, toolCallID,
		core.AgentMessageKindCompletion, "replayed report — must not land")
	rowID, err = repo.EnqueueTerminalAgentReport(ctx, replay)
	require.NoError(t, err, "a duplicate real report must be a no-op, not SQLSTATE 23505")
	require.Empty(t, rowID, "no row was written for the duplicate")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, parentThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, "first real report", queued[0].Body)

	// And a stand-in arriving AFTER a real report must not displace it.
	standIn := terminalReport(chatID, childThreadID, parentThreadID, toolCallID,
		core.AgentMessageKindFailed, "stand-in — must not land")
	inserted, err := repo.EnqueueAgentMessageIfAbsent(ctx, standIn)
	require.NoError(t, err)
	require.False(t, inserted)
}
