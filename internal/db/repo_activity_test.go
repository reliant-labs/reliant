// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
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

func insertTestYield(t *testing.T, repo *Repo, id, chatID, workflowID string, status YieldStatus) {
	t.Helper()
	ctx := context.Background()
	yield := &Yield{
		ID:                 id,
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		ThreadID:           workflowID,
		StepID:             "step-1",
		Status:             status,
		CreatedAt:          time.Now(),
	}
	if err := repo.CreateYield(ctx, yield); err != nil {
		t.Fatalf("insertTestYield: %v", err)
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
	insertTestWorkflow(t, repo, "wf-1", chatID, "builtin://agent", WorkflowStatusRunning)

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
	insertTestWorkflow(t, repo, "wf-thread", chatID, "thread:abc123", WorkflowStatusRunning)

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

func TestGetChatActivity_AwaitingInput_PendingYield(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "chat-yield"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflow(t, repo, "wf-yield", chatID, "builtin://agent", WorkflowStatusRunning)
	insertTestYield(t, repo, "yield-1", chatID, "wf-yield", YieldStatusPending)

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
	insertTestWorkflow(t, repo, "wf-run", chatID, "builtin://agent", WorkflowStatusRunning)
	insertTestApproval(t, repo, "approval-pri", chatID, 1) // PENDING

	activity, err := repo.GetChatActivity(context.Background(), chatID)
	if err != nil {
		t.Fatalf("GetChatActivity: %v", err)
	}
	if activity != 2 {
		t.Fatalf("expected activity=2 (AWAITING_INPUT takes priority over RUNNING), got %d", activity)
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
	insertTestWorkflow(t, repo, "wf-transition", chatID, "builtin://agent", WorkflowStatusRunning)

	activity, err := repo.GetChatActivity(ctx, chatID)
	if err != nil {
		t.Fatalf("GetChatActivity after start: %v", err)
	}
	if activity != 1 {
		t.Fatalf("expected activity=1 (RUNNING) after workflow start, got %d", activity)
	}

	// Complete the workflow → activity should transition to IDLE (0).
	if err := repo.UpdateWorkflowStatus(ctx, "wf-transition", WorkflowStatusCompleted); err != nil {
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
