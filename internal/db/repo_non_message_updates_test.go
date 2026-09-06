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

func TestGetLatestNonMessageUpdatesPerEntity_ThreadAnnouncementSurvivesWorkflowStatus(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// An INLINE fork reuses its parent's workflow id (see
	// inline_workflow_executor.go: "Inline workflows use parent's workflow
	// ID"), and both thread and workflow_status updates key their entity_id on
	// that workflow id. So the fork's one-and-only announcement shares an
	// entity_id with every workflow_status row of the run that created it.
	//
	// Deduping on entity_id alone therefore collapses two DIFFERENT KINDS of
	// update into one bucket, and the last write wins. When the run finishes it
	// writes a workflow_status, which evicts the announcement — the snapshot
	// then has no origin for the thread, InterleavedTimeline cannot classify
	// it, and every message on it is dropped from the timeline.
	//
	// The tell is that this only breaks on a COMPLETED run: while streaming,
	// and on a run whose last write happened to be the announcement, the same
	// chat renders correctly.
	workflowID := uuid.New().String()
	threadID := uuid.New().String()

	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeWorkflowStatus, workflowID,
		`{"update_type":"workflow_status","workflow_id":"`+workflowID+`","status":"started"}`))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeThread, workflowID,
		`{"update_type":"thread","id":"`+workflowID+`","thread":"`+threadID+`","origin":"fork","thread_title":"implement #1"}`))
	// The run completes. This is the row that used to evict the announcement.
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeWorkflowStatus, workflowID,
		`{"update_type":"workflow_status","workflow_id":"`+workflowID+`","status":"completed"}`))

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	var threadUpdate *ChatUpdate
	var workflowStatus *ChatUpdate
	for i := range updates {
		switch updates[i].UpdateType {
		case UpdateTypeThread:
			threadUpdate = &updates[i]
		case UpdateTypeWorkflowStatus:
			workflowStatus = &updates[i]
		}
	}

	require.NotNil(t, threadUpdate,
		"the thread announcement must survive: it is the only record of the thread's origin, "+
			"and without it the timeline drops every message on that thread")
	require.Contains(t, string(threadUpdate.Data), `"origin":"fork"`)
	require.Contains(t, string(threadUpdate.Data), threadID)

	// Deduping thread updates by thread id must not weaken workflow_status
	// dedup: its own latest-wins collapse still has to hold.
	require.NotNil(t, workflowStatus, "the latest workflow_status must still be delivered")
	require.Contains(t, string(workflowStatus.Data), `"status":"completed"`,
		"workflow_status must still collapse to its newest row")
}

func TestGetLatestNonMessageUpdatesPerEntity_CollapsesThreadToLatestStatus(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := seedChatForUpdates(t, repo, ctx)

	// Two threads under ONE inline workflow id — the shape the entity_id key
	// could not represent. Each must survive independently, and each must
	// collapse to its own latest status rather than replaying its history.
	workflowID := uuid.New().String()
	forkThread := uuid.New().String()
	nodeThread := uuid.New().String()

	for _, u := range []struct{ thread, origin, status string }{
		{forkThread, "fork", "running"},
		{nodeThread, "node", "running"},
		{forkThread, "fork", "completed"},
	} {
		require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeThread, workflowID,
			`{"update_type":"thread","id":"`+workflowID+`","thread":"`+u.thread+
				`","origin":"`+u.origin+`","status":"`+u.status+`"}`))
	}

	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	statusByThread := map[string]string{}
	originByThread := map[string]string{}
	for _, update := range updates {
		require.Equal(t, UpdateTypeThread, update.UpdateType)
		var payload struct {
			Thread string `json:"thread"`
			Origin string `json:"origin"`
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(update.Data, &payload))
		statusByThread[payload.Thread] = payload.Status
		originByThread[payload.Thread] = payload.Origin
	}

	require.Equal(t, map[string]string{
		forkThread: "completed",
		nodeThread: "running",
	}, statusByThread, "each thread keeps its own latest status")
	// Origin is what separates a spawned sub-agent from a node/fork thread in
	// the UI, so it must survive per-thread rather than be smeared together.
	require.Equal(t, map[string]string{
		forkThread: "fork",
		nodeThread: "node",
	}, originByThread)
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
