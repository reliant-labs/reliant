// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// insertTestWorkflowAt creates a workflows row with an explicit created_at and
// completed_at, which is what these tests are entirely about: the revive
// predicate is a time window around the reset point, so the timestamps ARE the
// fixture.
func insertTestWorkflowAt(
	t *testing.T, repo *Repo, id, chatID string, parentID *string,
	status WorkflowStatus, createdAt time.Time, completedAt *time.Time,
) {
	t.Helper()
	if err := repo.CreateWorkflow(context.Background(), &Workflow{
		ID:           id,
		ParentID:     parentID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       id,
		Status:       status,
		CreatedAt:    createdAt,
		CompletedAt:  completedAt,
	}); err != nil {
		t.Fatalf("insertTestWorkflowAt(%s): %v", id, err)
	}
}

// insertTerminalThread creates a threads row owned by workflowID and stamps it
// terminal, mirroring what the reaps leave behind.
func insertTerminalThread(t *testing.T, repo *Repo, id, chatID, workflowID string, status int32, completedAt time.Time) {
	t.Helper()
	insertTestThreadForWorkflow(t, repo, id, chatID, workflowID, ThreadStatusRunning)
	if _, err := repo.UpdateThreadStatus(context.Background(), id, status, &completedAt); err != nil {
		t.Fatalf("insertTerminalThread(%s): %v", id, err)
	}
}

