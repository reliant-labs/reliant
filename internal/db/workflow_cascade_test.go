// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

func insertTestWorkflowWithParent(t *testing.T, repo *Repo, id, chatID string, parentID *string, status WorkflowStatus) {
	t.Helper()
	wf := &Workflow{
		ID:           id,
		ParentID:     parentID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       id,
		Status:       status,
		CreatedAt:    time.Now(),
	}
	if err := repo.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatalf("insertTestWorkflowWithParent: %v", err)
	}
}

func TestCascadeTerminalStatusToDescendants_CascadesRecursively(t *testing.T) {
	// A spawn's own spawns (grandchildren and deeper) must complete with it.
	// A one-level cascade leaves them running/paused forever: the chat stays
	// permanently "active" in chats_with_activity, and stale paused rows stay
	// permanently exempt from the progress watchdog.
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-cascade"
	createActivityTestChat(t, repo, chatID)

	rootID := "wf-root"
	childID := "wf-child"
	grandchildID := "wf-grandchild"
	greatGrandchildID := "wf-great-grandchild"
	siblingChatWorkflowID := "wf-unrelated"

	insertTestWorkflowWithParent(t, repo, rootID, chatID, nil, Active())
	insertTestWorkflowWithParent(t, repo, childID, chatID, &rootID, Active())
	insertTestWorkflowWithParent(t, repo, grandchildID, chatID, &childID, Paused())
	insertTestWorkflowWithParent(t, repo, greatGrandchildID, chatID, &grandchildID, Active())
	// Unrelated root in the same chat must be untouched.
	insertTestWorkflowWithParent(t, repo, siblingChatWorkflowID, chatID, nil, Active())

	if err := repo.CascadeTerminalStatusToDescendants(context.Background(), rootID, StopReasonCompleted); err != nil {
		t.Fatalf("CascadeTerminalStatusToDescendants: %v", err)
	}

	for _, id := range []string{childID, grandchildID, greatGrandchildID} {
		wf, err := repo.GetWorkflow(context.Background(), id)
		if err != nil {
			t.Fatalf("GetWorkflow(%s): %v", id, err)
		}
		if wf.Status != Completed() {
			t.Errorf("%s: expected completed (3), got %d", id, wf.Status)
		}
		if wf.CompletedAt == nil {
			t.Errorf("%s: expected completed_at to be set", id)
		}
	}

	// The root itself and the unrelated sibling root are not the cascade's
	// responsibility.
	for _, id := range []string{rootID, siblingChatWorkflowID} {
		wf, err := repo.GetWorkflow(context.Background(), id)
		if err != nil {
			t.Fatalf("GetWorkflow(%s): %v", id, err)
		}
		if wf.Status != Active() {
			t.Errorf("%s: expected running (2) untouched, got %d", id, wf.Status)
		}
	}
}

// TestCascadeTerminalStatusToDescendants_TerminationIsNotCompletion pins the
// measured defect: `reliant workflow cancel` wrote COMPLETED to all 23
// descendants it had just terminated mid-flight. Those units never finished, so
// every later count of completed work over-counted by the whole subtree, and a
// cancelled run was indistinguishable from a successful one.
//
// CHAT_WORKFLOW_STATUS already carries the distinction (3=completed,
// 5=cancelled, 4=failed) — the cascade just has to write what happened.
func TestCascadeTerminalStatusToDescendants_TerminationIsNotCompletion(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-cancel-cascade"
	createActivityTestChat(t, repo, chatID)

	for _, tc := range []struct {
		name   string
		reason WorkflowStopReason
	}{
		{"cancelled", StopReasonCancelled},
		{"failed", StopReasonFailed},
		{"completed", StopReasonCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootID := "wf-" + tc.name + "-root"
			childID := "wf-" + tc.name + "-child"
			grandchildID := "wf-" + tc.name + "-grandchild"
			insertTestWorkflowWithParent(t, repo, rootID, chatID, nil, Stopped(tc.reason))
			insertTestWorkflowWithParent(t, repo, childID, chatID, &rootID, Active())
			insertTestWorkflowWithParent(t, repo, grandchildID, chatID, &childID, Paused())

			if err := repo.CascadeTerminalStatusToDescendants(ctx, rootID, tc.reason); err != nil {
				t.Fatalf("CascadeTerminalStatusToDescendants: %v", err)
			}

			for _, id := range []string{childID, grandchildID} {
				wf, err := repo.GetWorkflow(ctx, id)
				if err != nil {
					t.Fatalf("GetWorkflow(%s): %v", id, err)
				}
				if wf.Status != Stopped(tc.reason) {
					t.Errorf("%s: status %s, want %s — a descendant of a %s run must record what actually happened to it",
						id, wf.Status.Label(), Stopped(tc.reason).Label(), tc.name)
				}
				if wf.CompletedAt == nil {
					t.Errorf("%s: expected completed_at to be set", id)
				}
			}
		})
	}
}
