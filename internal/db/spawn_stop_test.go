// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestCancelStoppedSpawn pins what a stopped spawn's bookkeeping commits: the
// spawn's row, its descendants AND every thread of its subtree end together.
// A bare status CAS left a cancelled workflow over a running thread until the
// spawn's loop unwound, and a reconciler pass in that window reaped the
// thread and paged on it as a missed cascade (prod, 2026-10-30..11-04).
func TestCancelStoppedSpawn(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-spawn-stop"
	createActivityTestChat(t, repo, chatID)

	root := "wf-ss-root"
	insertTestWorkflowWithParent(t, repo, root, chatID, nil, Active())

	// An active spawn with a spawn of its own, each with a running thread.
	spawn, grandchild := "wf-ss-spawn", "wf-ss-grandchild"
	insertTestWorkflowWithParent(t, repo, spawn, chatID, &root, Active())
	insertTestThreadForWorkflow(t, repo, "th-ss-spawn", chatID, spawn, ThreadStatusRunning)
	insertTestWorkflowWithParent(t, repo, grandchild, chatID, &spawn, Active())
	insertTestThreadForWorkflow(t, repo, "th-ss-grandchild", chatID, grandchild, ThreadStatusRunning)

	// A paused spawn: an explicit stop overrides a pause.
	paused := "wf-ss-paused"
	insertTestWorkflowWithParent(t, repo, paused, chatID, &root, Paused())
	insertTestThreadForWorkflow(t, repo, "th-ss-paused", chatID, paused, ThreadStatusRunning)

	// A spawn that already completed on its own is never clobbered.
	done := "wf-ss-done"
	insertTestWorkflowWithParent(t, repo, done, chatID, &root, Completed())
	insertTestThreadForWorkflow(t, repo, "th-ss-done", chatID, done, ThreadStatusCompleted)

	for _, tc := range []struct {
		workflowID string
		wantSwap   bool
	}{{spawn, true}, {paused, true}, {done, false}} {
		swapped, err := repo.CancelStoppedSpawn(ctx, tc.workflowID)
		if err != nil {
			t.Fatalf("CancelStoppedSpawn(%s): %v", tc.workflowID, err)
		}
		if swapped != tc.wantSwap {
			t.Errorf("CancelStoppedSpawn(%s) = %v, want %v", tc.workflowID, swapped, tc.wantSwap)
		}
	}

	for id, want := range map[string]WorkflowStatus{
		spawn: Cancelled(), grandchild: Cancelled(), paused: Cancelled(), done: Completed(), root: Active(),
	} {
		wf, err := repo.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatalf("GetWorkflow(%s): %v", id, err)
		}
		if wf.Status != want {
			t.Errorf("%s: status %s, want %s", id, wf.Status.Label(), want.Label())
		}
	}
	for id, want := range map[string]ThreadStatus{
		"th-ss-spawn": ThreadStatusCancelled, "th-ss-grandchild": ThreadStatusCancelled,
		"th-ss-paused": ThreadStatusCancelled, "th-ss-done": ThreadStatusCompleted,
	} {
		th, err := repo.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		if th.Status != want {
			t.Errorf("%s: thread status %d, want %d", id, th.Status, want)
		}
	}

	// The invariant the reconciler checks holds the moment the stop commits:
	// nothing is left for it to reap.
	reaped, err := repo.ReapOrphanedThreads(ctx)
	if err != nil {
		t.Fatalf("ReapOrphanedThreads: %v", err)
	}
	if len(reaped) != 0 {
		t.Errorf("reaped %+v after a spawn stop; want none — the stop must end its threads in the same commit", reaped)
	}
}

// A stopped spawn's unread mail is resolved with the stop, not left queued
// under its cancelled threads for the reconciler's orphaned-mailbox sweep to
// find and report as a broken loop invariant (reliant #2394: three spawn_send
// rows to a sub-agent parked on a question, which its parent then stopped).
// Mail for a thread outside the stopped subtree is untouched.
func TestCancelStoppedSpawnResolvesTheSubtreesQueuedMail(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-spawn-stop-mail"
	createActivityTestChat(t, repo, chatID)

	root := "wf-ssm-root"
	insertTestWorkflowWithParent(t, repo, root, chatID, nil, Active())
	insertTestThreadForWorkflow(t, repo, "th-ssm-root", chatID, root, ThreadStatusRunning)
	spawn, grandchild := "wf-ssm-spawn", "wf-ssm-grandchild"
	insertTestWorkflowWithParent(t, repo, spawn, chatID, &root, Active())
	insertTestThreadForWorkflow(t, repo, "th-ssm-spawn", chatID, spawn, ThreadStatusRunning)
	insertTestWorkflowWithParent(t, repo, grandchild, chatID, &spawn, Active())
	insertTestThreadForWorkflow(t, repo, "th-ssm-grandchild", chatID, grandchild, ThreadStatusRunning)

	queue := func(id, to string) {
		t.Helper()
		if err := repo.EnqueueAgentMessage(ctx, &AgentMessage{
			ID: id, ChatID: chatID, FromThreadID: "th-ssm-root", ToThreadID: to,
			Kind: core.AgentMessageKindMessage, Body: "status?", Status: core.AgentMessageStatusQueued, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("EnqueueAgentMessage(%s): %v", id, err)
		}
	}
	queue("am-ssm-spawn", "th-ssm-spawn")
	queue("am-ssm-grandchild", "th-ssm-grandchild")
	queue("am-ssm-root", "th-ssm-root")

	if _, err := repo.CancelStoppedSpawn(ctx, spawn); err != nil {
		t.Fatalf("CancelStoppedSpawn: %v", err)
	}

	for id, want := range map[string]core.AgentMessageStatus{
		"am-ssm-spawn":      core.AgentMessageStatusUndelivered,
		"am-ssm-grandchild": core.AgentMessageStatusUndelivered,
		"am-ssm-root":       core.AgentMessageStatusQueued,
	} {
		msg, err := repo.GetAgentMessage(ctx, id)
		if err != nil {
			t.Fatalf("GetAgentMessage(%s): %v", id, err)
		}
		if msg.Status != want {
			t.Errorf("%s: status %d, want %d", id, msg.Status, want)
		}
	}

	orphaned, err := repo.ListThreadsWithOrphanedAgentMessages(ctx)
	if err != nil {
		t.Fatalf("ListThreadsWithOrphanedAgentMessages: %v", err)
	}
	for _, id := range orphaned {
		if id == "th-ssm-spawn" || id == "th-ssm-grandchild" {
			t.Errorf("the sweep still finds %s's mail orphaned after the stop", id)
		}
	}
}
