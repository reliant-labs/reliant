// Copyright (c) 2025 Reliant Labs
// Comprehensive tests for GetThreadTokenCount - the unified token counting function.
// These tests verify the refactored token counting system that:
// 1. Queries tokens directly from messages (no caching)
// 2. Handles fork inheritance automatically
// 3. Works with Thread-based fork metadata
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// messageIDAtOrdinal resolves the message with the given ordinal in the given
// context window. Test fixtures below build fork points from ordinals because
// that's how they already track message identity; this mirrors the
// resolution 20260803010000_fork_points_reference_messages.sql's backfill
// does against real data.
func messageIDAtOrdinal(t *testing.T, ctx context.Context, repo *Repo, cwID string, ordinal int64) *string {
	t.Helper()
	msgs, err := repo.GetMessagesByContextWindow(ctx, cwID, nil)
	require.NoError(t, err)
	for _, m := range msgs {
		if m.Ordinal == ordinal {
			id := m.ID
			return &id
		}
	}
	t.Fatalf("no message with ordinal %d in context window %s", ordinal, cwID)
	return nil
}

// =============================================================================
// BASIC FUNCTIONALITY TESTS
// =============================================================================

// TestGetThreadTokenCount_EmptyThread tests that an empty thread returns 0 tokens.
func TestGetThreadTokenCount_EmptyThread(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread with no messages
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create context window
	cwID := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Empty thread should return 0 tokens
	tokens, err := repo.GetThreadTokenCount(ctx, threadID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), tokens, "empty thread should return 0 tokens")
}

// TestGetThreadTokenCount_SingleMessage tests token count from a single message.
func TestGetThreadTokenCount_SingleMessage(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create context window
	cwID := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create message with token count
	tokenCount := 1650
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ContextWindowID: cwID,
		ThreadID:        threadID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		TokenCount:      &tokenCount,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Should return token count
	tokens, err := repo.GetThreadTokenCount(ctx, threadID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1650), tokens, "should return token count")
}

// TestGetThreadTokenCount_MultipleMessages tests that we get tokens from the latest message.
// Token counts are cumulative (each LLM response reports total context it saw).
func TestGetThreadTokenCount_MultipleMessages(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create context window
	cwID := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create multiple messages with cumulative token counts
	// User message (no tokens)
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ContextWindowID: cwID,
		ThreadID:        threadID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Assistant message 1: 5000 tokens
	tokens1 := 5000
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         2,
		Seq:             2,
		ContextWindowID: cwID,
		ThreadID:        threadID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		TokenCount:      &tokens1,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// User message 2 (no tokens)
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         3,
		Seq:             3,
		ContextWindowID: cwID,
		ThreadID:        threadID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Assistant message 2: 10000 tokens (cumulative)
	tokens2 := 10000
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         4,
		Seq:             4,
		ContextWindowID: cwID,
		ThreadID:        threadID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		TokenCount:      &tokens2,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Should return latest (highest ordinal) token count
	tokens, err := repo.GetThreadTokenCount(ctx, threadID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(10000), tokens, "should return tokens from latest message")
}

// TestGetThreadTokenCount_MaxOrdinal tests that maxOrdinal parameter works correctly.
func TestGetThreadTokenCount_MaxOrdinal(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create context window
	cwID := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create messages with cumulative tokens
	tokenCounts := []int{5000, 10000, 15000, 20000, 25000}
	for i, total := range tokenCounts {
		tc := total
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i + 1),
			Seq:             int64(i + 1),
			ContextWindowID: cwID,
			ThreadID:        threadID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &tc,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	testCases := []struct {
		maxOrdinal     int64
		expectedTokens int64
	}{
		{maxOrdinal: 1, expectedTokens: 5000},
		{maxOrdinal: 2, expectedTokens: 10000},
		{maxOrdinal: 3, expectedTokens: 15000},
		{maxOrdinal: 4, expectedTokens: 20000},
		{maxOrdinal: 5, expectedTokens: 25000},
	}

	for _, tc := range testCases {
		t.Run("max_ordinal_"+string(rune('0'+tc.maxOrdinal)), func(t *testing.T) {
			ord := tc.maxOrdinal
			tokens, err := repo.GetThreadTokenCount(ctx, threadID, &ord)
			require.NoError(t, err)
			require.Equal(t, tc.expectedTokens, tokens)
		})
	}
}

// =============================================================================
// FORK INHERITANCE TESTS
// =============================================================================

