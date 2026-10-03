// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Chat.RootStatus is the lifecycle of the chat's ROOT workflow, and every chat
// read path must carry it.
//
// Before chats_with_activity exposed root_workflow_state /
// root_workflow_stop_reason, nothing in Go ever assigned these: the proto's
// workflow_state and workflow_stop_reason went out zeroed on every chat, so
// the web's isWorkflowPaused was false even for a chat parked at
// STOPPED/PAUSED. The view carries it because the alternative is an N+1
// lookup per chat in a list, which is why it was skipped in the first place.
//
// The four statuses below are the ones the UI branches on. UNSPECIFIED is the
// fifth case and means something different from all of them: no root workflow
// row exists at all, which is a branched chat whose first run has not been
// created.

// chatWithRootWorkflow creates a chat plus a root workflow row whose id is the
// chat's workflow_id, matching production: a chat's root workflow is the row
// chats.workflow_id points at.
func chatWithRootWorkflow(t *testing.T, repo *Repo, chatID, workflowID string, status WorkflowStatus) {
	t.Helper()
	ctx := context.Background()

	chat := &Chat{
		ID:         chatID,
		Title:      "root status chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		State:      ChatStateIdle,
		WorkflowID: &workflowID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	require.NoError(t, repo.CreateChat(ctx, chat))
	createTestRootThread(t, repo, chatID)
	insertTestWorkflow(t, repo, workflowID, chatID, "test-workflow", status)
}

func TestChatReadsCarryRootWorkflowStatus(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	cases := []struct {
		name   string
		chatID string
		status WorkflowStatus
		want   WorkflowStatus
	}{
		{"pending", "chat-root-pending", Pending(), Pending()},
		{"active", "chat-root-active", Active(), Active()},
		{"paused", "chat-root-paused", Paused(), Paused()},
		{"completed", "chat-root-completed", Completed(), Completed()},
		{"failed", "chat-root-failed", Failed(), Failed()},
		{"cancelled", "chat-root-cancelled", Cancelled(), Cancelled()},
	}
	for _, tc := range cases {
		chatWithRootWorkflow(t, repo, tc.chatID, "wf-"+tc.chatID, tc.status)
	}

	// A chat with no root workflow row at all: a branch that has not started.
	// Its root status is UNSPECIFIED, not PENDING — there is no run yet.
	noRootID := "chat-root-absent"
	createActivityTestChat(t, repo, noRootID)

	for _, tc := range cases {
		t.Run("GetChat/"+tc.name, func(t *testing.T) {
			chat, err := repo.GetChat(ctx, tc.chatID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, chat.RootStatus)
		})

		t.Run("GetChatWithUserCheck/"+tc.name, func(t *testing.T) {
			chat, err := repo.GetChatWithUserCheck(ctx, tc.chatID, "test-user")
			require.NoError(t, err)
			assert.Equal(t, tc.want, chat.RootStatus)
		})
	}

	t.Run("GetChat/no root workflow", func(t *testing.T) {
		chat, err := repo.GetChat(ctx, noRootID)
		require.NoError(t, err)
		assert.Equal(t, WorkflowStatus{}, chat.RootStatus,
			"a chat with no root workflow row maps to WORKFLOW_STATE_UNSPECIFIED")
		assert.Equal(t, core.WorkflowStateUnspecified, chat.RootStatus.State)
	})

	t.Run("ListChats", func(t *testing.T) {
		projectID := "test-project"
		chats, err := repo.ListChats(ctx, ChatFilters{
			UserID:    "test-user",
			ProjectID: &projectID,
			Limit:     100,
		})
		require.NoError(t, err)

		byID := make(map[string]WorkflowStatus, len(chats))
		for _, c := range chats {
			byID[c.ID] = c.RootStatus
		}
		for _, tc := range cases {
			got, ok := byID[tc.chatID]
			require.True(t, ok, "ListChats must return %s", tc.chatID)
			assert.Equal(t, tc.want, got, "ListChats root status for %s", tc.name)
		}
		got, ok := byID[noRootID]
		require.True(t, ok)
		assert.Equal(t, WorkflowStatus{}, got)
	})

	t.Run("SearchChats", func(t *testing.T) {
		chats, err := repo.SearchChats(ctx, ChatSearchFilters{
			UserID:      "test-user",
			ProjectID:   "test-project",
			SearchQuery: "root status chat",
			Limit:       100,
		})
		require.NoError(t, err)
		require.NotEmpty(t, chats, "the search must match the fixture chats by title")

		byID := make(map[string]WorkflowStatus, len(chats))
		for _, c := range chats {
			byID[c.ID] = c.RootStatus
		}
		for _, tc := range cases {
			got, ok := byID[tc.chatID]
			require.True(t, ok, "SearchChats must return %s", tc.chatID)
			assert.Equal(t, tc.want, got, "SearchChats root status for %s", tc.name)
		}
	})
}

// TestChatRootStatusIgnoresNonRootWorkflows pins that the view reads the ROOT
// row specifically and not "any workflow for this chat". The activity column
// deliberately aggregates across threads and spawns; root status must not.
// A chat whose root run has completed while a spawn is still active is
// completed, and the web must not offer to resume it.
func TestChatRootStatusIgnoresNonRootWorkflows(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-root-vs-spawn"
	chatWithRootWorkflow(t, repo, chatID, "wf-root-vs-spawn", Completed())

	// A spawn under the same chat, still running. It is NOT the root row.
	insertTestWorkflow(t, repo, "wf-spawn-vs-root", chatID, "spawn-workflow", Active())

	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, Completed(), chat.RootStatus,
		"root status comes from workflows.id = chats.workflow_id, not from any workflow in the chat")
}
