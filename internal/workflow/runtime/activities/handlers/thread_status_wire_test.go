// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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

// latestThreadUpdateStatus returns the `status` field of the most recent
// "thread" chat_update for the given thread.
func latestThreadUpdateStatus(t *testing.T, h *IdempotencyTestHelper, chatID, threadID string) string {
	t.Helper()

	updates, err := h.Repo().GetUpdatesSince(context.Background(), chatID, 0, 100)
	require.NoError(t, err)

	status := ""
	for _, u := range updates {
		var payload struct {
			UpdateType string `json:"update_type"`
			Thread     string `json:"thread"`
			Status     string `json:"status"`
		}
		if err := json.Unmarshal([]byte(u.Data), &payload); err != nil {
			continue
		}
		if payload.UpdateType == "thread" && payload.Thread == threadID {
			status = payload.Status
		}
	}
	require.NotEmpty(t, status, "no thread update was emitted for %s", threadID)
	return status
}