// TestGetThreadTokenCount_ForkInheritance tests that forked threads inherit tokens from parent.
func TestGetThreadTokenCount_ForkInheritance(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	parentThread := uuid.New().String()
	childThread := uuid.New().String()
	parentCW := uuid.New().String()
	forkOrdinal := int64(3)

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        parentThread,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent context window
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        parentCW,
		ThreadID:  parentThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent messages with tokens
	tokenCount := 16000
	for i := 1; i <= 5; i++ {
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: parentCW,
			ThreadID:        parentThread,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &tokenCount,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Create child thread forked at ordinal 3
	_, err = repo.CreateThread(ctx, &Thread{
		ID:              childThread,
		ChatID:          chatID,
		ParentThreadID:  &parentThread,
		ForkAtMessageID: messageIDAtOrdinal(t, ctx, repo, parentCW, forkOrdinal),
		CreatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create child context window (no messages yet)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        uuid.New().String(),
		ThreadID:  childThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Child with no messages should inherit parent's tokens at fork point
	tokens, err := repo.GetThreadTokenCount(ctx, childThread, nil)
	require.NoError(t, err)
	require.Equal(t, int64(16000), tokens, "forked thread should inherit parent's tokens")
}

// TestGetThreadTokenCount_ForkChainABC tests A→B→C fork chain inheritance.
func TestGetThreadTokenCount_ForkChainABC(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadA := uuid.New().String()
	threadB := uuid.New().String()
	threadC := uuid.New().String()
	cwA := uuid.New().String()
	cwB := uuid.New().String()

	forkBAtOrdinal := int64(2)
	forkCAtOrdinal := int64(4)

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Thread A (root)
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadA,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwA,
		ThreadID:  threadA,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Thread A messages: ordinals 1-3 with 10000, 20000, 30000 tokens
	for i := 1; i <= 3; i++ {
		tc := i * 10000
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: cwA,
			ThreadID:        threadA,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &tc,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Thread B (forked from A at ordinal 2)
	_, err = repo.CreateThread(ctx, &Thread{
		ID:              threadB,
		ChatID:          chatID,
		ParentThreadID:  &threadA,
		ForkAtMessageID: messageIDAtOrdinal(t, ctx, repo, cwA, forkBAtOrdinal),
		CreatedAt:       time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwB,
		ThreadID:  threadB,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Thread B messages: ordinals 4-5 with 40000, 50000 tokens
	for i := 4; i <= 5; i++ {
		tc := i * 10000
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: cwB,
			ThreadID:        threadB,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &tc,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Thread C (forked from B at ordinal 4)
	_, err = repo.CreateThread(ctx, &Thread{
		ID:              threadC,
		ChatID:          chatID,
		ParentThreadID:  &threadB,
		ForkAtMessageID: messageIDAtOrdinal(t, ctx, repo, cwB, forkCAtOrdinal),
		CreatedAt:       time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        uuid.New().String(),
		ThreadID:  threadC,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// No messages in C yet

	// Verify chain inheritance:
	// Thread A at ordinal 2: 20000 tokens
	tokensA, err := repo.GetThreadTokenCount(ctx, threadA, nil)
	require.NoError(t, err)
	require.Equal(t, int64(30000), tokensA, "Thread A should have 30k tokens (latest)")

	// Thread B: should have its own tokens (50000 from ordinal 5)
	tokensB, err := repo.GetThreadTokenCount(ctx, threadB, nil)
	require.NoError(t, err)
	require.Equal(t, int64(50000), tokensB, "Thread B should have 50k tokens")

	// Thread C (no messages): should inherit from B at ordinal 4 = 40000
	tokensC, err := repo.GetThreadTokenCount(ctx, threadC, nil)
	require.NoError(t, err)
	require.Equal(t, int64(40000), tokensC, "Thread C should inherit 40k tokens from B at fork point")
}

// TestGetThreadTokenCount_ForkAtUserMessage tests fork at user message (no token data).
// Should find prior assistant message with tokens.
func TestGetThreadTokenCount_ForkAtUserMessage(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	parentThread := uuid.New().String()
	childThread := uuid.New().String()
	parentCW := uuid.New().String()
	forkOrdinal := int64(3) // Fork at user message

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        parentThread,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        parentCW,
		ThreadID:  parentThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create messages: user (no tokens), assistant (tokens), user (no tokens)
	// Ordinal 1: user (no tokens)
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ContextWindowID: parentCW,
		ThreadID:        parentThread,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Ordinal 2: assistant (5000 tokens)
	tc := 5000
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         2,
		Seq:             2,
		ContextWindowID: parentCW,
		ThreadID:        parentThread,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		TokenCount:      &tc,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Ordinal 3: user (no tokens) - THIS IS WHERE WE FORK
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         3,
		Seq:             3,
		ContextWindowID: parentCW,
		ThreadID:        parentThread,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Ordinal 4: assistant (10000 tokens) - AFTER fork point
	tc2 := 10000
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         4,
		Seq:             4,
		ContextWindowID: parentCW,
		ThreadID:        parentThread,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		TokenCount:      &tc2,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Create child thread forked at ordinal 3 (user message)
	_, err = repo.CreateThread(ctx, &Thread{
		ID:              childThread,
		ChatID:          chatID,
		ParentThreadID:  &parentThread,
		ForkAtMessageID: messageIDAtOrdinal(t, ctx, repo, parentCW, forkOrdinal),
		CreatedAt:       time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        uuid.New().String(),
		ThreadID:  childThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Child should inherit from parent at ordinal 3 or before
	// The latest message with tokens at or before ordinal 3 is ordinal 2 (5000 tokens)
	tokens, err := repo.GetThreadTokenCount(ctx, childThread, nil)
	require.NoError(t, err)
	require.Equal(t, int64(5000), tokens, "fork at user message should inherit from prior assistant message")
}

// TestGetThreadTokenCount_SelfReferentialForkGuard tests that self-referential forks don't cause infinite loops.
func TestGetThreadTokenCount_SelfReferentialForkGuard(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread that references itself as parent (invalid but testing guard)
	_, err = repo.CreateThread(ctx, &Thread{
		ID:             threadID,
		ChatID:         chatID,
		ParentThreadID: &threadID, // Self-reference!
		CreatedAt:      time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// fork_at_message_id is a foreign key, so it needs a real row -- but the
	// self-referential guard fires before that row is ever looked up, so its
	// content doesn't matter for what this test exercises.
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ContextWindowID: cwID,
		ThreadID:        threadID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)
	msgs, err := repo.GetMessagesByContextWindow(ctx, cwID, nil)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	_, err = repo.UpdateThreadForkPoint(ctx, threadID, &msgs[0].ID)
	require.NoError(t, err)

	// Should return 0 without infinite loop
	tokens, err := repo.GetThreadTokenCount(ctx, threadID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), tokens, "self-referential fork should return 0")
}

// =============================================================================
// COMPACTION BOUNDARY TESTS
// =============================================================================

// TestGetThreadTokenCount_PostCompaction tests token counting after compaction.
func TestGetThreadTokenCount_PostCompaction(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create context window 0 (pre-compaction)
	cw0 := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cw0,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create pre-compaction messages with high token counts
	oldTokenCount := 150000
	for i := 1; i <= 3; i++ {
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: cw0,
			ThreadID:        threadID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &oldTokenCount,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Create context window 1 (post-compaction)
	cw1 := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cw1,
		ThreadID:  threadID,
		Sequence:  1,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create post-compaction messages with lower token counts (compaction summary is smaller)
	newTokenCount := 12000
	for i := 4; i <= 5; i++ {
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: cw1, // Post-compaction
			ThreadID:        threadID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &newTokenCount,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// GetThreadTokenCount should return tokens from the CURRENT context sequence (1)
	// not from the old pre-compaction context (0)
	tokens, err := repo.GetThreadTokenCount(ctx, threadID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(12000), tokens, "should return tokens from current context sequence (post-compaction)")
}

// TestGetThreadTokenCount_ForkFromPostCompactedParent tests forking from a parent that has compacted.
func TestGetThreadTokenCount_ForkFromPostCompactedParent(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	parentThread := uuid.New().String()
	childThread := uuid.New().String()
	parentCW0 := uuid.New().String()
	parentCW1 := uuid.New().String()
	forkOrdinal := int64(5)

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        parentThread,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Parent CW 0 (pre-compaction)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        parentCW0,
		ThreadID:  parentThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Pre-compaction messages (ordinals 1-3)
	oldTokenCount := 60000
	for i := 1; i <= 3; i++ {
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: parentCW0,
			ThreadID:        parentThread,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &oldTokenCount,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Parent CW 1 (post-compaction)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        parentCW1,
		ThreadID:  parentThread,
		Sequence:  1,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Post-compaction messages (ordinals 4-6) with different token counts
	tokenCounts := []int{12000, 17000, 22000}
	for i, total := range tokenCounts {
		tc := total
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i + 4),
			Seq:             int64(i + 4),
			ContextWindowID: parentCW1, // Post-compaction
			ThreadID:        parentThread,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &tc,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Create child thread forked at ordinal 5 (in post-compaction context)
	_, err = repo.CreateThread(ctx, &Thread{
		ID:              childThread,
		ChatID:          chatID,
		ParentThreadID:  &parentThread,
		ForkAtMessageID: messageIDAtOrdinal(t, ctx, repo, parentCW1, forkOrdinal), // Post-compaction CW
		CreatedAt:       time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        uuid.New().String(),
		ThreadID:  childThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Child should inherit parent's tokens from ordinal 5 in context_sequence 1
	tokens, err := repo.GetThreadTokenCount(ctx, childThread, nil)
	require.NoError(t, err)
	require.Equal(t, int64(17000), tokens, "fork from post-compacted parent should use new context sequence tokens")
}

// =============================================================================
// CROSS-CONVERSATION FORK TESTS
// =============================================================================

// TestGetThreadTokenCount_CrossConversationFork tests token inheritance across different conversations.
func TestGetThreadTokenCount_CrossConversationFork(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	parentChatID := uuid.New().String()
	childChatID := uuid.New().String()
	parentThread := uuid.New().String()
	childThread := uuid.New().String()
	parentCW := uuid.New().String()
	forkOrdinal := int64(3)

	// Create parent chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        parentChatID,
		Title:     "Parent Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create child chat (different conversation)
	err = repo.CreateChat(ctx, &Chat{
		ID:        childChatID,
		Title:     "Child Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent thread in parent conversation
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        parentThread,
		ChatID:    parentChatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        parentCW,
		ThreadID:  parentThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create parent messages with tokens
	tokenCount := 30000
	for i := 1; i <= 5; i++ {
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          parentChatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: parentCW,
			ThreadID:        parentThread,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			TokenCount:      &tokenCount,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Create child thread in DIFFERENT conversation, forked from parent
	_, err = repo.CreateThread(ctx, &Thread{
		ID:              childThread,
		ChatID:          childChatID, // Different conversation
		ParentThreadID:  &parentThread,
		ForkAtMessageID: messageIDAtOrdinal(t, ctx, repo, parentCW, forkOrdinal),
		CreatedAt:       time.Now(),
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        uuid.New().String(),
		ThreadID:  childThread,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Child (no local messages) should inherit from parent across conversations
	tokens, err := repo.GetThreadTokenCount(ctx, childThread, nil)
	require.NoError(t, err)
	require.Equal(t, int64(30000), tokens, "cross-conversation fork should inherit parent's tokens")
}

// =============================================================================
// EDGE CASES
// =============================================================================

// TestGetThreadTokenCount_EmptyThreadID tests that empty thread ID returns error.
func TestGetThreadTokenCount_EmptyThreadID(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := repo.GetThreadTokenCount(ctx, "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "thread ID cannot be empty")
}

// TestGetThreadTokenCount_NonExistentThread tests handling of non-existent thread.
func TestGetThreadTokenCount_NonExistentThread(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	// Thread doesn't exist - should return 0 without error (legacy fallback)
	tokens, err := repo.GetThreadTokenCount(ctx, uuid.New().String(), nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), tokens)
}

// TestGetThreadTokenCount_OnlyUserMessages tests thread with only user messages (no token data).
func TestGetThreadTokenCount_OnlyUserMessages(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	cwID := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create only user messages (no token data)
	for i := 1; i <= 5; i++ {
		err = repo.CreateMessage(ctx, &Message{
			ID:              uuid.New().String(),
			ChatID:          chatID,
			Ordinal:         int64(i),
			Seq:             int64(i),
			ContextWindowID: cwID,
			ThreadID:        threadID,
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
		require.NoError(t, err)
	}

	// Thread with only user messages should return 0 (no token data)
	tokens, err := repo.GetThreadTokenCount(ctx, threadID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), tokens, "thread with only user messages should return 0")
}

// TestGetThreadTokenCount_ZeroTokenValues tests messages with explicit zero token values.
func TestGetThreadTokenCount_ZeroTokenValues(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	threadID := uuid.New().String()

	// Create chat
	err := repo.CreateChat(ctx, &Chat{
		ID:        chatID,
		Title:     "Test Chat",
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create thread
	_, err = repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	cwID := uuid.New().String()
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Create message with zero token values (but non-nil pointers)
	zero := 0
	err = repo.CreateMessage(ctx, &Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ContextWindowID: cwID,
		ThreadID:        threadID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		TokenCount:      &zero,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// Should return 0 (the sum of all zeros)
	tokens, err := repo.GetThreadTokenCount(ctx, threadID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), tokens, "message with zero tokens should return 0")
}
