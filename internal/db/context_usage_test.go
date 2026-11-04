// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/testkit"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// TestGetContextUsage_AgreesWithTheGeneralWalk pins GetContextUsage's one-read
// fast path (a token-bearing message in the thread's own window) to what the
// general token-count and threshold walks compute, and checks a fork with no
// tokens of its own still inherits both through the general path.
func TestGetContextUsage_AgreesWithTheGeneralWalk(t *testing.T) {
	testkit.Slow(t, "a DB-backed test with many round trips to pgtest's Postgres")
	t.Parallel()

	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	chatID := uuid.New().String()
	parent := uuid.New().String()
	child := uuid.New().String()
	parentCW := uuid.New().String()
	model := "claude-sonnet-4"

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "usage", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: parent, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{ID: parentCW, ThreadID: parent, CreatedAt: now})
	require.NoError(t, err)
	for i := 1; i <= 4; i++ {
		tokens := 1000 * i
		msg := &Message{
			ID: uuid.New().String(), ChatID: chatID, Ordinal: int64(i), Seq: int64(i),
			ContextWindowID: parentCW, ThreadID: parent, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			CreatedAt: now, UpdatedAt: now,
		}
		if i%2 == 0 { // every other turn reports usage
			msg.TokenCount = &tokens
			msg.Model = &model
		}
		require.NoError(t, repo.CreateMessage(ctx, msg))
	}

	_, err = repo.CreateThread(ctx, &Thread{
		ID: child, ChatID: chatID, ParentThreadID: &parent,
		ForkAtMessageID: messageIDAtOrdinal(t, ctx, repo, parentCW, 3), CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{ID: uuid.New().String(), ThreadID: child, CreatedAt: now})
	require.NoError(t, err)

	for _, tc := range []struct {
		thread    string
		tokens    int64
		threshold int
	}{
		{parent, 4000, models.CompactionThresholdForModel(model)}, // fast path: its own newest token row
		{child, 2000, models.CompactionThresholdForModel(model)},  // inherited at the fork point
	} {
		usage, err := repo.GetContextUsage(ctx, chatID, tc.thread)
		require.NoError(t, err)
		require.Equal(t, tc.tokens, usage.ThreadTokenCount)
		require.Equal(t, int64(tc.threshold), usage.CompactionThreshold)

		general, err := repo.GetThreadTokenCount(ctx, tc.thread, nil)
		require.NoError(t, err)
		require.Equal(t, general, usage.ThreadTokenCount, "the fast path must agree with GetThreadTokenCount")
		require.Equal(t, int64(repo.resolveThreadCompactionThreshold(ctx, tc.thread, nil, nil)), usage.CompactionThreshold,
			"the fast path must agree with resolveThreadCompactionThreshold")
	}
}
