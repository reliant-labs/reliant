// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// TestReviveThread is the inverse of TestReapOrphanedThreads, and pins the
// half of the thread lifecycle that was missing entirely: threads.status was
// only ever written in the CLOSING direction.
//
// That is harmless for a spawned sub-agent, whose thread is created fresh per
// run and legitimately ends once. But a chat's MAIN thread is reused for
// every turn -- SendMessage starts a new Temporal run under the same workflow
// ID and the same thread ID. Turn N stamped the thread terminal; turn N+1
// revived only the workflow row, so every chat from its second turn onward
// ran with a live workflow behind a thread that still read completed.
//
// The user-visible cost was SendAgentMessage refusing to queue into a
// visibly-working agent with "This agent has already finished (status:
// completed)". Measured on the live DB at the time of the fix: 3 chats with a
// RUNNING workflow and a terminal main thread.
func TestReviveThread(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-thread-revive"
	createActivityTestChat(t, repo, chatID)

	// Every terminal status a previous turn could have left behind must be
	// revivable -- a turn that failed or was cancelled is followed by a new
	// turn on the same thread exactly like a completed one.
	for _, tc := range []struct {
		name   string
		status int32
	}{
		{"previous turn completed", ThreadStatusCompleted},
		{"previous turn failed", ThreadStatusFailed},
		{"previous turn was cancelled", ThreadStatusCancelled},
		{"previous turn expired", ThreadStatusExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wfID := "wf-revive-" + tc.name
			threadID := "th-revive-" + tc.name
			insertTestWorkflowWithParent(t, repo, wfID, chatID, nil, WorkflowStatusCompleted)
			insertTestThreadForWorkflow(t, repo, threadID, chatID, wfID, ThreadStatusRunning)

			completedAt := time.Now().UTC()
			if _, err := repo.UpdateThreadStatus(ctx, threadID, tc.status, &completedAt); err != nil {
				t.Fatalf("UpdateThreadStatus: %v", err)
			}

			rows, err := repo.ReviveThread(ctx, threadID)
			if err != nil {
				t.Fatalf("ReviveThread: %v", err)
			}
			if rows != 1 {
				t.Fatalf("ReviveThread moved %d rows, want 1", rows)
			}

			thread, err := repo.GetThread(ctx, threadID)
			if err != nil {
				t.Fatalf("GetThread: %v", err)
			}
			if thread.Status != ThreadStatusRunning {
				t.Errorf("status = %d, want %d (running)", thread.Status, ThreadStatusRunning)
			}
			// A revived thread that keeps the previous turn's completed_at
			// reads as "finished at 16:31" while it is executing.
			if thread.CompletedAt != nil {
				t.Errorf("completed_at = %v, want nil on a revived thread", thread.CompletedAt)
			}
		})
	}
}

// TestReviveThread_LeavesLiveThreadsAlone guards the write from the direction
// that cannot be undone. Reviving is only ever correct for a thread that has
// already stopped; clearing a live thread's bookkeeping would be a silent
// corruption of a row nothing else revisits.
func TestReviveThread_LeavesLiveThreadsAlone(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-thread-revive-live"
	createActivityTestChat(t, repo, chatID)

	for _, tc := range []struct {
		name   string
		status int32
	}{
		{"already running", ThreadStatusRunning},
		{"paused and resumable", int32(6)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wfID := "wf-revive-live-" + tc.name
			threadID := "th-revive-live-" + tc.name
			insertTestWorkflowWithParent(t, repo, wfID, chatID, nil, WorkflowStatusRunning)
			insertTestThreadForWorkflow(t, repo, threadID, chatID, wfID, tc.status)

			rows, err := repo.ReviveThread(ctx, threadID)
			if err != nil {
				t.Fatalf("ReviveThread: %v", err)
			}
			if rows != 0 {
				t.Fatalf("ReviveThread moved %d rows on a non-terminal thread, want 0", rows)
			}

			thread, err := repo.GetThread(ctx, threadID)
			if err != nil {
				t.Fatalf("GetThread: %v", err)
			}
			if thread.Status != tc.status {
				t.Errorf("status = %d, want %d unchanged", thread.Status, tc.status)
			}
		})
	}
}

// TestReviveThread_OnlyTargetThread pins the scoping: a revival is about the
// one thread a run is starting on. A chat has many threads -- its spawned
// sub-agents legitimately stay finished when the main thread takes a new
// turn, and reviving them would resurrect agents that are not running.
func TestReviveThread_OnlyTargetThread(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-thread-revive-scope"
	createActivityTestChat(t, repo, chatID)

	insertTestWorkflowWithParent(t, repo, "wf-scope-main", chatID, nil, WorkflowStatusCompleted)
	insertTestThreadForWorkflow(t, repo, "th-scope-main", chatID, "wf-scope-main", ThreadStatusCompleted)

	parent := "wf-scope-main"
	insertTestWorkflowWithParent(t, repo, "wf-scope-spawn", chatID, &parent, WorkflowStatusCompleted)
	insertTestThreadForWorkflow(t, repo, "th-scope-spawn", chatID, "wf-scope-spawn", ThreadStatusCompleted)

	if _, err := repo.ReviveThread(ctx, "th-scope-main"); err != nil {
		t.Fatalf("ReviveThread: %v", err)
	}

	spawn, err := repo.GetThread(ctx, "th-scope-spawn")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if spawn.Status != ThreadStatusCompleted {
		t.Errorf("sibling spawn thread status = %d, want %d (untouched)",
			spawn.Status, ThreadStatusCompleted)
	}
}

// TestReviveThread_UnknownThread is the no-op contract for a thread ID that
// does not exist: a caller on the hot "started" path must get 0 rows and no
// error, not a failure that retries the whole status notification.
func TestReviveThread_UnknownThread(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	rows, err := repo.ReviveThread(context.Background(), "th-does-not-exist")
	if err != nil {
		t.Fatalf("ReviveThread on unknown thread: %v", err)
	}
	if rows != 0 {
		t.Errorf("ReviveThread moved %d rows for an unknown thread, want 0", rows)
	}
}