// TestReviveSubtreeLiveAt is the 2026-09-29 incident, pinned. The reconciler
// terminated a healthy root and the reaps stamped its six in-flight sub-agents
// failed. The user resumed via reset-and-replay, which REBUILT those same
// sub-agents from the same rows — but only the root was marked Active, so the
// UI showed live agents as failed and a reconciler sweep wrote six false
// "the parent had already exited" reports.
//
// The reset point is the only thing that separates the two populations, and
// this test is the table of both: a descendant that was live at T comes back,
// one that had already finished by T stays finished.
func TestReviveSubtreeLiveAt(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-subtree-revive"
	createActivityTestChat(t, repo, chatID)

	resetPoint := time.Now().UTC().Add(-10 * time.Minute)
	before := resetPoint.Add(-5 * time.Minute)
	after := resetPoint.Add(2 * time.Minute)
	killedAt := resetPoint.Add(5 * time.Minute) // when the terminate reaped the subtree

	rootID := "wf-sr-root"
	insertTestWorkflowAt(t, repo, rootID, chatID, nil, Failed(), before, &killedAt)
	insertTerminalThread(t, repo, rootID, chatID, rootID, ThreadStatusFailed, killedAt)

	// (a) Live at T, killed by the terminate: the incident's six sub-agents.
	liveAtReset := "wf-sr-live"
	insertTestWorkflowAt(t, repo, liveAtReset, chatID, &rootID, Failed(), before, &killedAt)
	insertTerminalThread(t, repo, "th-sr-live", chatID, liveAtReset, ThreadStatusFailed, killedAt)

	// (b) Genuinely finished before T: its completion is in the replayed
	// history, so the new run never re-runs it and it must stay completed.
	finishedBefore := "wf-sr-finished"
	finishedAt := resetPoint.Add(-1 * time.Minute)
	insertTestWorkflowAt(t, repo, finishedBefore, chatID, &rootID, Completed(), before, &finishedAt)
	insertTerminalThread(t, repo, "th-sr-finished", chatID, finishedBefore, ThreadStatusCompleted, finishedAt)

	// (c) Created AFTER T: the new run re-executes its own "started"
	// activity and self-revives. Reviving it here would be a guess about a
	// run that has not happened yet.
	createdAfter := "wf-sr-after"
	insertTestWorkflowAt(t, repo, createdAfter, chatID, &rootID, Failed(), after, &killedAt)
	insertTerminalThread(t, repo, "th-sr-after", chatID, createdAfter, ThreadStatusFailed, killedAt)

	// (d) Paused: ResumeWorkflowsByChat's business. Un-pausing here would
	// resume a run the user deliberately parked.
	pausedChild := "wf-sr-paused"
	insertTestWorkflowAt(t, repo, pausedChild, chatID, &rootID, Paused(), before, nil)

	// (e) Grandchild live at T — the cascade recursed, so the revive must too.
	grandchild := "wf-sr-grandchild"
	insertTestWorkflowAt(t, repo, grandchild, chatID, &liveAtReset, Failed(), before, &killedAt)
	insertTerminalThread(t, repo, "th-sr-grandchild", chatID, grandchild, ThreadStatusFailed, killedAt)

	// Another root's subtree in the same chat: a resume is about ONE run.
	otherRoot := "wf-sr-other-root"
	insertTestWorkflowAt(t, repo, otherRoot, chatID, nil, Failed(), before, &killedAt)
	otherChild := "wf-sr-other-child"
	insertTestWorkflowAt(t, repo, otherChild, chatID, &otherRoot, Failed(), before, &killedAt)
	insertTerminalThread(t, repo, "th-sr-other-child", chatID, otherChild, ThreadStatusFailed, killedAt)

	workflowsRevived, threadsRevived, err := repo.ReviveSubtreeLiveAt(ctx, rootID, resetPoint)
	if err != nil {
		t.Fatalf("ReviveSubtreeLiveAt: %v", err)
	}

	// (a) + (e): two workflow rows. Threads: those two, plus the root's own.
	if workflowsRevived != 2 {
		t.Errorf("workflowsRevived = %d, want 2 (the live child and the live grandchild)", workflowsRevived)
	}
	if threadsRevived != 3 {
		t.Errorf("threadsRevived = %d, want 3 (child, grandchild, and the root's own thread)", threadsRevived)
	}

	for _, id := range []string{liveAtReset, grandchild} {
		wf, err := repo.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatalf("GetWorkflow(%s): %v", id, err)
		}
		if wf.Status.State != WorkflowStateActive {
			t.Errorf("%s: state = %d, want %d (active)", id, wf.Status.State, WorkflowStateActive)
		}
		if wf.Status.StopReason != StopReasonUnspecified {
			t.Errorf("%s: stop_reason = %d, want unspecified on a revived row", id, wf.Status.StopReason)
		}
		// A revived row that keeps the kill's completed_at reads as
		// "finished at 21:52" while it is executing.
		if wf.CompletedAt != nil {
			t.Errorf("%s: completed_at = %v, want nil on a revived row", id, wf.CompletedAt)
		}
	}

	for _, id := range []string{"th-sr-live", "th-sr-grandchild", rootID} {
		th, err := repo.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		if th.Status != ThreadStatusRunning {
			t.Errorf("thread %s: status = %d, want %d (running)", id, th.Status, ThreadStatusRunning)
		}
		if th.CompletedAt != nil {
			t.Errorf("thread %s: completed_at = %v, want nil on a revived thread", id, th.CompletedAt)
		}
	}

	// Everything the window excludes stays exactly as it was.
	for _, tc := range []struct {
		name       string
		workflowID string
		wantState  WorkflowState
		wantReason WorkflowStopReason
		threadID   string
		wantThread int32
	}{
		{"finished before the reset point", finishedBefore, WorkflowStateStopped, StopReasonCompleted, "th-sr-finished", ThreadStatusCompleted},
		{"created after the reset point", createdAfter, WorkflowStateStopped, StopReasonFailed, "th-sr-after", ThreadStatusFailed},
		{"paused", pausedChild, WorkflowStateStopped, StopReasonPaused, "", 0},
		{"under another root", otherChild, WorkflowStateStopped, StopReasonFailed, "th-sr-other-child", ThreadStatusFailed},
	} {
		wf, err := repo.GetWorkflow(ctx, tc.workflowID)
		if err != nil {
			t.Fatalf("GetWorkflow(%s): %v", tc.workflowID, err)
		}
		if wf.Status.State != tc.wantState || wf.Status.StopReason != tc.wantReason {
			t.Errorf("%s (%s): status = %d/%d, want %d/%d untouched",
				tc.name, tc.workflowID, wf.Status.State, wf.Status.StopReason, tc.wantState, tc.wantReason)
		}
		if tc.threadID == "" {
			continue
		}
		th, err := repo.GetThread(ctx, tc.threadID)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", tc.threadID, err)
		}
		if th.Status != tc.wantThread {
			t.Errorf("%s (thread %s): status = %d, want %d untouched", tc.name, tc.threadID, th.Status, tc.wantThread)
		}
	}
}

