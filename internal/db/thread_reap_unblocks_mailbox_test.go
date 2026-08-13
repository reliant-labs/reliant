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

// TestReapOrphanedThreads_UnblocksOrphanedMailboxSweep is the second-order
// effect the incident briefing calls out as mattering more than the cosmetics
// (docs/incidents/2026-08-12-spawn-history-cap.md, "Gap 2"):
//
// ListThreadsWithOrphanedAgentMessages -- the sweep that resolves a dead
// thread's still-queued mailbox rows -- only matches threads already in a
// TERMINAL status (3, 4, 5, 7). A thread stranded at running (2) under an
// already-terminal workflow therefore makes its OWN orphaned mailbox
// permanently invisible to the sweep that exists to resolve it. Two such rows
// were observed queued forever in the incident chat.
//
// This test pins both directions in one pass: the mailbox is invisible while
// the thread is stranded, and the reap is what makes it visible.
func TestReapOrphanedThreads_UnblocksOrphanedMailboxSweep(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-reap-unblocks"
	createActivityTestChat(t, repo, chatID)

	// A completed workflow whose thread was never cascaded -- the exact
	// 288-row shape.
	strandedWf := "wf-unblock-stranded"
	strandedThread := "th-unblock-stranded"
	insertTestWorkflowWithParent(t, repo, strandedWf, chatID, nil, WorkflowStatusCompleted)
	insertTestThreadForWorkflow(t, repo, strandedThread, chatID, strandedWf, ThreadStatusRunning)

	// A repair report addressed to that dead thread, still queued: exactly
	// the undeliverable row repairStrandedBackgroundSpawns leaves behind.
	require.NoError(t, repo.EnqueueAgentMessage(ctx, &AgentMessage{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		FromThreadID: chatID,
		ToThreadID:   strandedThread,
		Kind:         core.AgentMessageKindCompletion,
		Body:         "a report nothing will ever deliver",
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}))

	// BEFORE: the sweep cannot see it. The thread is at running (2), and the
	// query matches only terminal statuses.
	before, err := repo.ListThreadsWithOrphanedAgentMessages(ctx)
	require.NoError(t, err)
	require.NotContains(t, before, strandedThread,
		"precondition: a thread stranded at running hides its own orphaned mailbox from the sweep — this is the bug")

	reaped, err := repo.ReapOrphanedThreads(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), reaped)

	// AFTER: the thread is terminal, so the sweep that exists to resolve
	// this mailbox can finally reach it.
	after, err := repo.ListThreadsWithOrphanedAgentMessages(ctx)
	require.NoError(t, err)
	require.Contains(t, after, strandedThread,
		"once the thread carries its workflow's terminal status, its orphaned mailbox becomes visible to the sweep")
}
