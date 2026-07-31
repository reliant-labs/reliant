// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// TestGetPendingQuestionRejectsUnknownChat is the regression behind
// "`workflow questions <short-id>` reports no gate while a gate is open".
//
// GetPendingQuestion used to skip the chat-existence check every other
// supervision RPC performs, so `WHERE chat_id = '<short-id>'` found no row and
// the handler returned an empty-but-successful response. The CLI cannot tell
// that apart from a genuinely quiet run, so it prints "No open questions." and
// exits 0 — a confident, clean, wrong answer. `workflow status` on the same id
// errors honestly, which is what made the pair so misleading: one command says
// the id is bad, the other says the run is fine.
//
// An id that does not name a chat must be an error, not an empty answer.
func TestGetPendingQuestionRejectsUnknownChat(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	projectID := "test-project-questions-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Questions Test Project",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "Gated run", ProjectID: projectID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now,
	}))

	svc := NewQuestionService(repo, nil)

	get := func(ctx context.Context, id string) (*connect.Response[reliantv1.GetPendingQuestionResponse], error) {
		return svc.GetPendingQuestion(ctx, connect.NewRequest(&reliantv1.GetPendingQuestionRequest{ChatId: id}))
	}

	t.Run("short id prefix of a real chat is not found", func(t *testing.T) {
		// The exact shape a supervisor hits: `workflow ps` printed an 8-char
		// prefix, and it was pasted into `workflow questions`.
		_, err := get(ctx, chatID[:8])
		require.Error(t, err, "a short id must not be answered as if it were a chat with no gate")
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("unknown id is not found", func(t *testing.T) {
		_, err := get(ctx, uuid.NewString())
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("another user's chat is not found", func(t *testing.T) {
		otherCtx := context.WithValue(context.Background(), auth.UserIDContextKey, "other-user")
		_, err := get(otherCtx, chatID)
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("real chat with no gate answers empty", func(t *testing.T) {
		// The honest empty answer must still work, or the fix has just traded
		// a false negative for a false positive.
		resp, err := get(ctx, chatID)
		require.NoError(t, err)
		assert.Nil(t, resp.Msg.GetQuestion())
	})
}