// TestReviveSubtreeLiveAt_RootThreadOnly is the minimum a resume must do: the
// root's own thread (whose id is the workflow id) was stamped terminal by the
// same kill, and the root re-parks by replay rather than re-running its
// "started" activity — so nothing else brings it back.
func TestReviveSubtreeLiveAt_RootThreadOnly(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-subtree-revive-root"
	createActivityTestChat(t, repo, chatID)

	resetPoint := time.Now().UTC().Add(-10 * time.Minute)
	killedAt := resetPoint.Add(5 * time.Minute)

	rootID := "wf-srr-root"
	insertTestWorkflowAt(t, repo, rootID, chatID, nil, Failed(), resetPoint.Add(-time.Hour), &killedAt)
	insertTerminalThread(t, repo, rootID, chatID, rootID, ThreadStatusFailed, killedAt)

	workflowsRevived, threadsRevived, err := repo.ReviveSubtreeLiveAt(ctx, rootID, resetPoint)
	if err != nil {
		t.Fatalf("ReviveSubtreeLiveAt: %v", err)
	}
	// The root's WORKFLOW row is the caller's own job (UpdateWorkflowStatus
	// marks it Active right after) — this revives the subtree and the root's
	// thread, not the root row itself.
	if workflowsRevived != 0 {
		t.Errorf("workflowsRevived = %d, want 0 (a childless root has no descendants)", workflowsRevived)
	}
	if threadsRevived != 1 {
		t.Errorf("threadsRevived = %d, want 1 (the root's own thread)", threadsRevived)
	}

	th, err := repo.GetThread(ctx, rootID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if th.Status != ThreadStatusRunning {
		t.Errorf("root thread status = %d, want %d (running)", th.Status, ThreadStatusRunning)
	}
	if th.CompletedAt != nil {
		t.Errorf("root thread completed_at = %v, want nil", th.CompletedAt)
	}
}

// TestReviveSubtreeLiveAt_LeavesLiveRowsAlone guards the write from the
// direction that cannot be undone. An active descendant is already correct,
// and re-writing its bookkeeping would clear a live row's state for nothing.
func TestReviveSubtreeLiveAt_LeavesLiveRowsAlone(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-subtree-revive-live"
	createActivityTestChat(t, repo, chatID)

	resetPoint := time.Now().UTC()
	rootID := "wf-srl-root"
	insertTestWorkflowAt(t, repo, rootID, chatID, nil, Active(), resetPoint.Add(-time.Hour), nil)

	activeChild := "wf-srl-active"
	insertTestWorkflowAt(t, repo, activeChild, chatID, &rootID, Active(), resetPoint.Add(-time.Minute), nil)
	insertTestThreadForWorkflow(t, repo, "th-srl-active", chatID, activeChild, ThreadStatusRunning)

	workflowsRevived, threadsRevived, err := repo.ReviveSubtreeLiveAt(ctx, rootID, resetPoint)
	if err != nil {
		t.Fatalf("ReviveSubtreeLiveAt: %v", err)
	}
	if workflowsRevived != 0 || threadsRevived != 0 {
		t.Errorf("revived %d workflows / %d threads on an already-live subtree, want 0/0",
			workflowsRevived, threadsRevived)
	}
}

// TestReviveSubtreeLiveAt_UnknownRoot is the no-op contract for the best-effort
// caller: a resume must not fail because the root has no rows to repair.
func TestReviveSubtreeLiveAt_UnknownRoot(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	workflowsRevived, threadsRevived, err := repo.ReviveSubtreeLiveAt(
		context.Background(), "wf-does-not-exist", time.Now().UTC())
	if err != nil {
		t.Fatalf("ReviveSubtreeLiveAt on unknown root: %v", err)
	}
	if workflowsRevived != 0 || threadsRevived != 0 {
		t.Errorf("revived %d/%d for an unknown root, want 0/0", workflowsRevived, threadsRevived)
	}
}
