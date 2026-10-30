// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

func (c *endedRunTemporalClient) CancelWorkflow(_ context.Context, workflowID, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.canceled = append(c.canceled, workflowID+"/"+runID)
	return nil
}

func (c *endedRunTemporalClient) cancels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.canceled...)
}

// Prod 2026-10-29: the cicd-probe chats were archived, and archiving must end
// everything the chat holds for its machine. A message queued for the machine
// is dropped with the archive — not merely skipped by the delivery sweep while
// archived — so neither a delivery now nor unarchiving later starts a run.
func TestArchive_DropsTheMessageQueuedForTheMachine(t *testing.T) {
	t.Parallel()

	f := newContinueFixture(t, true)
	f.queueForMachine(t)

	_, err := f.svc.UpdateChatState(f.ctx, connect.NewRequest(&reliantv1.UpdateChatStateRequest{
		ChatId: f.chatID, State: reliantv1.ChatState_CHAT_STATE_ARCHIVED,
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{f.chatID + "/"}, f.temporal.cancels(), "archiving stops the chat's current run")

	started, err := f.svc.ContinueQueued(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.False(t, started, "an archived chat is never delivered to")

	_, err = f.svc.UpdateChatState(f.ctx, connect.NewRequest(&reliantv1.UpdateChatStateRequest{
		ChatId: f.chatID, State: reliantv1.ChatState_CHAT_STATE_IDLE,
	}))
	require.NoError(t, err)
	// The delivery sweep and a machine connect deliver only what this lists.
	queued, err := f.repo.ListChatsQueuedForMachine(f.ctx, "", 10)
	require.NoError(t, err)
	assert.Empty(t, queued, "unarchiving does not revive the queued message")
	assert.Empty(t, f.temporal.runsStarted(), "no run is ever started for the archived message")
	assert.Zero(t, f.pause.resetCount())
}

// Archiving takes the chat's run-control lock, the lock a delivery holds from
// reading the chat to recording the run it starts. So a delivery already past
// its read finishes first and the archive's cancel stops its run; it can never
// start a run after the archive's cancel went out.
func TestArchive_WaitsForADeliveryInProgress(t *testing.T) {
	t.Parallel()

	f := newContinueFixture(t, true)
	chat, err := f.repo.GetChat(f.ctx, f.chatID)
	require.NoError(t, err)

	release, err := f.repo.LockChatRunControl(f.ctx, f.chatID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- f.svc.archiveChat(f.ctx, chat, "user_archived") }()

	select {
	case err := <-done:
		t.Fatalf("archive finished while a delivery held the run-control lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	got, err := f.repo.GetChat(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.NotEqual(t, db.ChatStateArchived, got.State, "not archived until the delivery lets go")
	assert.Empty(t, f.temporal.cancels())

	release()
	require.NoError(t, <-done)
	got, err = f.repo.GetChat(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.Equal(t, db.ChatStateArchived, got.State)
	assert.Equal(t, []string{f.chatID + "/"}, f.temporal.cancels())
}
