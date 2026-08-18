// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
)

// TestReapOrphanedThreads is the invariant
// CascadeTerminalStatusToThreadSubtree asserts, read from the other
// direction: a thread whose WORKFLOW is terminal is not running.
//
// This pins the measured regression from
// docs/incidents/2026-08-12-spawn-history-cap.md: threads.status is written
// ONLY by ThreadStatusActivity on the live path, so any write path that
// forgets (or is unable, e.g. a hard Temporal terminate) to run the direct
// cascade strands the thread at running (2) / paused (6) forever. Measured on
// the live DB: 288 rows in exactly this state -- 174 whose workflow had
// completed, 64 cancelled, 50 failed.
func TestReapOrphanedThreads(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-thread-reap"
	createActivityTestChat(t, repo, chatID)

	// Completed workflow, thread still running -- the exact shape measured
	// live (174 of the 288 rows).
	completedWf := "wf-tr-completed"
	completedThread := "th-tr-completed"
	insertTestWorkflowWithParent(t, repo, completedWf, chatID, nil, Completed())
	insertTestThreadForWorkflow(t, repo, completedThread, chatID, completedWf, ThreadStatusRunning)

	// Cancelled workflow, thread still running (64 of the 288).
	cancelledWf := "wf-tr-cancelled"
	cancelledThread := "th-tr-cancelled"
	insertTestWorkflowWithParent(t, repo, cancelledWf, chatID, nil, Cancelled())
	insertTestThreadForWorkflow(t, repo, cancelledThread, chatID, cancelledWf, ThreadStatusRunning)

	// Failed workflow, thread still PAUSED -- the reap must catch paused
	// threads too, not just running ones (50 of the 288 were failed; not all
	// stranded threads necessarily sat at "running").
	failedWf := "wf-tr-failed"
	failedThread := "th-tr-failed"
	insertTestWorkflowWithParent(t, repo, failedWf, chatID, nil, Failed())
	insertTestThreadForWorkflow(t, repo, failedThread, chatID, failedWf, int32(6))

	// A LIVE workflow with a live thread. Reaping this would falsely close
	// an in-flight agent's conversation -- a far worse failure than the one
	// being fixed.
	liveWf := "wf-tr-live"
	liveThread := "th-tr-live"
	insertTestWorkflowWithParent(t, repo, liveWf, chatID, nil, Active())
	insertTestThreadForWorkflow(t, repo, liveThread, chatID, liveWf, ThreadStatusRunning)

	// A PAUSED workflow with a running thread: pause is resumable, not
	// terminal, so the thread must not be touched either.
	pausedWf := "wf-tr-paused"
	pausedThread := "th-tr-paused"
	insertTestWorkflowWithParent(t, repo, pausedWf, chatID, nil, Paused())
	insertTestThreadForWorkflow(t, repo, pausedThread, chatID, pausedWf, ThreadStatusRunning)

	// A terminal workflow whose thread already reflects it (the live path
	// worked correctly). Must not be counted or rewritten.
	settledWf := "wf-tr-settled"
	settledThread := "th-tr-settled"
	insertTestWorkflowWithParent(t, repo, settledWf, chatID, nil, Completed())
	insertTestThreadForWorkflow(t, repo, settledThread, chatID, settledWf, ThreadStatusCompleted)

	reaped, err := repo.ReapOrphanedThreads(ctx)
	if err != nil {
		t.Fatalf("ReapOrphanedThreads: %v", err)
	}
	if reaped != 3 {
		t.Errorf("reaped %d rows, want 3 (completed, cancelled, failed)", reaped)
	}

	assertThreadStatus := func(id string, want int32, why string) {
		t.Helper()
		th, err := repo.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		if th.Status != want {
			t.Errorf("%s: status %d, want %d — %s", id, th.Status, want, why)
		}
	}

	assertThreadStatus(completedThread, ThreadStatusCompleted, "the thread must inherit its workflow's actual terminal status")
	assertThreadStatus(cancelledThread, ThreadStatusCancelled, "a thread under a cancelled workflow was cancelled, not completed")
	assertThreadStatus(failedThread, ThreadStatusFailed, "a paused thread under a failed workflow must also be reaped")

	for _, id := range []string{completedThread, cancelledThread, failedThread} {
		th, err := repo.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		if th.CompletedAt == nil {
			t.Errorf("%s: expected completed_at to be set by the reap", id)
		}
	}

	assertThreadStatus(liveThread, ThreadStatusRunning, "a live workflow's thread is live work — reaping it would falsely close it")
	assertThreadStatus(pausedThread, ThreadStatusRunning, "a paused workflow has not ended, so its thread has not either")

	// The settled thread must be left EXACTLY as it was — including
	// completed_at staying nil, since insertTestThreadForWorkflow never set
	// it. A reap that touched this row would prove it is not scoping to
	// status IN (2, 6) as intended.
	settled, err := repo.GetThread(ctx, settledThread)
	if err != nil {
		t.Fatalf("GetThread(settled): %v", err)
	}
	if settled.Status != ThreadStatusCompleted {
		t.Errorf("settled thread: status %d, want completed (3) untouched", settled.Status)
	}
	if settled.CompletedAt != nil {
		t.Errorf("settled thread: completed_at must stay nil — the reap must not have touched an already-terminal row")
	}

	// Idempotent: a second pass finds nothing, so the reconciler does not
	// report the same anomaly on every poll.
	again, err := repo.ReapOrphanedThreads(ctx)
	if err != nil {
		t.Fatalf("ReapOrphanedThreads (second pass): %v", err)
	}
	if again != 0 {
		t.Errorf("second pass reaped %d rows, want 0 — the repair must converge", again)
	}
}
