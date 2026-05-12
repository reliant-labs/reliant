// Copyright (c) 2025 Reliant Labs
// Tests for CW chain resolution using a REAL Postgres database.
// These tests catch bugs in the actual repository implementation that mocks would hide.
package threads

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// testContext holds shared test infrastructure
type testContext struct {
	t       *testing.T
	ctx     context.Context
	repo    *db.Repo
	svc     *Service
	cleanup func()
}

func setupTest(t *testing.T) *testContext {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	return &testContext{
		t:       t,
		ctx:     context.Background(),
		repo:    repo,
		svc:     NewService(repo),
		cleanup: cleanup,
	}
}

// createChat creates a test chat and returns its ID
func (tc *testContext) createChat(title string) string {
	tc.t.Helper()
	chatID := uuid.New().String()
	err := tc.repo.CreateChat(tc.ctx, &db.Chat{
		ID:        chatID,
		Title:     title,
		ProjectID: "test-project",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(tc.t, err)
	return chatID
}

// createThread creates a thread and returns it
func (tc *testContext) createThread(chatID string, parentThreadID *string) *db.Thread {
	tc.t.Helper()
	thread := &db.Thread{
		ID:             uuid.New().String(),
		ConversationID: chatID,
		ParentThreadID: parentThreadID,
		CreatedAt:      time.Now(),
	}
	created, err := tc.repo.CreateThread(tc.ctx, thread)
	require.NoError(tc.t, err)
	return created
}

// createContextWindow creates a context window and returns it
func (tc *testContext) createContextWindow(threadID string, sequence int, parentCWID *string, forkAtOrdinal *int64) *db.ContextWindow {
	tc.t.Helper()
	cw := &db.ContextWindow{
		ID:                    uuid.New().String(),
		ThreadID:              threadID,
		Sequence:              sequence,
		ParentContextWindowID: parentCWID,
		ForkAtOrdinal:         forkAtOrdinal,
		CreatedAt:             time.Now(),
	}
	created, err := tc.repo.CreateContextWindow(tc.ctx, cw)
	require.NoError(tc.t, err)
	return created
}

// createMessage creates a message in a context window
func (tc *testContext) createMessage(chatID, threadID, cwID string, ordinal int64, role reliantv1.MessageRole) *db.Message {
	tc.t.Helper()
	msg := &db.Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Ordinal:         ordinal,
		Role:            role,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	err := tc.repo.CreateMessage(tc.ctx, msg)
	require.NoError(tc.t, err)
	return msg
}

// TestRealDB_SimpleCW tests resolution from a simple context window with no parent.
func TestRealDB_SimpleCW(t *testing.T) {
	tc := setupTest(t)
	defer tc.cleanup()

	chatID := tc.createChat("Test Chat")
	thread := tc.createThread(chatID, nil)
	cw := tc.createContextWindow(thread.ID, 0, nil, nil)

	// Add messages
	tc.createMessage(chatID, thread.ID, cw.ID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER)
	tc.createMessage(chatID, thread.ID, cw.ID, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	tc.createMessage(chatID, thread.ID, cw.ID, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER)

	// Resolve messages
	msgs, err := tc.svc.ResolveMessagesFromCW(tc.ctx, cw.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 3, "should return all 3 messages")

	// Verify order
	require.Equal(t, int64(1), msgs[0].Ordinal)
	require.Equal(t, int64(2), msgs[1].Ordinal)
	require.Equal(t, int64(3), msgs[2].Ordinal)
}

// TestRealDB_BranchChain tests a fork/branch relationship.
// CWA (parent) -> CWB (child, forked at ordinal 2)
func TestRealDB_BranchChain(t *testing.T) {
	tc := setupTest(t)
	defer tc.cleanup()

	// Parent chat and thread
	chatA := tc.createChat("Chat A")
	threadA := tc.createThread(chatA, nil)
	cwA := tc.createContextWindow(threadA.ID, 0, nil, nil)

	// Add messages to parent
	tc.createMessage(chatA, threadA.ID, cwA.ID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER)      // should inherit
	tc.createMessage(chatA, threadA.ID, cwA.ID, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT) // should inherit
	tc.createMessage(chatA, threadA.ID, cwA.ID, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER)      // should NOT inherit (beyond fork point)

	// Child chat and thread (forked at ordinal 2)
	chatB := tc.createChat("Chat B")
	threadB := tc.createThread(chatB, &threadA.ID)
	forkOrdinal := int64(2)
	cwB := tc.createContextWindow(threadB.ID, 0, &cwA.ID, &forkOrdinal)

	// Add message to child
	tc.createMessage(chatB, threadB.ID, cwB.ID, 4, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	// Resolve from child CW
	msgs, err := tc.svc.ResolveMessagesFromCW(tc.ctx, cwB.ID)
	require.NoError(t, err)

	// Should have: ordinals 1, 2 (inherited, filtered), 4 (local)
	require.Len(t, msgs, 3, "should have 3 messages (2 inherited + 1 local)")

	ordinals := make([]int64, len(msgs))
	for i, m := range msgs {
		ordinals[i] = m.Ordinal
	}
	require.Equal(t, []int64{1, 2, 4}, ordinals, "should have correct ordinals")
}

// TestRealDB_NestedBranches tests a multi-level fork chain: A → B → C
// This is the critical "branch of branch" scenario.
func TestRealDB_NestedBranches(t *testing.T) {
	tc := setupTest(t)
	defer tc.cleanup()

	// Thread A (root)
	chatA := tc.createChat("Chat A - Root")
	threadA := tc.createThread(chatA, nil)
	cwA := tc.createContextWindow(threadA.ID, 0, nil, nil)

	tc.createMessage(chatA, threadA.ID, cwA.ID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER)      // inherit to B, C
	tc.createMessage(chatA, threadA.ID, cwA.ID, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT) // inherit to B, C
	tc.createMessage(chatA, threadA.ID, cwA.ID, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER)      // NOT inherit (filtered by B's fork at 2)

	// Thread B forks from A at ordinal 2
	chatB := tc.createChat("Chat B - Branch 1")
	threadB := tc.createThread(chatB, &threadA.ID)
	forkOrdinalB := int64(2)
	cwB := tc.createContextWindow(threadB.ID, 0, &cwA.ID, &forkOrdinalB)

	tc.createMessage(chatB, threadB.ID, cwB.ID, 4, reliantv1.MessageRole_MESSAGE_ROLE_USER)      // inherit to C
	tc.createMessage(chatB, threadB.ID, cwB.ID, 5, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT) // inherit to C
	tc.createMessage(chatB, threadB.ID, cwB.ID, 6, reliantv1.MessageRole_MESSAGE_ROLE_USER)      // NOT inherit (filtered by C's fork at 5)

	// Thread C forks from B at ordinal 5
	chatC := tc.createChat("Chat C - Branch 2")
	threadC := tc.createThread(chatC, &threadB.ID)
	forkOrdinalC := int64(5)
	cwC := tc.createContextWindow(threadC.ID, 0, &cwB.ID, &forkOrdinalC)

	tc.createMessage(chatC, threadC.ID, cwC.ID, 7, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT) // local to C

	// Resolve from C
	msgs, err := tc.svc.ResolveMessagesFromCW(tc.ctx, cwC.ID)
	require.NoError(t, err)

	// Should have: 1, 2 (from A), 4, 5 (from B up to fork point), 7 (local)
	// Should NOT have: 3 (filtered by B's fork), 6 (filtered by C's fork)
	expectedOrdinals := []int64{1, 2, 4, 5, 7}
	require.Len(t, msgs, len(expectedOrdinals), "should have %d messages", len(expectedOrdinals))

	actualOrdinals := make([]int64, len(msgs))
	for i, m := range msgs {
		actualOrdinals[i] = m.Ordinal
	}
	require.Equal(t, expectedOrdinals, actualOrdinals, "should have correct ordinals in order")

	// Verify context window IDs to ensure proper inheritance
	require.Equal(t, cwA.ID, msgs[0].ContextWindowID, "msg 1 should be from CWA")
	require.Equal(t, cwA.ID, msgs[1].ContextWindowID, "msg 2 should be from CWA")
	require.Equal(t, cwB.ID, msgs[2].ContextWindowID, "msg 4 should be from CWB")
	require.Equal(t, cwB.ID, msgs[3].ContextWindowID, "msg 5 should be from CWB")
	require.Equal(t, cwC.ID, msgs[4].ContextWindowID, "msg 7 should be from CWC")
}

// TestRealDB_CompactionStopsTraversal tests that compaction summary stops parent traversal.
func TestRealDB_CompactionStopsTraversal(t *testing.T) {
	tc := setupTest(t)
	defer tc.cleanup()

	chatID := tc.createChat("Test Chat")
	thread := tc.createThread(chatID, nil)

	// CW1 with messages
	cw1 := tc.createContextWindow(thread.ID, 0, nil, nil)
	tc.createMessage(chatID, thread.ID, cw1.ID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER)
	tc.createMessage(chatID, thread.ID, cw1.ID, 2, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	// CW2 with compaction summary
	cw2 := tc.createContextWindow(thread.ID, 1, &cw1.ID, nil)
	summaryMsg := tc.createMessage(chatID, thread.ID, cw2.ID, 3, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM)
	tc.createMessage(chatID, thread.ID, cw2.ID, 4, reliantv1.MessageRole_MESSAGE_ROLE_USER)

	// Set compaction summary
	_, err := tc.repo.SetCompactionSummaryMessage(tc.ctx, cw2.ID, summaryMsg.ID)
	require.NoError(t, err)

	// Resolve from CW2
	msgs, err := tc.svc.ResolveMessagesFromCW(tc.ctx, cw2.ID)
	require.NoError(t, err)

	// Should only have messages from CW2 (compaction stops traversal)
	require.Len(t, msgs, 2, "should only have 2 messages from CW2")

	for _, msg := range msgs {
		require.Equal(t, cw2.ID, msg.ContextWindowID, "all messages should be from CW2")
	}
}

// TestRealDB_BranchFromCompactedCW tests branching from a CW that has a compaction summary.
func TestRealDB_BranchFromCompactedCW(t *testing.T) {
	tc := setupTest(t)
	defer tc.cleanup()

	// Thread A with compaction
	chatA := tc.createChat("Chat A")
	threadA := tc.createThread(chatA, nil)

	cwA0 := tc.createContextWindow(threadA.ID, 0, nil, nil)
	tc.createMessage(chatA, threadA.ID, cwA0.ID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER)

	cwA1 := tc.createContextWindow(threadA.ID, 1, &cwA0.ID, nil)
	summaryMsg := tc.createMessage(chatA, threadA.ID, cwA1.ID, 2, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM)
	tc.createMessage(chatA, threadA.ID, cwA1.ID, 3, reliantv1.MessageRole_MESSAGE_ROLE_USER)

	_, err := tc.repo.SetCompactionSummaryMessage(tc.ctx, cwA1.ID, summaryMsg.ID)
	require.NoError(t, err)

	// Thread B forks from A's compacted CW
	chatB := tc.createChat("Chat B")
	threadB := tc.createThread(chatB, &threadA.ID)
	forkOrdinal := int64(3)
	cwB := tc.createContextWindow(threadB.ID, 1, &cwA1.ID, &forkOrdinal) // inherits sequence

	tc.createMessage(chatB, threadB.ID, cwB.ID, 4, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	// Resolve from B
	msgs, err := tc.svc.ResolveMessagesFromCW(tc.ctx, cwB.ID)
	require.NoError(t, err)

	// Should have: summary (2), user (3) from parent (compaction stops at A1), local (4)
	require.Len(t, msgs, 3, "should have 3 messages")

	// First two from parent's compacted CW, last from local
	require.Equal(t, cwA1.ID, msgs[0].ContextWindowID)
	require.Equal(t, cwA1.ID, msgs[1].ContextWindowID)
	require.Equal(t, cwB.ID, msgs[2].ContextWindowID)
}
