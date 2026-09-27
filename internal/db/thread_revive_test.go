// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			wfID := "wf-revive-" + tc.name
			threadID := "th-revive-" + tc.name
			insertTestWorkflowWithParent(t, repo, wfID, chatID, nil, Completed())
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
			insertTestWorkflowWithParent(t, repo, wfID, chatID, nil, Active())
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

	insertTestWorkflowWithParent(t, repo, "wf-scope-main", chatID, nil, Completed())
	insertTestThreadForWorkflow(t, repo, "th-scope-main", chatID, "wf-scope-main", ThreadStatusCompleted)

	parent := "wf-scope-main"
	insertTestWorkflowWithParent(t, repo, "wf-scope-spawn", chatID, &parent, Completed())
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

// TestUpdateWorkflowStatus_ReopenRevivesOwnThread pins the reopening half of
// the workflow/thread lifecycle at the one writer every reopen goes through.
//
// ReviveThread was only called from WorkflowStatusActivity's "started" arm, so
// a run reopened by any OTHER path left its thread terminal behind a live
// workflow. The incident: the reconciler terminated a wedged main workflow
// and marked it failed, ReapOrphanedThreads stamped its thread failed, and
// the user's resume reset-and-replayed the run. Replay does not re-execute
// the already-completed "started" activity, and PauseService wrote Active
// straight onto the workflow row — so the main thread read "failed" while it
// kept spawning sub-agents. SendAgentMessage then refused to queue into it
// ("This agent has already finished (status: failed)"), and the stranded-
// spawn sweep marked two finished sub-agents' reports UNDELIVERED.
func TestUpdateWorkflowStatus_ReopenRevivesOwnThread(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-reopen-revive"
	createActivityTestChat(t, repo, chatID)

	for _, tc := range []struct {
		name         string
		workflowWas  WorkflowStatus
		threadStatus int32
	}{
		{"wedge-terminated then reset-resumed", Failed(), ThreadStatusFailed},
		{"completed then reopened", Completed(), ThreadStatusCompleted},
		{"cancelled then reopened", Cancelled(), ThreadStatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mainID := "wf-reopen-main-" + tc.name
			insertTestWorkflowWithParent(t, repo, mainID, chatID, nil, tc.workflowWas)
			insertTestThreadForWorkflow(t, repo, mainID, chatID, mainID, ThreadStatusRunning)
			completedAt := time.Now().UTC()
			if _, err := repo.UpdateThreadStatus(ctx, mainID, tc.threadStatus, &completedAt); err != nil {
				t.Fatalf("UpdateThreadStatus(main): %v", err)
			}

			// A spawn the reaper closed alongside the main thread. It really
			// did stop, and reopening its parent must not resurrect it.
			spawnID := "wf-reopen-spawn-" + tc.name
			insertTestWorkflowWithParent(t, repo, spawnID, chatID, &mainID, tc.workflowWas)
			insertTestThreadForWorkflow(t, repo, spawnID, chatID, spawnID, ThreadStatusRunning)
			if _, err := repo.UpdateThreadStatus(ctx, spawnID, tc.threadStatus, &completedAt); err != nil {
				t.Fatalf("UpdateThreadStatus(spawn): %v", err)
			}

			if err := repo.UpdateWorkflowStatus(ctx, mainID, Active()); err != nil {
				t.Fatalf("UpdateWorkflowStatus(Active): %v", err)
			}

			mainThread, err := repo.GetThread(ctx, mainID)
			if err != nil {
				t.Fatalf("GetThread(main): %v", err)
			}
			if mainThread.Status != ThreadStatusRunning {
				t.Errorf("main thread status = %s, want running: a reopened run's own thread must reopen with it",
					core.ThreadStatusLabel(mainThread.Status))
			}
			if mainThread.CompletedAt != nil {
				t.Errorf("main thread completed_at = %v, want nil on a reopened thread", mainThread.CompletedAt)
			}

			spawnThread, err := repo.GetThread(ctx, spawnID)
			if err != nil {
				t.Fatalf("GetThread(spawn): %v", err)
			}
			if spawnThread.Status != tc.threadStatus {
				t.Errorf("spawn thread status = %s, want %s untouched",
					core.ThreadStatusLabel(spawnThread.Status), core.ThreadStatusLabel(tc.threadStatus))
			}
		})
	}
}

// TestUpdateWorkflowStatus_ActiveOntoLiveRunLeavesThreadAlone guards the
// reopen revival from the direction that cannot be undone: only a run that
// had ENDED is reopened. An Active write onto a run that never ended is not
// a reopen, and reviving a thread there would invite a message into an agent
// that has stopped.
//
//   - Already Active: a spawn thread that has just written its own
//     "completed" sits behind a still-Active workflow for the moment before
//     its workflow follows, and a redundant Active write can land in that
//     window (EnsureWorkflowRunning, a racing resume).
//   - Paused: a spawn that finished before a chat-wide pause keeps its
//     finished thread when the run resumes.
func TestUpdateWorkflowStatus_ActiveOntoLiveRunLeavesThreadAlone(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-active-onto-live"
	createActivityTestChat(t, repo, chatID)

	for _, tc := range []struct {
		name        string
		workflowWas WorkflowStatus
	}{
		{"already active", Active()},
		{"paused", Paused()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wfID := "wf-active-onto-live-" + tc.name
			insertTestWorkflowWithParent(t, repo, wfID, chatID, nil, tc.workflowWas)
			insertTestThreadForWorkflow(t, repo, wfID, chatID, wfID, ThreadStatusRunning)
			completedAt := time.Now().UTC()
			if _, err := repo.UpdateThreadStatus(ctx, wfID, ThreadStatusCompleted, &completedAt); err != nil {
				t.Fatalf("UpdateThreadStatus: %v", err)
			}

			if err := repo.UpdateWorkflowStatus(ctx, wfID, Active()); err != nil {
				t.Fatalf("UpdateWorkflowStatus(Active): %v", err)
			}

			thread, err := repo.GetThread(ctx, wfID)
			if err != nil {
				t.Fatalf("GetThread: %v", err)
			}
			if thread.Status != ThreadStatusCompleted {
				t.Errorf("thread status = %s, want completed: an Active write onto a run that never ended is not a reopen",
					core.ThreadStatusLabel(thread.Status))
			}
		})
	}
}
