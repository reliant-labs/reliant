// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// An ownership check reads the chats row, not the activity view: the row
// carries the chat's own columns, and a chat of another user is the same
// ErrChatNotFound as one that does not exist.
func TestGetChatRowWithUserCheck_ReadsTheRowAndHidesOthersChats(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	workflowID := uuid.NewString()
	chat := &Chat{
		ID: uuid.NewString(), Title: "row", ProjectID: "test-project", UserID: "owner",
		WorkflowID: &workflowID, NoMachine: true, State: ChatStateArchived,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateChat(ctx, chat))

	row, err := repo.GetChatRowWithUserCheck(ctx, chat.ID, "owner")
	require.NoError(t, err)
	require.Equal(t, chat.ID, row.ID)
	require.Equal(t, "owner", row.UserID)
	require.Equal(t, "test-project", row.ProjectID)
	require.Equal(t, workflowID, row.MainThreadID())
	require.True(t, row.NoMachine)
	require.Equal(t, ChatStateArchived, row.State)

	_, err = repo.GetChatRowWithUserCheck(ctx, chat.ID, "someone-else")
	require.True(t, errors.Is(err, core.ErrChatNotFound), "another user's chat: %v", err)
	_, err = repo.GetChatRow(ctx, uuid.NewString())
	require.True(t, errors.Is(err, core.ErrChatNotFound), "a missing chat: %v", err)
}
