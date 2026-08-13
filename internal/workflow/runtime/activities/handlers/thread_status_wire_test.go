// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// The UI treats a thread as active only when its record says "running" or
// "active" (useIsThreadActive, web/src/store/threadActivityStore.ts). The
// spawn path writes a "running" record when the sub-thread is created, and
// ThreadStatus then emits its own update for the same thread — so if that
// second update carries the raw lifecycle verb "started", it OVERWRITES the
// good record with one the UI reads as inactive, and the spawned sub-thread
// shows no thinking indicator for its entire run.
func TestThreadStatus_StartedEmitsRunningForTheUI(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	threadID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	h.CreateTestThread(ctx, chatID, threadID)

	activityInstance := NewThreadStatusActivity(h.Repo())
	var out ThreadStatusOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, ThreadStatusInput{
		ChatID:      chatID,
		ThreadID:    threadID,
		Status:      "started",
		WorkflowID:  threadID,
		ThreadTitle: "spawned sub-agent",
	}, &out))
	require.True(t, out.Success)

	status := latestThreadUpdateStatus(t, h, chatID, threadID)
	require.Equal(t, "running", status,
		`the UI only treats "running"/"active" as active; emitting the raw verb `+
			`"started" hides the thinking indicator on spawned sub-threads`)
}

// Terminal verbs must survive untouched — the UI compares those by name to
// decide a thread has finished.
func TestThreadStatus_TerminalVerbsPassThrough(t *testing.T) {
	for _, verb := range []string{"completed", "failed", "cancelled", "expired"} {
		t.Run(verb, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()

			userID := uuid.New().String()
			projectID := uuid.New().String()
			chatID := uuid.New().String()
			threadID := uuid.New().String()

			h.CreateTestProject(ctx, projectID, userID)
			h.CreateTestChat(ctx, chatID, projectID, userID)
			h.CreateTestThread(ctx, chatID, threadID)

			activityInstance := NewThreadStatusActivity(h.Repo())
			var out ThreadStatusOutput
			require.NoError(t, h.ExecuteActivity(activityInstance.Execute, ThreadStatusInput{
				ChatID:     chatID,
				ThreadID:   threadID,
				Status:     verb,
				WorkflowID: threadID,
			}, &out))

			require.Equal(t, verb, latestThreadUpdateStatus(t, h, chatID, threadID))
		})
	}
}

// Every thread update must carry origin, even when the caller does not supply
// one — and almost none of them do (the inline executor only passes an origin
// for a fork).
//
// Omitting it is invisible on a live stream, because the client merges
// successive updates and carries origin forward from the previous record. It
// is NOT invisible on reconnect: GetLatestNonMessageUpdatesPerEntity keeps only
// the newest row per entity, so a reloading client receives this update alone,
// with nothing to carry forward from. origin comes back undefined,
// isSpawnOrigin() goes false, and BackgroundWorkPill silently drops every
// running spawn — the chat looks idle while six sub-agents are still working.
func TestThreadStatus_UpdateCarriesPersistedOrigin(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	threadID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	_, err := h.Repo().CreateThread(ctx, &db.Thread{
		ID:     threadID,
		ChatID: chatID,
		Origin: db.ThreadOriginSpawn,
	})
	require.NoError(t, err)

	activityInstance := NewThreadStatusActivity(h.Repo())
	var out ThreadStatusOutput
	// Origin deliberately left empty: this is what the inline executor sends
	// for a spawned sub-agent's thread.
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, ThreadStatusInput{
		ChatID:      chatID,
		ThreadID:    threadID,
		Status:      "started",
		WorkflowID:  threadID,
		ThreadTitle: "spawned sub-agent",
	}, &out))
	require.True(t, out.Success)

	require.Equal(t, db.ThreadOriginSpawn, latestThreadUpdateField(t, h, chatID, threadID, "origin"),
		"a thread update read in isolation (the reconnect snapshot) must still "+
			"identify the thread as a spawn, or the background-work pill drops it")
}

// A caller that DOES state an origin is authoritative over the stored column —
// the fork case, where the executor genuinely knows what it created.
func TestThreadStatus_ExplicitOriginWins(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	threadID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	_, err := h.Repo().CreateThread(ctx, &db.Thread{
		ID:     threadID,
		ChatID: chatID,
		Origin: db.ThreadOriginNode,
	})
	require.NoError(t, err)

	activityInstance := NewThreadStatusActivity(h.Repo())
	var out ThreadStatusOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute, ThreadStatusInput{
		ChatID:     chatID,
		ThreadID:   threadID,
		Status:     "started",
		WorkflowID: threadID,
		Origin:     db.ThreadOriginFork,
	}, &out))

	require.Equal(t, db.ThreadOriginFork,
		latestThreadUpdateField(t, h, chatID, threadID, "origin"))
}

// latestThreadUpdateStatus returns the `status` field of the most recent
// "thread" chat_update for the given thread.
func latestThreadUpdateStatus(t *testing.T, h *IdempotencyTestHelper, chatID, threadID string) string {
	t.Helper()
	return latestThreadUpdateField(t, h, chatID, threadID, "status")
}

// latestThreadUpdateField returns one field of the most recent "thread"
// chat_update for the given thread.
func latestThreadUpdateField(t *testing.T, h *IdempotencyTestHelper, chatID, threadID, field string) string {
	t.Helper()

	updates, err := h.Repo().GetUpdatesSince(context.Background(), chatID, 0, 100)
	require.NoError(t, err)

	value := ""
	found := false
	for _, u := range updates {
		var payload map[string]any
		if err := json.Unmarshal([]byte(u.Data), &payload); err != nil {
			continue
		}
		if payload["update_type"] != "thread" || payload["thread"] != threadID {
			continue
		}
		found = true
		if v, ok := payload[field].(string); ok {
			value = v
		} else {
			value = ""
		}
	}
	require.True(t, found, "no thread update was emitted for %s", threadID)
	require.NotEmpty(t, value, "thread update for %s carried no %q", threadID, field)
	return value
}
