// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The chat snapshot used to collect its non-message updates by reading
// GetUpdatesSince(chatID, 0, 10000) — every update TYPE, oldest-first — and
// discarding message updates in Go. Message updates dominate chat_updates, so
// on a long-lived chat the limit was spent on rows the caller was about to
// throw away and the genuinely needed non-message updates past the cap never
// reached the client at all. The oldest rows evicted the newest, which is
// backwards for a snapshot, and nothing ever backfilled them: the snapshot's
// sequence high-water mark is computed separately, so the client's gap
// detection never saw a hole.
//
// These tests pin the replacement's two guarantees: completeness regardless of
// how much message traffic precedes the interesting rows, and per-tool-call
// dedup down to the latest status.

func seedChatForUpdates(t *testing.T, repo *Repo, ctx context.Context) string {
	t.Helper()
	now := time.Now()
	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID:         chatID,
		Title:      "Update coverage chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))
	return chatID
}

func TestGetLatestNonMessageUpdatesPerEntity_NotEvictedByMessageVolume(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// An approval arrives first, then a wall of message updates buries it.
	// Under the old oldest-first read the approval survived only because it was
	// early; the row that used to be LOST is the late one below.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-early", `{"k":"early"}`))

	const messageNoise = 250
	for i := 0; i < messageNoise; i++ {
		require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeMessage,
			fmt.Sprintf("msg-%d", i), `{"content":"noise"}`))
	}

	// The update that mattered: emitted last, after the message flood. This is
	// exactly the row the capped scan dropped.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-late", `{"k":"late"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	byEntity := make(map[string]ChatUpdate, len(updates))
	for _, u := range updates {
		byEntity[u.EntityID] = u
		require.NotEqual(t, UpdateTypeMessage, u.UpdateType,
			"message updates must be filtered out in SQL, got entity %s", u.EntityID)
	}

	require.Contains(t, byEntity, "approval-early")
	require.Contains(t, byEntity, "approval-late",
		"an update emitted after heavy message traffic must still reach the snapshot")
	require.Len(t, updates, 2, "only the two non-message updates should survive")

	// Ordering is by sequence, not by the dedup key the query groups on.
	require.Less(t, updates[0].SequenceNumber, updates[1].SequenceNumber)
}

func TestGetLatestNonMessageUpdatesPerEntity_CollapsesToolCallToLatestStatus(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// EntityIDForToolCall embeds a timestamp, so the three status transitions of
	// ONE tool call each get a distinct entity_id. Deduping on entity_id alone
	// would replay all three; the frontend keys tool state by tool_call_id and
	// only renders the latest, so the snapshot should carry just one.
	toolCallID := "toolu_regression"
	for _, status := range []ToolCallStatus{
		ToolCallStatusPending,
		ToolCallStatusExecuting,
		ToolCallStatusCompleted,
	} {
		require.NoError(t, repo.EmitToolCallUpdate(ctx, chatID, ToolCallUpdate{
			ToolCallID: toolCallID,
			ToolName:   "view",
			Status:     status,
		}))
		// EntityIDForToolCall's timestamp has nanosecond precision, but sleep a
		// touch so the ids are unambiguously distinct even on a coarse clock —
		// distinct ids are the precondition this test is about.
		time.Sleep(time.Millisecond)
	}

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	require.Len(t, updates, 1,
		"three status transitions of one tool call must collapse to the latest")
	require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL, updates[0].UpdateType)
	require.Contains(t, string(updates[0].Data), string(ToolCallStatusCompleted),
		"the surviving row should be the newest status, not the first")
}

func TestGetLatestNonMessageUpdatesPerEntity_DedupsToolCallsByJSONToolCallID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// EntityIDForToolCall includes the tool-call id, but the id itself can
	// contain hyphens. Splitting entity_id on '-' collapses these two distinct
	// calls to the same key ("toolu_same") and drops one from the snapshot.
	firstToolCallID := "toolu_same-prefix-one"
	secondToolCallID := "toolu_same-prefix-two"
	for _, update := range []ToolCallUpdate{
		{ToolCallID: firstToolCallID, ToolName: "bash", Status: ToolCallStatusExecuting},
		{ToolCallID: secondToolCallID, ToolName: "bash", Status: ToolCallStatusCompleted},
	} {
		require.NoError(t, repo.EmitToolCallUpdate(ctx, chatID, update))
		time.Sleep(time.Millisecond)
	}

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	statusesByToolCallID := make(map[string]string, len(updates))
	for _, update := range updates {
		require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL, update.UpdateType)
		var payload ToolCallUpdate
		require.NoError(t, json.Unmarshal(update.Data, &payload))
		statusesByToolCallID[payload.ToolCallID] = string(payload.Status)
	}

	require.Equal(t, map[string]string{
		firstToolCallID:  string(ToolCallStatusExecuting),
		secondToolCallID: string(ToolCallStatusCompleted),
	}, statusesByToolCallID)
}

func TestGetLatestNonMessageUpdatesPerEntity_ExcludesStreamFinalized(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// STREAM_FINALIZED markers retire in-flight streaming placeholders. A fresh
	// snapshot has no in-flight deltas to retire — the finalized messages are
	// already present as persisted rows — so shipping them is pure weight.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeStreamFinalized, "finalized-1", `{"message_id":"m1"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "approval-1", `{"k":"v"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	require.Len(t, updates, 1)
	require.Equal(t, "approval-1", updates[0].EntityID)
}
