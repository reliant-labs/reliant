// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers for the activity integration tests
// ---------------------------------------------------------------------------

func createActivityTestChat(t *testing.T, repo *Repo, chatID string) {
	t.Helper()
	ctx := context.Background()
	chat := &Chat{
		ID:         chatID,
		Title:      "test chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		State:      2, // IDLE
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	if err := repo.CreateChat(ctx, chat); err != nil {
		t.Fatalf("createActivityTestChat: %v", err)
	}
	// Every real chat gets a root thread whose id equals the chat id, and
	// messages/context windows reference it. Fixtures that skipped it were
	// relying on the absence of foreign keys.
	createTestRootThread(t, repo, chatID)
}

// createTestRootThread creates the root thread row for a chat, matching the
// production invariant that a chat's root thread id equals the chat id.
func createTestRootThread(t *testing.T, repo *Repo, chatID string) {
	t.Helper()
	if _, err := repo.CreateThread(context.Background(), &Thread{
		ID:        chatID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("createTestRootThread: %v", err)
	}
}

func insertTestWorkflow(t *testing.T, repo *Repo, id, chatID, workflowName string, status WorkflowStatus) {
	t.Helper()
	ctx := context.Background()
	wf := &Workflow{
		ID:           id,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       id,
		Status:       status,
		CreatedAt:    time.Now(),
	}
	if err := repo.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("insertTestWorkflow: %v", err)
	}
}

func insertTestApproval(t *testing.T, repo *Repo, id, chatID string, status int32) {
	t.Helper()
	ctx := context.Background()
	approval := &Approval{
		ID:           id,
		ChatID:       chatID,
		ApprovalType: 1, // tool
		EntityID:     id,
		Status:       status,
		Title:        "test approval",
		CreatedAt:    time.Now(),
	}
	if err := repo.CreateApproval(ctx, approval); err != nil {
		t.Fatalf("insertTestApproval: %v", err)
	}
}

// activityUpdatesForChat filters user_updates to only chat_activity_changed
// events for the given chat.
func activityUpdatesForChat(t *testing.T, repo *Repo, chatID string) []UserUpdate {
	t.Helper()
	updates, err := repo.GetUserUpdatesSince(context.Background(), "test-user", 0, 1000)
	if err != nil {
		t.Fatalf("activityUpdatesForChat: %v", err)
	}
	var result []UserUpdate
	for _, u := range updates {
		if u.UpdateType == UserUpdateChatActivityChanged {
			var data map[string]interface{}
			if err := json.Unmarshal(u.Data, &data); err == nil {
				if data["chat_id"] == chatID {
					result = append(result, u)
				}
			}
		}
	}
	return result
}

// activityValueFromUpdate extracts the "activity" int from a user_update's JSON data.
func activityValueFromUpdate(t *testing.T, u UserUpdate) int {
	t.Helper()
	var data map[string]interface{}
	if err := json.Unmarshal(u.Data, &data); err != nil {
		t.Fatalf("activityValueFromUpdate: unmarshal: %v", err)
	}
	v, ok := data["activity"]
	if !ok {
		t.Fatalf("activityValueFromUpdate: missing 'activity' key")
	}
	return int(v.(float64))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestGetChatActivity_Idle(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-idle"
	createActivityTestChat(t, repo, chatID)

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 0 {
		t.Fatalf("expected activity=0 (IDLE), got %d", activity)
	}
}

func TestGetChatActivity_Running(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-running"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, "wf-1", chatID, "builtin://agent", Active())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING), got %d", activity)
	}
}

func TestGetChatActivity_RunningWithThreadWorkflow(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-thread"
	createActivityTestChat(t, repo, chatID)

	// Only a thread-prefixed workflow is running. Previously excluded
	// from the activity view, causing the chat to appear IDLE.
	insertTestWorkflow(t, repo, "wf-thread", chatID, "thread:abc123", Active())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING) for thread workflow, got %d", activity)
	}
}

func TestGetChatActivity_AwaitingInput_PendingApproval(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-approval"
	createActivityTestChat(t, repo, chatID)
	insertTestApproval(t, repo, "approval-1", chatID, 1) // status=1 → PENDING

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 2 {
		t.Fatalf("expected activity=2 (AWAITING_INPUT), got %d", activity)
	}
}

func TestGetChatActivity_ApprovalTakesPriorityOverRunning(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-priority"
	createActivityTestChat(t, repo, chatID)

	// Both a running workflow AND a pending approval exist.
	insertTestWorkflow(t, repo, "wf-run", chatID, "builtin://agent", Active())
	insertTestApproval(t, repo, "approval-pri", chatID, 1) // PENDING

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 2 {
		t.Fatalf("expected activity=2 (AWAITING_INPUT takes priority over RUNNING), got %d", activity)
	}
}

