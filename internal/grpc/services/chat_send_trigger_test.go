// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runs"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// inputCapturingTemporalClient records the WorkflowInput of every root start.
type inputCapturingTemporalClient struct {
	absorbTestTemporalClient
	inputs []v2.WorkflowInput
}

func (c *inputCapturingTemporalClient) ExecuteWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, wf interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	for _, arg := range args {
		if input, ok := arg.(v2.WorkflowInput); ok {
			c.inputs = append(c.inputs, input)
		}
	}
	return c.absorbTestTemporalClient.ExecuteWorkflow(ctx, options, wf, args...)
}

// The trigger is fixed at launch for the life of the chat: a SendMessage that
// starts a new run of a completed chat re-supplies the chat's launch event, so
// `trigger.*` means the same thing in the continuation as in the first run.
func TestSendMessage_RestartOfCompletedChatCarriesLaunchTrigger(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())

	scheduledAt := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	triggerID := "trg-restart"
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: "test-user", ProjectID: mustChatProject(t, ctx, repo, fx.chatID),
		Name: "nightly", Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "builtin://agent",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: scheduledAt, UpdatedAt: scheduledAt,
	}))
	created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: "evt-restart", TriggerID: &triggerID, UserID: "test-user",
		Kind: core.TriggerEventKindSchedule, DedupeKey: "fire-restart", OccurredAt: scheduledAt,
		Payload: map[string]any{"scheduled_for": "2026-01-02T09:00:00Z", "trigger_name": "nightly"},
		Outcome: core.TriggerEventLaunched, ChatID: &fx.chatID, CreatedAt: scheduledAt,
	})
	require.NoError(t, err)
	require.True(t, created)

	temporal := &inputCapturingTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
	}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	_, err = service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "one more thing"))
	require.NoError(t, err)

	require.Len(t, temporal.inputs, 1, "a completed chat restarts with one new root run")
	trigger := temporal.inputs[0].Trigger
	require.NotNil(t, trigger, "the restart must carry the chat's launch trigger")
	assert.Equal(t, "schedule", trigger.Kind)
	assert.Equal(t, triggerID, trigger.TriggerID)
	assert.Equal(t, "evt-restart", trigger.EventID)
	assert.Equal(t, "nightly", trigger.Payload["trigger_name"])
}

func mustChatProject(t *testing.T, ctx context.Context, repo *db.Repo, chatID string) string {
	t.Helper()
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	return chat.ProjectID
}

// The wire carries the chat's origin, and exclude_automations hides schedule
// chats unless one is waiting on a human.
func TestListChatsAndGetChatCarryLaunchOriginAndExcludeAutomations(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	service := &ChatService{database: repo}

	ctx, quiet := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	projectID := mustChatProject(t, ctx, repo, quiet.chatID)

	// Two more chats in the same project: an interactive one and a schedule
	// chat with a pending approval.
	addChat := func(id string) {
		now := time.Now().UTC()
		root := id
		require.NoError(t, repo.CreateChat(ctx, &db.Chat{
			ID: id, UserID: "test-user", Title: id, ProjectID: projectID, State: db.ChatStateIdle,
			WorkflowID: &root, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		_, err := repo.CreateThread(ctx, &db.Thread{ID: id, ChatID: id, Origin: db.ThreadOriginMain, Status: db.ThreadStatusRunning, CreatedAt: now})
		require.NoError(t, err)
	}
	addChat("wire-interactive")
	addChat("wire-needs-approval")

	scheduledAt := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	triggerID := "trg-wire"
	require.NoError(t, repo.CreateTrigger(ctx, &core.Trigger{
		ID: triggerID, UserID: "test-user", ProjectID: projectID, Name: "nightly",
		Kind: core.TriggerKindSchedule, Enabled: true, Workflow: "builtin://agent",
		Config: []byte(`{"interval":"1h"}`), CreatedAt: scheduledAt, UpdatedAt: scheduledAt,
	}))
	record := func(chatID string, kind core.TriggerEventKind, trigger *string) {
		created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
			ID: "evt-" + chatID, TriggerID: trigger, UserID: "test-user", Kind: kind,
			DedupeKey: "dedupe-" + chatID, OccurredAt: scheduledAt, Payload: map[string]any{},
			Outcome: core.TriggerEventLaunched, ChatID: &chatID, CreatedAt: scheduledAt,
		})
		require.NoError(t, err)
		require.True(t, created)
	}
	record(quiet.chatID, core.TriggerEventKindSchedule, &triggerID)
	record("wire-interactive", core.TriggerEventKindChatStart, nil)
	record("wire-needs-approval", core.TriggerEventKindSchedule, &triggerID)
	require.NoError(t, repo.CreateApproval(ctx, &db.Approval{
		ID: "wire-approval", ChatID: "wire-needs-approval", ApprovalType: 1, Status: 1, CreatedAt: time.Now().UTC(),
	}))

	getResp, err := service.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: quiet.chatID}))
	require.NoError(t, err)
	require.NotNil(t, getResp.Msg.Chat.LaunchKind)
	assert.Equal(t, "schedule", *getResp.Msg.Chat.LaunchKind)
	require.NotNil(t, getResp.Msg.Chat.TriggerId)
	assert.Equal(t, triggerID, *getResp.Msg.Chat.TriggerId)

	list := func(exclude bool) map[string]bool {
		resp, err := service.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID, ExcludeAutomations: &exclude}))
		require.NoError(t, err)
		ids := map[string]bool{}
		for _, c := range resp.Msg.Chats {
			ids[c.Id] = true
		}
		return ids
	}
	assert.True(t, list(false)[quiet.chatID])

	filtered := list(true)
	assert.False(t, filtered[quiet.chatID], "a quiet scheduled chat is hidden")
	assert.True(t, filtered["wire-interactive"])
	assert.True(t, filtered["wire-needs-approval"], "a scheduled chat awaiting approval stays visible")
}
