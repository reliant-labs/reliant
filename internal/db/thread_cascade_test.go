// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// insertTestThreadForWorkflow creates a threads row owned by workflowID, at
// the given status, matching the real shape (a thread's workflow_id names
// the workflow whose ThreadStatusActivity calls own it).
func insertTestThreadForWorkflow(t *testing.T, repo *Repo, id, chatID, workflowID string, status int32) {
	t.Helper()
	wfID := workflowID
	if _, err := repo.CreateThread(context.Background(), &Thread{
		ID:         id,
		ChatID:     chatID,
		WorkflowID: &wfID,
		CreatedAt:  time.Now(),
		Status:     status,
	}); err != nil {
		t.Fatalf("insertTestThreadForWorkflow(%s): %v", id, err)
	}
}

// TestCascadeTerminalStatusToThreadSubtree_CascadesRecursively pins the bug
// the incident briefing measured: CascadeTerminalStatusToDescendants moves
// every descendant WORKFLOW to the parent's terminal status, but nothing did
// the equivalent for the THREAD each of those workflows owns. Live DB: 288
// threads stranded at status=2 under an already-terminal workflow (see
// docs/incidents/2026-08-12-spawn-history-cap.md).
func TestCascadeTerminalStatusToThreadSubtree_CascadesRecursively(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-thread-cascade"
	createActivityTestChat(t, repo, chatID)

	rootID := "wf-tc-root"
	childID := "wf-tc-child"
	grandchildID := "wf-tc-grandchild"
	unrelatedID := "wf-tc-unrelated"

	insertTestWorkflowWithParent(t, repo, rootID, chatID, nil, WorkflowStatusRunning)
	insertTestWorkflowWithParent(t, repo, childID, chatID, &rootID, WorkflowStatusRunning)
	insertTestWorkflowWithParent(t, repo, grandchildID, chatID, &childID, WorkflowStatusPaused)
	insertTestWorkflowWithParent(t, repo, unrelatedID, chatID, nil, WorkflowStatusRunning)

	// One thread per workflow, each still running/paused -- the shape a
	// worker death between UpdateWorkflowStatus and ThreadStatusActivity's
	// own "completed" call leaves behind.
	insertTestThreadForWorkflow(t, repo, "th-tc-root", chatID, rootID, ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-tc-child", chatID, childID, ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-tc-grandchild", chatID, grandchildID, ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-tc-unrelated", chatID, unrelatedID, ThreadStatusRunning)

	if err := repo.CascadeTerminalStatusToThreadSubtree(ctx, rootID, WorkflowStatusCompleted); err != nil {
		t.Fatalf("CascadeTerminalStatusToThreadSubtree: %v", err)
	}

	for _, id := range []string{"th-tc-root", "th-tc-child", "th-tc-grandchild"} {
		th, err := repo.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		if th.Status != ThreadStatusCompleted {
			t.Errorf("%s: expected completed (3), got %d", id, th.Status)
		}
		if th.CompletedAt == nil {
			t.Errorf("%s: expected completed_at to be set", id)
		}
	}

	unrelated, err := repo.GetThread(ctx, "th-tc-unrelated")
	if err != nil {
		t.Fatalf("GetThread(unrelated): %v", err)
	}
	if unrelated.Status != ThreadStatusRunning {
		t.Errorf("unrelated thread: expected running (2) untouched, got %d", unrelated.Status)
	}
	if unrelated.CompletedAt != nil {
		t.Errorf("unrelated thread: expected completed_at to stay nil")
	}
}

// TestCascadeTerminalStatusToThreadSubtree_TerminationIsNotCompletion mirrors
// TestCascadeTerminalStatusToDescendants_TerminationIsNotCompletion: a thread
// under a cancelled/failed run must record what actually happened, not be
// laundered into "completed".
func TestCascadeTerminalStatusToThreadSubtree_TerminationIsNotCompletion(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-thread-cascade-term"
	createActivityTestChat(t, repo, chatID)

	for _, tc := range []struct {
		name   string
		status WorkflowStatus
	}{
		{"cancelled", WorkflowStatusCancelled},
		{"failed", WorkflowStatusFailed},
		{"completed", WorkflowStatusCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootID := "wf-tct-" + tc.name
			threadID := "th-tct-" + tc.name
			insertTestWorkflowWithParent(t, repo, rootID, chatID, nil, WorkflowStatusRunning)
			insertTestThreadForWorkflow(t, repo, threadID, chatID, rootID, ThreadStatusRunning)

			if err := repo.CascadeTerminalStatusToThreadSubtree(ctx, rootID, tc.status); err != nil {
				t.Fatalf("CascadeTerminalStatusToThreadSubtree: %v", err)
			}

			th, err := repo.GetThread(ctx, threadID)
			if err != nil {
				t.Fatalf("GetThread(%s): %v", threadID, err)
			}
			wantStatus := int32(tc.status)
			if th.Status != wantStatus {
				t.Errorf("%s: status %d, want %d — a thread under a %s run must record what actually happened", threadID, th.Status, wantStatus, tc.name)
			}
			if th.CompletedAt == nil {
				t.Errorf("%s: expected completed_at to be set", threadID)
			}
		})
	}
}

// TestCascadeTerminalStatusToThreadSubtree_FailClosed pins the FAIL-CLOSED
// discipline: a thread already in a terminal status is left untouched, and
// a thread whose workflow_id does not match the cascaded subtree is never
// rewritten.
func TestCascadeTerminalStatusToThreadSubtree_FailClosed(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-thread-cascade-closed"
	createActivityTestChat(t, repo, chatID)

	rootID := "wf-fc-root"
	insertTestWorkflowWithParent(t, repo, rootID, chatID, nil, WorkflowStatusRunning)

	alreadyDoneID := "th-fc-already-done"
	completedAt := time.Now()
	if _, err := repo.CreateThread(ctx, &Thread{
		ID: alreadyDoneID, ChatID: chatID, WorkflowID: &rootID,
		CreatedAt: time.Now(), Status: ThreadStatusFailed, CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	if err := repo.CascadeTerminalStatusToThreadSubtree(ctx, rootID, WorkflowStatusCompleted); err != nil {
		t.Fatalf("CascadeTerminalStatusToThreadSubtree: %v", err)
	}

	th, err := repo.GetThread(ctx, alreadyDoneID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if th.Status != ThreadStatusFailed {
		t.Errorf("an already-terminal thread must not be overwritten by a later cascade: got status %d, want failed (4)", th.Status)
	}
}
