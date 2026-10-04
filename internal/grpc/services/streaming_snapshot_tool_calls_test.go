// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// The snapshot no longer replays the chat's tool_call update history; it
// synthesizes updates from the durable tool_calls rows for (a) calls whose
// block is in the message window and (b) every non-terminal call. These pin
// that bound and each side of it.

func snapshotToolCallStatuses(t *testing.T, snapshot *reliantv1.ChatSyncSnapshot) map[string]string {
	t.Helper()
	statuses := map[string]string{}
	for _, update := range snapshot.OtherUpdates {
		if update.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL {
			continue
		}
		var payload struct {
			ToolCallID string `json:"tool_call_id"`
			Status     string `json:"status"`
		}
		require.NoError(t, json.Unmarshal([]byte(update.DataJson), &payload))
		_, dup := statuses[payload.ToolCallID]
		require.False(t, dup, "one update per tool call, got a second for %s", payload.ToolCallID)
		statuses[payload.ToolCallID] = payload.Status
		require.Equal(t, snapshot.LatestSequence, update.SequenceNumber)
	}
	return statuses
}

func TestChatSnapshot_ToolCallsAreBoundedToWindowAndLive(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	fixture := setupToolCallDurableFixture(t, ctx, repo, `{"command":"ls"}`)

	now := time.Now().UTC()
	upsert := func(id string, status core.ToolCallStatus, messageID *string) {
		call := &db.ToolCall{
			ID: id, ChatID: fixture.chatID, ThreadID: &fixture.threadID, MessageID: messageID,
			ToolName: "bash", Status: status, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if status.IsTerminal() {
			call.CompletedAt = &now
		}
		require.NoError(t, repo.UpsertToolCall(ctx, call))
		// Stale history for every call: the old read would have replayed this.
		require.NoError(t, repo.EmitToolCallUpdate(ctx, fixture.chatID, db.ToolCallUpdate{
			ToolCallID: id, ToolName: "bash", Status: db.ToolCallStatusPending,
		}))
	}
	// In-window (its block is in the fixture's only message): terminal.
	upsert(fixture.toolCallID, core.ToolCallStatusCompleted, &fixture.messageID)
	// Out of window (no block anywhere): terminal must be absent, live must be present.
	upsert("toolu_old_done", core.ToolCallStatusCompleted, nil)
	upsert("toolu_old_failed", core.ToolCallStatusFailed, nil)
	upsert("toolu_old_executing", core.ToolCallStatusExecuting, nil)
	upsert("toolu_old_backgrounded", core.ToolCallStatusBackgrounded, nil)
	upsert("toolu_old_pending", core.ToolCallStatusPending, nil)

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, fixture.chatID)
	require.NoError(t, err)

	require.Equal(t, map[string]string{
		fixture.toolCallID:       string(db.ToolCallStatusCompleted), // durable row beats the stale pending update
		"toolu_old_executing":    string(db.ToolCallStatusExecuting),
		"toolu_old_backgrounded": string(db.ToolCallStatusBackgrounded),
		"toolu_old_pending":      string(db.ToolCallStatusPending),
	}, snapshotToolCallStatuses(t, snapshot))
}

func TestChatSnapshot_InWindowInheritedInFlightCallIsCancelledLikeItsBlock(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	fixture := setupToolCallDurableFixture(t, ctx, repo, `{"command":"ls"}`)

	// The viewing thread is the chat's root workflow id; without one the
	// inherited-call rule has nothing to compare against.
	chat, err := repo.GetChat(ctx, fixture.chatID)
	require.NoError(t, err)
	chat.WorkflowID = &fixture.threadID
	require.NoError(t, repo.UpdateChat(ctx, chat))

	// Executing in a thread other than the one being viewed: the block path
	// reports CANCELLED to this viewer, and the synthesized update must agree.
	now := time.Now().UTC()
	otherThread := uuid.New().String()
	_, err = repo.CreateThread(ctx, &db.Thread{ID: otherThread, ChatID: fixture.chatID, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: fixture.toolCallID, ChatID: fixture.chatID, ThreadID: &otherThread, MessageID: &fixture.messageID,
		ToolName: "bash", Status: core.ToolCallStatusExecuting, StartedAt: ptr.Of(now),
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, fixture.chatID)
	require.NoError(t, err)

	var blockStatus *reliantv1.ToolCallStatus
	for _, m := range snapshot.Messages {
		for _, b := range m.ContentBlocks {
			if b.ToolCallId != nil && *b.ToolCallId == fixture.toolCallID {
				blockStatus = b.ToolCallStatus
			}
		}
	}
	require.NotNil(t, blockStatus)
	require.Equal(t, reliantv1.ToolCallStatus_TOOL_CALL_STATUS_CANCELLED, *blockStatus)
	require.Equal(t, string(db.ToolCallStatusCancelled), snapshotToolCallStatuses(t, snapshot)[fixture.toolCallID])
}
