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
	"github.com/stretchr/testify/require"
)

// The snapshot must carry a spawn's originating tool call id, so the cancel
// button survives a reload.
//
// This is the same failure shape as thread `origin` (see
// streaming_snapshot_thread_origin_test.go), on a different field, and it is
// the one the user hit: "only 1 cancel button on the spawn pills in the UI",
// then "looks like it populates the button when it happens, but not on a
// refresh".
//
// Two thread updates are written per spawn, microseconds apart:
//
//	workflow_status.emitThreadUpdate  — carries spawned_by_tool_call_id
//	thread_status.emitThreadUpdate    — lifecycle only, OMITS it
//
// On the live stream that is invisible: mergeActiveThreads carries the field
// forward from the earlier record. But the reconnect snapshot
// (GetLatestNonMessageUpdatesPerEntity) keeps only the NEWEST row per entity —
// which is the lifecycle row that omits it — and hands it to a client with no
// history to carry forward from. spawn.toolCallId goes undefined, and
// BackgroundWorkPill's `{spawn.toolCallId && ...}` guard drops the ■ button.
//
// The user therefore keeps the button on spawns created in the current page
// session and loses it on every spawn that has survived a refresh — exactly the
// long-running agents they most want to stop.
//
// tool_calls.child_workflow_id -> workflows.id -> workflows.thread is the
// durable link, already written by the spawn path, and the same join
// ListSpawnChildrenForThread and cancelChildWorkflowForToolCall already trust.
// Verified against live chat data: both stranded spawn threads recovered their
// toolu_* id through it.
func TestChatSnapshot_ThreadUpdateCarriesSpawnedByToolCallID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	spawnThreadID := uuid.New().String()
	spawnWorkflowID := uuid.New().String()
	toolCallID := "toolu_01PHSCqg3WxYCkTzWY6v3Xwd"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "spawns", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	spawnTitle := "electron config via forge KCL"
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: spawnThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		WorkflowID: &spawnWorkflowID, Title: &spawnTitle,
		Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	// The durable link the spawn path writes: the parent's spawn tool call
	// names the child workflow, and the child workflow names the thread.
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: spawnWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: spawnThreadID, CreatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &mainThreadID,
		ToolName: "spawn", ChildWorkflowID: &spawnWorkflowID,
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	// The lifecycle row that WINS per-entity dedup. Correct in every respect
	// except that it never restates spawned_by_tool_call_id — exactly what
	// thread_status.emitThreadUpdate writes today.
	lifecycle, err := json.Marshal(map[string]any{
		"update_type":  "thread",
		"id":           spawnWorkflowID,
		"chat_id":      chatID,
		"thread":       spawnThreadID,
		"workflow_id":  spawnWorkflowID,
		"origin":       db.ThreadOriginSpawn,
		"status":       "running",
		"thread_title": spawnTitle,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, spawnWorkflowID, string(lifecycle)))

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	var payload map[string]any
	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			continue
		}
		var candidate map[string]any
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &candidate))
		if candidate["thread"] == spawnThreadID {
			payload = candidate
		}
	}
	require.NotNil(t, payload, "snapshot must carry the spawn thread's update")

	require.Equal(t, toolCallID, payload["spawned_by_tool_call_id"],
		"a reloading client sees this row alone; without the tool call id the "+
			"background-work pill renders no cancel button and a long-running "+
			"agent becomes unstoppable from the UI")
}

// A RESUMED spawn issues several tool calls against ONE child thread, and the
// snapshot must offer the newest — the call still executing is the only one a
// cancel can stop; an earlier, already-completed call would be a no-op button.
//
// Not hypothetical: in live chat data one thread had 6 tool calls against it.
// The ordering that makes this come out right (requested_at ASC, folded into a
// map so the last write wins) lives in SQL, where it is easy to drop by
// accident, so it is pinned here.
func TestChatSnapshot_ResumedSpawnUsesNewestToolCallID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	spawnThreadID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "resumed spawn", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)

	firstWorkflowID := uuid.New().String()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: spawnThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		WorkflowID: &firstWorkflowID, Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	// Each resumption is a NEW workflow id against the SAME thread — which is
	// why workflows.thread, not child_workflow_id, is the join key.
	oldToolCallID := "toolu_first_dispatch"
	newToolCallID := "toolu_latest_resumption"
	for _, dispatch := range []struct {
		workflowID string
		toolCallID string
		at         time.Time
	}{
		{firstWorkflowID, oldToolCallID, now.Add(-2 * time.Hour)},
		{uuid.New().String(), newToolCallID, now.Add(-1 * time.Minute)},
	} {
		require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
			ID: dispatch.workflowID, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: spawnThreadID, CreatedAt: dispatch.at,
		}))
		require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
			ID: dispatch.toolCallID, ChatID: chatID, ThreadID: &mainThreadID,
			ToolName: "spawn", ChildWorkflowID: &dispatch.workflowID,
			Status:      core.ToolCallStatusExecuting,
			RequestedAt: dispatch.at, CreatedAt: dispatch.at, UpdatedAt: dispatch.at,
		}))
	}

	lifecycle, err := json.Marshal(map[string]any{
		"update_type": "thread",
		"id":          firstWorkflowID,
		"chat_id":     chatID,
		"thread":      spawnThreadID,
		"origin":      db.ThreadOriginSpawn,
		"status":      "running",
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, firstWorkflowID, string(lifecycle)))

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	var payload map[string]any
	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			continue
		}
		var candidate map[string]any
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &candidate))
		if candidate["thread"] == spawnThreadID {
			payload = candidate
		}
	}
	require.NotNil(t, payload, "snapshot must carry the resumed spawn's thread update")

	require.Equal(t, newToolCallID, payload["spawned_by_tool_call_id"],
		"a resumed spawn must offer the newest tool call — cancelling the "+
			"superseded one would leave the agent running while the UI "+
			"reported it stopped")
}

// Reconciliation fills a gap; it never overwrites what an emitter stated.
func TestChatSnapshot_ThreadUpdateKeepsStoredSpawnedByToolCallID(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	chatID := uuid.New().String()
	mainThreadID := chatID
	spawnThreadID := uuid.New().String()
	spawnWorkflowID := uuid.New().String()
	statedToolCallID := "toolu_stated_by_emitter"
	joinToolCallID := "toolu_from_the_join"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "spawns", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: mainThreadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: spawnThreadID, ChatID: chatID, ParentThreadID: &mainThreadID,
		WorkflowID: &spawnWorkflowID, Origin: db.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: spawnWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: spawnThreadID, CreatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: joinToolCallID, ChatID: chatID, ThreadID: &mainThreadID,
		ToolName: "spawn", ChildWorkflowID: &spawnWorkflowID,
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	stated, err := json.Marshal(map[string]any{
		"update_type":             "thread",
		"id":                      spawnWorkflowID,
		"chat_id":                 chatID,
		"thread":                  spawnThreadID,
		"workflow_id":             spawnWorkflowID,
		"origin":                  db.ThreadOriginSpawn,
		"status":                  "running",
		"spawned_by_tool_call_id": statedToolCallID,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeThread, spawnWorkflowID, string(stated)))

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			continue
		}
		var candidate map[string]any
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &candidate))
		if candidate["thread"] == spawnThreadID {
			require.Equal(t, statedToolCallID, candidate["spawned_by_tool_call_id"],
				"reconciliation must not overwrite a tool call id the emitter stated")
		}
	}
}