func TestGetChatActivity_Paused(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-paused"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, "wf-paused", chatID, "builtin://agent", Paused())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 4 {
		t.Fatalf("expected activity=4 (PAUSED), got %d", activity)
	}
}

func TestGetChatActivity_RunningTakesPriorityOverPaused(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-running-and-paused"
	createActivityTestChat(t, repo, chatID)

	// One workflow running, another paused (e.g. a paused thread alongside a
	// live one). The chat must still read RUNNING.
	insertTestWorkflow(t, repo, "wf-run", chatID, "builtin://agent", Active())
	insertTestWorkflow(t, repo, "wf-paused", chatID, "thread:abc123", Paused())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING takes priority over PAUSED), got %d", activity)
	}
}

func TestEmitChatActivityIfChanged_AlwaysEmits(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-always-emit"
	createActivityTestChat(t, repo, chatID)

	// Call emitChatActivityIfChanged twice with the same underlying state (IDLE).
	// Both calls should produce a user_update row — no dedup.
	if err := repo.emitChatActivityIfChanged(ctx, chatID); err != nil {
		t.Fatalf("first emit: %v", err)
	}
	if err := repo.emitChatActivityIfChanged(ctx, chatID); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	events := activityUpdatesForChat(t, repo, chatID)
	if len(events) != 2 {
		t.Fatalf("expected 2 activity events (no dedup), got %d", len(events))
	}
}

func TestEmitChatActivityIfChanged_TransitionRunningToIdle(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-transition"
	createActivityTestChat(t, repo, chatID)

	// Start a workflow → activity should be RUNNING (1).
	insertTestWorkflow(t, repo, "wf-transition", chatID, "builtin://agent", Active())

	activity, err := repo.GetChatActivity(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatActivity after start: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING) after workflow start, got %d", activity)
	}

	// Complete the workflow → activity should transition to IDLE (0).
	if err := repo.UpdateWorkflowStatus(ctx, "wf-transition", Completed()); err != nil {
		t.Fatalf("UpdateWorkflowStatus: %v", err)
	}

	activity, err = repo.GetChatActivity(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatActivity after complete: %v", err)
	}
	if activity != 0 {
		t.Fatalf("expected activity=0 (IDLE) after workflow completion, got %d", activity)
	}

	// Verify the emitted events reflect the transition.
	// CreateWorkflow emits 1 activity event, UpdateWorkflowStatus emits another.
	events := activityUpdatesForChat(t, repo, chatID)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 activity events (running + idle), got %d", len(events))
	}

	// The last event should reflect IDLE (0).
	lastActivity := activityValueFromUpdate(t, events[len(events)-1])
	if lastActivity != 0 {
		t.Fatalf("expected last activity event to be 0 (IDLE), got %d", lastActivity)
	}

	// Verify at least one RUNNING event was emitted.
	foundRunning := false
	for _, ev := range events {
		if activityValueFromUpdate(t, ev) == 1 {
			foundRunning = true
			break
		}
	}
	if !foundRunning {
		t.Fatal("expected at least one activity event with activity=1 (RUNNING)")
	}
}

// ---------------------------------------------------------------------------
// ERROR recency (recovered chats must not stay red)
// ---------------------------------------------------------------------------

// completeWorkflowNow drives a workflow to a terminal status through the
// production path, which is what stamps completed_at (see the CASE in
// queries/workflows.sql). The ERROR branch compares those timestamps, so a
// fixture that only INSERTed rows would not exercise it.
func completeWorkflowNow(t *testing.T, repo *Repo, id string, status WorkflowStatus) {
	t.Helper()
	if err := repo.UpdateWorkflowStatus(context.Background(), id, status); err != nil {
		t.Fatalf("completeWorkflowNow(%s, %d): %v", id, status, err)
	}
}

func TestGetChatActivity_Error_WhenNothingSucceededAfterFailure(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-failed"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 3 {
		t.Fatalf("a chat whose only terminal workflow failed must read ERROR (3), got %d", activity)
	}
}

