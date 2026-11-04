// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// Archiving a worktree archives its chats under each chat's run-control lock,
// as the chat service's archive does. A delivery that read the chat and holds
// the lock while it starts a run must finish starting it before the archive
// and the cancel happen, so the cancel stops that run instead of preceding it.
func TestArchiveWorktreeChat_WaitsForADeliveryHoldingTheRunControlLock(t *testing.T) {
	var (
		runControl sync.Mutex
		mu         sync.Mutex
		events     []string
	)
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	repo := &MockRepository{
		LockChatRunControlFunc: func(context.Context, string) (func(), error) {
			runControl.Lock()
			return runControl.Unlock, nil
		},
		UpdateChatStateFunc: func(_ context.Context, _ string, state db.ChatState, _ string) error {
			record("archive:" + state.String())
			return nil
		},
	}
	temporal := &StubTemporal{CancelWorkflowFunc: func(context.Context, string, string) error {
		record("cancel")
		return nil
	}}
	svc := NewTestWorktree(t, WithWorktreeDeps(Deps{Repo: repo, Temporal: temporal}))

	// The delivery has read the chat as live and holds the lock while it
	// starts the chat's run.
	runControl.Lock()
	workflowID := "wf-1"
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.archiveWorktreeChat(context.Background(), &db.Chat{ID: "chat-1", WorkflowID: &workflowID})
	}()

	time.Sleep(50 * time.Millisecond)
	record("delivery started run")
	runControl.Unlock()
	<-done

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 3)
	assert.Equal(t, []string{"delivery started run", "archive:" + db.ChatStateArchived.String(), "cancel"}, events)
}
