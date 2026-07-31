// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
)

// TestReapOrphanedWorkflowDescendants is the invariant
// CascadeTerminalStatusToDescendants asserts, read from the other direction: a
// workflow whose parent is terminal is not running.
//
// The regression it pins: `reliant workflow cancel` hard-terminates the
// Temporal execution, so the workflow's completion handler — the only thing
// that drives the cascade on a cancel — never runs.
// The root row was repaired by hand; every spawn and thread row underneath it
// was left at running (2) / paused (6) forever, because nothing revisits a row
// with a parent_id and `workflow ps` filters on status alone. Forty such rows
// were still being reported as live runs an hour after the cancel.
func TestReapOrphanedWorkflowDescendants(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-reap"
	createActivityTestChat(t, repo, chatID)

	// Cancelled root with a whole live subtree under it — the shape a user
	// cancel leaves behind.
	cancelledRoot := "wf-cancelled-root"
	cancelledChild := "wf-cancelled-child"
	cancelledGrandchild := "wf-cancelled-grandchild"
	insertTestWorkflowWithParent(t, repo, cancelledRoot, chatID, nil, WorkflowStatusCancelled)
	insertTestWorkflowWithParent(t, repo, cancelledChild, chatID, &cancelledRoot, WorkflowStatusRunning)
	insertTestWorkflowWithParent(t, repo, cancelledGrandchild, chatID, &cancelledChild, WorkflowStatusPaused)

	// Failed root — the reconciler's own terminate paths leave the identical
	// shape, so the repair must not be cancel-specific.
	failedRoot := "wf-failed-root"
	failedChild := "wf-failed-child"
	insertTestWorkflowWithParent(t, repo, failedRoot, chatID, nil, WorkflowStatusFailed)
	insertTestWorkflowWithParent(t, repo, failedChild, chatID, &failedRoot, WorkflowStatusRunning)

	// A LIVE root and its live child. Reaping these would kill running work,
	// which is a far worse failure than the one being fixed.
	liveRoot := "wf-live-root"
	liveChild := "wf-live-child"
	insertTestWorkflowWithParent(t, repo, liveRoot, chatID, nil, WorkflowStatusRunning)
	insertTestWorkflowWithParent(t, repo, liveChild, chatID, &liveRoot, WorkflowStatusRunning)

	// A paused root and its paused child: a pause is resumable, not terminal.
	pausedRoot := "wf-paused-root"
	pausedChild := "wf-paused-child"
	insertTestWorkflowWithParent(t, repo, pausedRoot, chatID, nil, WorkflowStatusPaused)
	insertTestWorkflowWithParent(t, repo, pausedChild, chatID, &pausedRoot, WorkflowStatusRunning)

	// An already-completed descendant of a terminal root must not be counted
	// or rewritten — the count is what the reconciler reports as anomalies.
	settledChild := "wf-settled-child"
	insertTestWorkflowWithParent(t, repo, settledChild, chatID, &cancelledRoot, WorkflowStatusCompleted)

	reaped, err := repo.ReapOrphanedWorkflowDescendants(ctx)
	if err != nil {
		t.Fatalf("ReapOrphanedWorkflowDescendants: %v", err)
	}
	if reaped != 3 {
		t.Errorf("reaped %d rows, want 3 (two under the cancelled root, one under the failed root)", reaped)
	}

	assertStatus := func(id string, want WorkflowStatus, why string) {
		t.Helper()
		wf, err := repo.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatalf("GetWorkflow(%s): %v", id, err)
		}
		if wf.Status != want {
			t.Errorf("%s: status %d, want %d — %s", id, wf.Status, want, why)
		}
	}

	// Reaped rows take the same status the direct cascade writes, so a row
	// repaired here is indistinguishable from one cascaded on the write path —
	// including the distinction between a run that finished and one that was
	// terminated. A reap that wrote COMPLETED here would re-introduce, one poll
	// later, exactly the laundering the cascade was fixed to stop.
	assertStatus(cancelledChild, WorkflowStatusCancelled, "a child of a cancelled root was cancelled, not completed")
	assertStatus(cancelledGrandchild, WorkflowStatusCancelled, "the reap is recursive: a grandchild is stranded exactly as hard as a child, and inherits the same terminal status")
	assertStatus(failedChild, WorkflowStatusFailed, "the leak is not cancel-specific — every terminate path has it, and each keeps its own status")

	// Terminal parents themselves are never rewritten: this repairs
	// descendants only, and a cancelled run must stay distinguishable from a
	// completed one (it is what routes the next message to a fresh start).
	assertStatus(cancelledRoot, WorkflowStatusCancelled, "the reap must not rewrite the parent it read")
	assertStatus(failedRoot, WorkflowStatusFailed, "the reap must not rewrite the parent it read")

	assertStatus(liveRoot, WorkflowStatusRunning, "a root is never reaped by this repair")
	assertStatus(liveChild, WorkflowStatusRunning, "a live parent's child is live work — reaping it would kill a running run")
	assertStatus(pausedRoot, WorkflowStatusPaused, "paused is resumable, not terminal")
	assertStatus(pausedChild, WorkflowStatusRunning, "a paused parent has not ended, so its child has not either")

	// Idempotent: a second pass over a repaired tree finds nothing, so the
	// reconciler does not report the same anomaly on every 30s poll.
	again, err := repo.ReapOrphanedWorkflowDescendants(ctx)
	if err != nil {
		t.Fatalf("ReapOrphanedWorkflowDescendants (second pass): %v", err)
	}
	if again != 0 {
		t.Errorf("second pass reaped %d rows, want 0 — the repair must converge", again)
	}
}