// The regression this file exists to pin: a chat that failed once and then
// recovered must stop reading ERROR. The old view asked
// `EXISTS (status = 4)` over the chat's whole history, so a single failure
// pinned the chat red forever and the sidebar sorted it up as "needs
// attention" for the rest of its life.
//
// Observed on chat c0ce9449-…: two spawned workflows failed, twenty later
// workflows completed (the last five hours afterwards), and the view still
// returned 3.
func TestGetChatActivity_ErrorClearedAfterRecovery(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-recovered"
	createActivityTestChat(t, repo, chatID)

	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())

	// The user retries and it works. completed_at is stamped by the same
	// production path, so the retry lands strictly after the failure.
	insertTestWorkflow(t, repo, "wf-retry", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-retry", Completed())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 0 {
		t.Fatalf("a chat that recovered after a failure must read IDLE (0), got %d "+
			"(the red dot must clear once work succeeds again)", activity)
	}
}

// A success that happened BEFORE the failure must not clear it — otherwise any
// chat with one early success would be permanently immune to showing an error.
func TestGetChatActivity_ErrorNotClearedByEarlierSuccess(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-success-then-failure"
	createActivityTestChat(t, repo, chatID)

	insertTestWorkflow(t, repo, "wf-ok", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-ok", Completed())

	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 3 {
		t.Fatalf("a failure after a success must still read ERROR (3), got %d", activity)
	}
}

// A live run outranks a past failure: the chat is working again, so it reads
// RUNNING rather than advertising an error the user can do nothing about.
func TestGetChatActivity_RunningTakesPriorityOverPastFailure(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-failed-then-running"
	createActivityTestChat(t, repo, chatID)

	insertTestWorkflow(t, repo, "wf-fail", chatID, "builtin://agent", Active())
	completeWorkflowNow(t, repo, "wf-fail", Failed())
	insertTestWorkflow(t, repo, "wf-live", chatID, "builtin://agent", Active())

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING takes priority over a past failure), got %d", activity)
	}
}

// ---------------------------------------------------------------------------
// Sequence allocation (SERIALIZABLE contention)
// ---------------------------------------------------------------------------

// Sequence numbers are the cursor a reconnecting stream resumes from. The
// scoped counter and ledger insert share a transaction, so committed values
// must be contiguous: another user's traffic cannot create a gap, and a
// rollback rolls the counter increment back with the update.
func TestUserUpdateSequencesStrictlyIncrease(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-seq-mono"
	createActivityTestChat(t, repo, chatID)

	ctx := context.Background()
	var seqs []int64
	for i := 0; i < 8; i++ {
		if err := repo.emitChatActivityChanged(ctx, chatID, i%5); err != nil {
			t.Fatalf("emitChatActivityChanged(%d): %v", i, err)
		}
		updates := activityUpdatesForChat(t, repo, chatID)
		seqs = append(seqs, updates[len(updates)-1].SequenceNumber)
	}

	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("sequence numbers must be contiguous, got %v (index %d did not advance by one)", seqs, i)
		}
	}
}

// The scoped allocator intentionally serializes writers to one user by
// updating that user's counter row. Under this repository's SERIALIZABLE
// isolation, waiters may retry with SQLSTATE 40001 after the row conflict.
// The production transaction wrapper must absorb those retries rather than
// surfacing a failed SendMessage under normal fan-out.
//
// Every update sequence for a user is allocated from ONE counter row, so all
// of that user's concurrent chats serialize on it. This test pins the property
// that matters: under realistic concurrency those writes must all SUCCEED, not
// merely mostly succeed. A dropped allocation is a lost update event in the UI.
//
// Sizing is empirical, not arbitrary. With the old maxRetries=3 budget the
// measured failure rate on one counter row was 0% at 4 writers, 5% at 8, and
// 8.8% at 16 — so 8 writers (the previous value) sat right at the threshold
// and failed only intermittently, which is why this read as a flaky test for
// a long time rather than as the real defect it was.
//
// The count here is chosen so the OLD budget fails every time rather than one
// run in five: an intermittent regression test is barely better than none,
// because the next person to see it green will assume the property holds.
// Verified by reverting maxRetries to 3, where this fails consistently.
func TestConcurrentUserUpdatesDoNotSerializationConflict(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	const writers = 48
	chatIDs := make([]string, writers)
	for i := range chatIDs {
		chatIDs[i] = fmt.Sprintf("chat-seq-conc-%d", i)
		createActivityTestChat(t, repo, chatIDs[i])
	}

	// All chats belong to the SAME user ("test-user"), which is the whole
	// point: user_updates has one counter per user, so every chat in a
	// multi-spawn run contends on it.
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(chatID string) {
			defer wg.Done()
			for n := 0; n < 5; n++ {
				if err := repo.emitChatActivityChanged(context.Background(), chatID, n%5); err != nil {
					errCh <- err
					return
				}
			}
		}(chatIDs[i])
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent user-update writes must not fail: %v", err)
	}
}
