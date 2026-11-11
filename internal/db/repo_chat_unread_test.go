// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unreadUpdates returns the reasons of userID's unread announcements for chatID.
func unreadUpdates(t *testing.T, repo *Repo, userID, chatID string) []string {
	t.Helper()
	updates, err := repo.GetUserUpdatesSince(context.Background(), userID, 0, 1000)
	require.NoError(t, err)
	var reasons []string
	for _, u := range updates {
		if u.EntityID != chatID || u.UpdateType != UserUpdateChatStateChange {
			continue
		}
		var data map[string]interface{}
		require.NoError(t, json.Unmarshal(u.Data, &data))
		if _, ok := data["unread"]; ok {
			reasons = append(reasons, data["reason"].(string))
		}
	}
	return reasons
}

func TestUpdateChatUnread_SetsFlagAndAnnouncesIt(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const chatID = "chat-unread-own-tx"
	createActivityTestChat(t, repo, chatID)

	require.NoError(t, repo.UpdateChatUnread(ctx, chatID, true, "workflow_completed"))

	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.True(t, chat.Unread)
	assert.Equal(t, []string{"workflow_completed"}, unreadUpdates(t, repo, "test-user", chatID))
}

// A serialization failure on the user's update counter aborts the caller's
// whole SERIALIZABLE transaction. UpdateChatUnread must hand it to that
// transaction's RunTx, which retries it, rather than log it and return nil
// (reliant #2393): the retry then lands the flag and exactly one
// announcement.
func TestUpdateChatUnread_InCallerTx_ReturnsSerializationFailureForRetry(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const chatID = "chat-unread-40001"
	createActivityTestChat(t, repo, chatID)

	racingUpdate := func() *UserUpdate {
		return &UserUpdate{
			UserID:     "test-user",
			UpdateType: UserUpdateChatStateChange,
			EntityType: EntityTypeChat,
			EntityID:   "another-entity",
			Data:       []byte(`{}`),
		}
	}
	// The user's counter row exists before the race, as it does in prod.
	require.NoError(t, repo.CreateUserUpdate(ctx, racingUpdate()))

	attempts := 0
	var firstAttemptErr error
	err := repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		attempts++
		// The first statement fixes this transaction's snapshot.
		if _, err := repo.GetChat(txCtx, chatID); err != nil {
			return err
		}
		if attempts == 1 {
			// Another of the user's updates commits after that snapshot, so
			// this transaction's counter UPDATE is refused with 40001.
			require.NoError(t, repo.CreateUserUpdate(ctx, racingUpdate()))
		}
		err := repo.UpdateChatUnread(txCtx, chatID, true, "question_pending")
		if attempts == 1 {
			firstAttemptErr = err
		}
		return err
	})
	require.NoError(t, err)

	var pgErr *pgconn.PgError
	require.True(t, errors.As(firstAttemptErr, &pgErr),
		"the conflict must reach the caller's transaction, got %v", firstAttemptErr)
	assert.Equal(t, "40001", pgErr.Code)
	assert.Equal(t, 2, attempts)

	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	assert.True(t, chat.Unread)
	assert.Equal(t, []string{"question_pending"}, unreadUpdates(t, repo, "test-user", chatID))
}
