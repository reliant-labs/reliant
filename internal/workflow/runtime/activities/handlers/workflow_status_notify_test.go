// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A ROOT completion marks the chat unread, and that unread write is what
// raises the OS notification in the web client. An automation run nobody
// started by typing must not do either on success, or an hourly schedule
// notifies 24 times a day (WORKFLOW_UI.md §6.4). A chat a human started still
// does, and a run that completed into a declared failure still does whatever
// launched it.

// completeRootRun creates a chat whose launch event has the given kind (none
// when kind is empty), runs WorkflowStatus("completed") on its root workflow
// with the given declared outcome, and returns the chat's unread flag.
func completeRootRun(t *testing.T, kind core.TriggerEventKind, outcome string) bool {
	t.Helper()
	unread, _ := finishRun(t, kind, "completed", outcome, false)
	return unread
}

// finishRun is completeRootRun generalised over the terminal status and over
// whether the reporting workflow is a child of the chat's root. It returns the
// chat's unread flag and unread reason.
func finishRun(t *testing.T, kind core.TriggerEventKind, status, outcome string, child bool) (bool, string) {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()

	userID := uuid.NewString()
	projectID := uuid.NewString()
	chatID := uuid.NewString()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID:           chatID,
		ChatID:       chatID,
		WorkflowName: "builtin://agent",
		Thread:       chatID,
		Status:       db.Active(),
	}))

	if kind != "" {
		now := time.Now().UTC()
		created, err := h.Repo().CreateTriggerEvent(ctx, &core.TriggerEvent{
			ID:         uuid.NewString(),
			UserID:     userID,
			Kind:       kind,
			DedupeKey:  "dedupe-" + chatID,
			OccurredAt: now,
			Payload:    map[string]any{},
			Outcome:    core.TriggerEventLaunched,
			ChatID:     &chatID,
			CreatedAt:  now,
		})
		require.NoError(t, err)
		require.True(t, created)
	}

	input := WorkflowStatusInput{
		ChatID:       chatID,
		WorkflowID:   chatID,
		WorkflowName: "builtin://agent",
		Status:       status,
		Thread:       chatID,
		Outcome:      outcome,
	}
	if child {
		input.WorkflowID = uuid.NewString()
		input.ParentWorkflowID = chatID
		input.Thread = chatID + "/child"
	}
	var output WorkflowStatusOutput
	require.NoError(t, h.ExecuteActivity(NewWorkflowStatusActivity(h.Repo()).Execute, input, &output))
	require.True(t, output.Success)

	chat, err := h.Repo().GetChat(ctx, chatID)
	require.NoError(t, err)
	// The reason is not stored on the chat; it travels on the user update the
	// web client notifies from.
	reason := ""
	updates, err := h.Repo().GetUserUpdatesSince(ctx, userID, 0, 100)
	require.NoError(t, err)
	for _, u := range updates {
		var data struct {
			Reason string `json:"reason"`
			Unread *bool  `json:"unread"`
		}
		if json.Unmarshal(u.Data, &data) == nil && data.Unread != nil && *data.Unread {
			reason = data.Reason
		}
	}
	return chat.Unread, reason
}

func TestWorkflowStatus_FailureUnread(t *testing.T) {
	cases := []struct {
		name       string
		kind       core.TriggerEventKind
		status     string
		child      bool
		wantUnread bool
	}{
		{"schedule-launched root failed notifies", core.TriggerEventKindSchedule, "failed", false, true},
		{"interactive root failed notifies", core.TriggerEventKindChatStart, "failed", false, true},
		{"cancelled stays silent", core.TriggerEventKindChatStart, "cancelled", false, false},
		{"schedule-launched cancelled stays silent", core.TriggerEventKindSchedule, "cancelled", false, false},
		{"child failure does not notify", core.TriggerEventKindSchedule, "failed", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unread, reason := finishRun(t, tc.kind, tc.status, "", tc.child)
			assert.Equal(t, tc.wantUnread, unread)
			if tc.wantUnread {
				assert.Equal(t, "workflow_failed", reason)
			}
		})
	}
}

func TestWorkflowStatus_DeclaredFailureOutcomeUsesFailedReason(t *testing.T) {
	unread, reason := finishRun(t, core.TriggerEventKindSchedule, "completed", model.OutcomeFailure, false)
	assert.True(t, unread)
	assert.Equal(t, "workflow_failed", reason)
}

func TestWorkflowStatus_CompletionUnreadFollowsLaunchKind(t *testing.T) {
	cases := []struct {
		name       string
		kind       core.TriggerEventKind
		outcome    string
		wantUnread bool
	}{
		{"interactive chat notifies", core.TriggerEventKindChatStart, "", true},
		{"chat predating launch kinds notifies", "", "", true},
		{"schedule-launched run is silent", core.TriggerEventKindSchedule, "", false},
		{"schedule-launched declared success is silent", core.TriggerEventKindSchedule, model.OutcomeSuccess, false},
		{"agent-started run is silent", core.TriggerEventKindAgentStartRun, "", false},
		{"schedule-launched declared failure still notifies", core.TriggerEventKindSchedule, model.OutcomeFailure, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantUnread, completeRootRun(t, tc.kind, tc.outcome))
		})
	}
}

// An automation that needs input must still surface: the approval path marks
// the chat unread regardless of how the run was launched.
func TestApprovalCreate_MarksScheduleLaunchedChatUnread(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.NewString()
	projectID := uuid.NewString()
	chatID := uuid.NewString()
	workflowID := uuid.NewString()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestWorkflow(ctx, workflowID, chatID)
	now := time.Now().UTC()
	_, err := h.Repo().CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), UserID: userID, Kind: core.TriggerEventKindSchedule,
		DedupeKey: "dedupe-" + chatID, OccurredAt: now, Payload: map[string]any{},
		Outcome: core.TriggerEventLaunched, ChatID: &chatID, CreatedAt: now,
	})
	require.NoError(t, err)

	var output ApprovalCreateOutput
	require.NoError(t, h.ExecuteActivity(NewApprovalCreateActivity(h.Repo()).Execute, ApprovalCreateInput{
		ChatID:             chatID,
		WorkflowID:         workflowID,
		TemporalWorkflowID: workflowID,
		StepID:             "agent_loop",
		Title:              "Deploy to production?",
	}, &output))

	chat, err := h.Repo().GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, "schedule", chat.LaunchKind)
	assert.True(t, chat.Unread, "an automation awaiting approval must still surface")
}
