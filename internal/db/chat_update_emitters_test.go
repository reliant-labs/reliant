package db

import (
	"context"
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestEmitSkillInvocationUpdate_PersistsStructuredPayload(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-skill-invocation-test"

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Skill invocation chat",
		ProjectID: "test-project",
		UserID:    "test-user",
	}))

	update := SkillInvocationUpdate{
		ID:            "skill-invocation-abc",
		ChatID:        chatID,
		SkillName:     "debug-sql",
		RequestedName: "debug-sql",
		Trigger:       SkillInvocationTriggerExplicit,
		Status:        SkillInvocationStatusActivated,
		Message:       "Activated skill debug-sql",
		Timestamp:     "2026-03-04T12:34:56.123Z",
		Warnings:      []string{"example warning"},
	}

	require.NoError(t, repo.EmitSkillInvocationUpdate(ctx, chatID, update))

	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 20)
	require.NoError(t, err)
	require.Len(t, updates, 1)

	stored := updates[0]
	require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_SKILL_INVOCATION, stored.UpdateType)
	require.Equal(t, update.ID, stored.EntityID)

	var payload SkillInvocationUpdate
	require.NoError(t, json.Unmarshal(stored.Data, &payload))
	require.Equal(t, update.ID, payload.ID)
	require.Equal(t, update.ChatID, payload.ChatID)
	require.Equal(t, update.SkillName, payload.SkillName)
	require.Equal(t, update.RequestedName, payload.RequestedName)
	require.Equal(t, update.Trigger, payload.Trigger)
	require.Equal(t, update.Status, payload.Status)
	require.Equal(t, update.Message, payload.Message)
	require.Equal(t, update.Timestamp, payload.Timestamp)
	require.Equal(t, update.Warnings, payload.Warnings)
	require.Equal(t, UpdateTypeSkillInvocation, payload.UpdateType)
}
