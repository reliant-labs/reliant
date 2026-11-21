// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// A workflow that starts several children at once in one chat (a parallel
// node, a fan-out of spawns) runs one CreateWorkflowWithThread per child, all
// at the same moment. Every one inserts an active workflows row, and that
// insert's recovery_workflow_admission trigger locks the chat row and bumps
// activity_generation on the chat's worktree row: a write every admission in
// the chat makes to the same row. Under SERIALIZABLE a transaction that waits
// for that row lock and then finds the row updated by the one it waited for
// fails with SQLSTATE 40001 "could not serialize access due to concurrent
// update" — every waiter, on every attempt, so a burst of children exhausted
// all seven attempts (prod issues 1f232560 and 53e2370c). Under READ
// COMMITTED the waiter re-reads the row it waited for and goes on, so no
// attempt is retried at all: counting retries, not timing them, keeps this
// exact on a loaded host.
func TestCreateWorkflowWithThread_ConcurrentChildrenInOneChatDoNotConflict(t *testing.T) {
	t.Parallel()
	const children = 8

	for _, tc := range []struct {
		name string
		// inherit gives every child the same, already existing thread (the
		// inherit thread mode); otherwise each child creates its own.
		inherit bool
	}{
		{name: "new threads"},
		{name: "one inherited thread", inherit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newTestHelper(t)
			defer h.Close()
			ctx := context.Background()
			chatID := h.createChatInWorktree(t)
			mainThread, _ := h.createThread("", chatID)

			// The recorder is safe for the children's concurrent retries: it
			// appends under its own mutex, and Len reads under it
			// (internal/db/testutil.go, RetryBackoffs).
			retries := db.RecordRetryBackoffs(h.repo)
			start := make(chan struct{})
			errs := make([]error, children)
			var wg sync.WaitGroup
			for i := range children {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					opts := CreateWorkflowWithThreadOpts{
						Workflow: &db.Workflow{
							ID:           uuid.NewString(),
							ChatID:       chatID,
							WorkflowName: fmt.Sprintf("child-%d", i),
							Status:       db.Active(),
							CreatedAt:    time.Now().UTC(),
						},
						ChatID:       chatID,
						ThreadID:     uuid.NewString(),
						ParentThread: &mainThread.ID,
					}
					if tc.inherit {
						opts.ThreadID = mainThread.ID
					}
					opts.Workflow.Thread = opts.ThreadID
					_, _, _, errs[i] = h.svc.CreateWorkflowWithThread(ctx, opts)
				}()
			}
			close(start)
			wg.Wait()

			for i, err := range errs {
				require.NoError(t, err, "child %d of %d", i+1, children)
			}
			assert.Zero(t, retries.Len(), "no CreateWorkflowWithThread transaction conflicted and had to be retried")
		})
	}
}

// createChatInWorktree creates a chat bound to a worktree of the helper's
// project, as every chat is in production: the worktree row is the one the
// workflow admission trigger writes.
func (h *testHelper) createChatInWorktree(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	worktreeID := uuid.NewString()
	require.NoError(t, h.repo.CreateWorktree(ctx, &db.Worktree{
		ID: worktreeID, Name: "wt-" + worktreeID[:8], Path: t.TempDir(), Branch: "b-" + worktreeID[:8], BaseBranch: "main",
		ProjectID: h.projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.NewString()
	require.NoError(t, h.repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: h.projectID, Title: "Concurrent children", WorktreeID: &worktreeID,
	}))
	return chatID
}
