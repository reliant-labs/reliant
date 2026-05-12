// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test Helpers - Seeding Infrastructure
// =============================================================================

type testEnv struct {
	t       *testing.T
	repo    db.Repository
	service *Service
	ctx     context.Context
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	repo := db.NewTestRepo(t)

	// Create test project
	err := repo.CreateProject(context.Background(), &db.Project{
		ID:        "test-project",
		Name:      "Test Project",
		Path:      "/tmp/test",
		UserID:    "test-user",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	t.Cleanup(func() { repo.Close() })

	return &testEnv{
		t:       t,
		repo:    repo,
		service: NewService(repo),
		ctx:     context.Background(),
	}
}

// createChat creates a chat with initial thread and context window, returns IDs
func (e *testEnv) createChat(title string) (chatID, threadID, cwID string) {
	e.t.Helper()
	chatID = uuid.New().String()
	threadID = chatID // Thread ID = Chat ID pattern
	cwID = fmt.Sprintf("%s:%s:0", chatID, threadID)

	err := e.repo.CreateChat(e.ctx, &db.Chat{
		ID:        chatID,
		UserID:    "test-user",
		Title:     title,
		ProjectID: "test-project",
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(e.t, err)

	_, err = e.repo.CreateThread(e.ctx, &db.Thread{
		ID:             threadID,
		ConversationID: chatID,
		CreatedAt:      time.Now(),
	})
	require.NoError(e.t, err)

	_, err = e.repo.CreateContextWindow(e.ctx, &db.ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: time.Now(),
	})
	require.NoError(e.t, err)

	return chatID, threadID, cwID
}

// addMessage adds a message to a thread/CW at the given ordinal
func (e *testEnv) addMessage(chatID, threadID, cwID string, ordinal int64, role reliantv1.MessageRole) *db.Message {
	e.t.Helper()
	msg := &db.Message{
		ID:              uuid.New().String(),
		ChatID:          chatID,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Ordinal:         ordinal,
		Role:            role,
		CreatedAt:       time.Now(),
	}
	err := e.repo.CreateMessage(e.ctx, msg)
	require.NoError(e.t, err)

	// Add a text block so message isn't empty
	err = e.repo.CreateContentBlock(e.ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: msg.ID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   ptr.Of(fmt.Sprintf("Message ordinal %d", ordinal)),
		Position:  0,
	})
	require.NoError(e.t, err)

	return msg
}

// addMessages adds multiple alternating user/assistant messages starting at ordinal
func (e *testEnv) addMessages(chatID, threadID, cwID string, startOrdinal int64, count int) []*db.Message {
	e.t.Helper()
	msgs := make([]*db.Message, count)
	for i := 0; i < count; i++ {
		role := reliantv1.MessageRole_MESSAGE_ROLE_USER
		if i%2 == 1 {
			role = reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT
		}
		msgs[i] = e.addMessage(chatID, threadID, cwID, startOrdinal+int64(i), role)
	}
	return msgs
}

// compact creates a new context window with compaction summary
func (e *testEnv) compact(chatID, threadID string) *db.ContextWindow {
	e.t.Helper()

	// Get current CW to link as parent
	currentCW, err := e.repo.GetLatestContextWindow(e.ctx, threadID)
	require.NoError(e.t, err)

	newSeq := currentCW.Sequence + 1
	newCWID := fmt.Sprintf("%s:%s:%d", chatID, threadID, newSeq)

	// Get max ordinal for summary message
	msgs, err := e.repo.GetMessagesByContextWindow(e.ctx, currentCW.ID, nil)
	require.NoError(e.t, err)
	maxOrdinal := int64(0)
	for _, m := range msgs {
		if m.Ordinal > maxOrdinal {
			maxOrdinal = m.Ordinal
		}
	}

	// Create new CW FIRST (without summary message ID - we'll set it after)
	newCW := &db.ContextWindow{
		ID:                    newCWID,
		ThreadID:              threadID,
		Sequence:              newSeq,
		ParentContextWindowID: &currentCW.ID,
		CreatedAt:             time.Now(),
	}
	_, err = e.repo.CreateContextWindow(e.ctx, newCW)
	require.NoError(e.t, err)

	// Now create summary message in the new CW
	summaryMsg := e.addMessage(chatID, threadID, newCWID, maxOrdinal+1, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM)

	// Update CW with summary message ID
	cw, err := e.repo.SetCompactionSummaryMessage(e.ctx, newCWID, summaryMsg.ID)
	require.NoError(e.t, err)
	return cw
}

// branch creates a branched thread forking from parentThread at forkAtOrdinal
func (e *testEnv) branch(parentChatID, parentThreadID, parentCWID string, forkAtOrdinal int64, branchTitle string) (childChatID, childThreadID, childCWID string) {
	e.t.Helper()

	childChatID = uuid.New().String()
	childThreadID = childChatID
	childCWID = fmt.Sprintf("%s:%s:0", childChatID, childThreadID)

	// Create child chat
	err := e.repo.CreateChat(e.ctx, &db.Chat{
		ID:        childChatID,
		UserID:    "test-user",
		Title:     branchTitle,
		ProjectID: "test-project",
		State:     db.ChatStateIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	require.NoError(e.t, err)

	// Create child thread pointing to parent
	_, err = e.repo.CreateThread(e.ctx, &db.Thread{
		ID:                    childThreadID,
		ConversationID:        childChatID,
		ParentThreadID:        &parentThreadID,
		ForkAtOrdinal:         &forkAtOrdinal,
		ForkAtContextWindowID: &parentCWID,
		CreatedAt:             time.Now(),
	})
	require.NoError(e.t, err)

	// Get parent CW sequence for inheritance
	parentCW, err := e.repo.GetContextWindow(e.ctx, parentCWID)
	require.NoError(e.t, err)

	// Create child context window with link to parent
	_, err = e.repo.CreateContextWindow(e.ctx, &db.ContextWindow{
		ID:                    childCWID,
		ThreadID:              childThreadID,
		Sequence:              parentCW.Sequence, // Inherit sequence
		ParentContextWindowID: &parentCWID,
		ForkAtOrdinal:         &forkAtOrdinal,
		CreatedAt:             time.Now(),
	})
	require.NoError(e.t, err)

	return childChatID, childThreadID, childCWID
}

// verifyMessages checks that messages have expected ordinals and are normalized to expectedThreadID
func (e *testEnv) verifyMessages(msgs []*db.Message, expectedOrdinals []int64, expectedThreadID string) {
	e.t.Helper()

	// Check count
	require.Len(e.t, msgs, len(expectedOrdinals),
		"message count should match expected ordinals")

	// Check each message
	for i, msg := range msgs {
		// Verify ordinal
		assert.Equal(e.t, expectedOrdinals[i], msg.Ordinal,
			"msg[%d] should have ordinal %d but got %d",
			i, expectedOrdinals[i], msg.Ordinal)

		// Verify ThreadID normalization
		assert.Equal(e.t, expectedThreadID, msg.ThreadID,
			"msg[%d] ordinal=%d should have ThreadID=%s but got %s",
			i, msg.Ordinal, expectedThreadID, msg.ThreadID)
	}
}

// =============================================================================
// Branching Scenario Tests
// =============================================================================

// TestBranchingScenarios tests various message resolution scenarios with branching
func TestBranchingScenarios(t *testing.T) {
	t.Run("simple_branch_inherits_parent_messages", func(t *testing.T) {
		env := newTestEnv(t)

		// Create parent chat with messages
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent Chat")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 4)

		// Branch at ordinal 2 (inherit ordinals 0, 1, 2)
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, parentCWID, 2, "Child Branch")
		env.addMessages(childChatID, childThreadID, childCWID, 4, 2)

		// Load messages from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should have 3 inherited + 2 local = 5
		assert.Len(t, msgs, 5)

		// Verify order
		for i, msg := range msgs {
			t.Logf("msg[%d]: ordinal=%d, threadID=%s", i, msg.Ordinal, msg.ThreadID)
		}
	})

	t.Run("nested_branch_a_to_b_to_c", func(t *testing.T) {
		env := newTestEnv(t)

		// Create A with messages
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 2)

		// B branches from A at ordinal 1
		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 1, "Chat B")
		env.addMessages(chatB, threadB, cwB, 2, 2)

		// C branches from B at ordinal 3
		chatC, threadC, cwC := env.branch(chatB, threadB, cwB, 3, "Chat C")
		env.addMessages(chatC, threadC, cwC, 4, 2)

		// Load messages from C - should get full chain
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadC)
		require.NoError(t, err)

		// 2 from A (0,1) + 2 from B (2,3) + 2 from C (4,5) = 6
		assert.Len(t, msgs, 6)
	})

	t.Run("branch_respects_compaction_in_parent", func(t *testing.T) {
		env := newTestEnv(t)

		// Create parent with messages
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 2)

		// Compact parent
		compactedCW := env.compact(parentChatID, parentThreadID)
		env.addMessages(parentChatID, parentThreadID, compactedCW.ID, 3, 2)

		// Branch from compacted CW
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, compactedCW.ID, 4, "Child After Compact")
		env.addMessages(childChatID, childThreadID, childCWID, 5, 1)

		// Load messages from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should have: summary + 2 post-compact + 1 child = 4
		// Should NOT have pre-compaction messages
		assert.Len(t, msgs, 4)

		// Verify no pre-compaction ordinals
		for _, msg := range msgs {
			assert.GreaterOrEqual(t, msg.Ordinal, int64(2), "should not include pre-compaction messages")
		}
	})

	// =============================================================================
	// BRANCHING DEPTH TESTS
	// =============================================================================

	t.Run("deep_nesting_five_levels", func(t *testing.T) {
		env := newTestEnv(t)

		// A → B → C → D → E (5 levels deep)
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 2)

		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 1, "Chat B")
		env.addMessages(chatB, threadB, cwB, 2, 2)

		chatC, threadC, cwC := env.branch(chatB, threadB, cwB, 3, "Chat C")
		env.addMessages(chatC, threadC, cwC, 4, 2)

		chatD, threadD, cwD := env.branch(chatC, threadC, cwC, 5, "Chat D")
		env.addMessages(chatD, threadD, cwD, 6, 2)

		chatE, threadE, cwE := env.branch(chatD, threadD, cwD, 7, "Chat E")
		env.addMessages(chatE, threadE, cwE, 8, 2)

		// Load from E - should get full chain
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadE)
		require.NoError(t, err)

		// 2 from A (0,1) + 2 from B (2,3) + 2 from C (4,5) + 2 from D (6,7) + 2 from E (8,9) = 10
		assert.Len(t, msgs, 10)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, threadE)
	})

	t.Run("multiple_branches_from_same_point", func(t *testing.T) {
		env := newTestEnv(t)

		// A creates base
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 3)

		// B1 branches from A at ordinal 1
		chatB1, threadB1, cwB1 := env.branch(chatA, threadA, cwA, 1, "Chat B1")
		env.addMessages(chatB1, threadB1, cwB1, 2, 2)

		// B2 also branches from A at ordinal 1 (same point)
		chatB2, threadB2, cwB2 := env.branch(chatA, threadA, cwA, 1, "Chat B2")
		env.addMessages(chatB2, threadB2, cwB2, 2, 3)

		// Load from B1
		msgsB1, err := env.service.LoadCurrentMessages(env.ctx, threadB1)
		require.NoError(t, err)
		assert.Len(t, msgsB1, 4) // 2 from A + 2 from B1
		env.verifyMessages(msgsB1, []int64{0, 1, 2, 3}, threadB1)

		// Load from B2
		msgsB2, err := env.service.LoadCurrentMessages(env.ctx, threadB2)
		require.NoError(t, err)
		assert.Len(t, msgsB2, 5) // 2 from A + 3 from B2
		env.verifyMessages(msgsB2, []int64{0, 1, 2, 3, 4}, threadB2)

		// Verify B1 and B2 are independent
		for _, msg := range msgsB1 {
			assert.Equal(t, threadB1, msg.ThreadID)
		}
		for _, msg := range msgsB2 {
			assert.Equal(t, threadB2, msg.ThreadID)
		}
	})

	t.Run("very_long_chain_10_plus_branches", func(t *testing.T) {
		env := newTestEnv(t)

		// Create a chain of 12 branches: A → B → C → ... → L
		type chainNode struct {
			chatID   string
			threadID string
			cwID     string
		}

		// Start with A
		chain := make([]chainNode, 12)
		chain[0].chatID, chain[0].threadID, chain[0].cwID = env.createChat("Chat A")
		env.addMessages(chain[0].chatID, chain[0].threadID, chain[0].cwID, 0, 2)

		// Build chain
		for i := 1; i < 12; i++ {
			prev := chain[i-1]
			forkOrdinal := int64(i*2 - 1)
			chain[i].chatID, chain[i].threadID, chain[i].cwID = env.branch(
				prev.chatID, prev.threadID, prev.cwID,
				forkOrdinal,
				fmt.Sprintf("Chat %c", 'A'+i),
			)
			env.addMessages(chain[i].chatID, chain[i].threadID, chain[i].cwID, int64(i*2), 2)
		}

		// Load from last node (L)
		lastThread := chain[11].threadID
		msgs, err := env.service.LoadCurrentMessages(env.ctx, lastThread)
		require.NoError(t, err)

		// Each node contributes 2 messages = 24 total
		assert.Len(t, msgs, 24)

		// Verify all messages normalized to last thread
		for _, msg := range msgs {
			assert.Equal(t, lastThread, msg.ThreadID)
		}

		// Verify ordinals are sequential
		for i, msg := range msgs {
			assert.Equal(t, int64(i), msg.Ordinal)
		}
	})

	// =============================================================================
	// BRANCH POINT LOCATION TESTS
	// =============================================================================

	t.Run("branch_from_first_message_ordinal_0", func(t *testing.T) {
		env := newTestEnv(t)

		// Create parent with messages
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 4)

		// Branch at ordinal 0 (include only first message)
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, parentCWID, 0, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 1, 2)

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should have: 1 from parent (ordinal 0) + 2 from child = 3
		assert.Len(t, msgs, 3)
		env.verifyMessages(msgs, []int64{0, 1, 2}, childThreadID)
	})

	t.Run("branch_from_last_message", func(t *testing.T) {
		env := newTestEnv(t)

		// Create parent with messages
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 5)

		// Branch at last ordinal (4)
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, parentCWID, 4, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 5, 2)

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should have all 5 from parent + 2 from child = 7
		assert.Len(t, msgs, 7)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3, 4, 5, 6}, childThreadID)
	})

	t.Run("branch_from_only_message_in_thread", func(t *testing.T) {
		env := newTestEnv(t)

		// Create parent with only 1 message
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessage(parentChatID, parentThreadID, parentCWID, 0, reliantv1.MessageRole_MESSAGE_ROLE_USER)

		// Branch from the only message
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, parentCWID, 0, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 1, 3)

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should have 1 from parent + 3 from child = 4
		assert.Len(t, msgs, 4)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3}, childThreadID)
	})

	// =============================================================================
	// COMPACTION INTERACTION TESTS
	// =============================================================================

	t.Run("branch_from_message_before_compaction_cw_seq_0", func(t *testing.T) {
		env := newTestEnv(t)

		// Parent creates messages in CW seq 0
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 3)

		// Branch from CW seq 0, ordinal 1
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, parentCWID, 1, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 2, 2)

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// 2 from parent (0,1) + 2 from child (2,3) = 4
		assert.Len(t, msgs, 4)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3}, childThreadID)
	})

	t.Run("branch_from_message_after_compaction_cw_seq_1_plus", func(t *testing.T) {
		env := newTestEnv(t)

		// Parent creates messages then compacts
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 3)     // ordinals 0, 1, 2
		compactedCW := env.compact(parentChatID, parentThreadID)            // summary at ordinal 3
		env.addMessages(parentChatID, parentThreadID, compactedCW.ID, 4, 3) // ordinals 4, 5, 6

		// Branch from post-compaction CW at ordinal 5 (include up to ordinal 5)
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, compactedCW.ID, 5, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 6, 2) // ordinals 6, 7

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// summary(3) + parent msgs(4,5) + child msgs(6,7) = 5 messages
		assert.Len(t, msgs, 5)
		env.verifyMessages(msgs, []int64{3, 4, 5, 6, 7}, childThreadID)
	})

	t.Run("branch_from_compaction_summary_message_itself", func(t *testing.T) {
		env := newTestEnv(t)

		// Parent compacts
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 3)
		compactedCW := env.compact(parentChatID, parentThreadID)

		// The summary message is at ordinal 3
		// Branch exactly at the summary message ordinal
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, compactedCW.ID, 3, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 4, 2)

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should include summary + child messages
		assert.Len(t, msgs, 3)
		env.verifyMessages(msgs, []int64{3, 4, 5}, childThreadID)
	})

	t.Run("parent_compacts_after_child_branches", func(t *testing.T) {
		env := newTestEnv(t)

		// Parent creates messages
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 4)

		// Child branches at ordinal 2
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, parentCWID, 2, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 3, 2)

		// NOW parent compacts (after child already branched)
		env.compact(parentChatID, parentThreadID)

		// Load from child - should be unaffected by parent's later compaction
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Child should still see original messages: 3 from parent (0,1,2) + 2 from child (3,4) = 5
		assert.Len(t, msgs, 5)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3, 4}, childThreadID)
	})

	t.Run("multiple_compactions_in_parent_branch_from_middle", func(t *testing.T) {
		env := newTestEnv(t)

		// Parent creates initial messages
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 3) // ordinals 0, 1, 2

		// First compaction
		cw1 := env.compact(parentChatID, parentThreadID)            // summary at ordinal 3
		env.addMessages(parentChatID, parentThreadID, cw1.ID, 4, 3) // ordinals 4, 5, 6

		// Second compaction
		cw2 := env.compact(parentChatID, parentThreadID)            // summary at ordinal 7
		env.addMessages(parentChatID, parentThreadID, cw2.ID, 8, 2) // ordinals 8, 9

		// Branch from middle (first compacted CW) at ordinal 5
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, cw1.ID, 5, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 6, 2) // ordinals 6, 7

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should have: first summary (3) + msgs from cw1 up to fork (4,5) + child msgs (6,7) = 5
		assert.Len(t, msgs, 5)
		env.verifyMessages(msgs, []int64{3, 4, 5, 6, 7}, childThreadID)
	})

	t.Run("branch_then_compact_branch_then_branch_from_that", func(t *testing.T) {
		env := newTestEnv(t)

		// A creates messages
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 3)

		// B branches from A
		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 2, "Chat B")
		env.addMessages(chatB, threadB, cwB, 3, 3)

		// B compacts
		cwBCompacted := env.compact(chatB, threadB)
		env.addMessages(chatB, threadB, cwBCompacted.ID, 7, 2)

		// C branches from B's compacted CW
		chatC, threadC, cwC := env.branch(chatB, threadB, cwBCompacted.ID, 8, "Chat C")
		env.addMessages(chatC, threadC, cwC, 9, 2)

		// Load from C
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadC)
		require.NoError(t, err)

		// B's summary (6) + B's post-compact msgs (7,8) + C's msgs (9,10) = 5
		assert.Len(t, msgs, 5)
		env.verifyMessages(msgs, []int64{6, 7, 8, 9, 10}, threadC)
	})

	// =============================================================================
	// FORK CHAIN + COMPACTION COMBINATIONS
	// =============================================================================

	t.Run("a_compacts_then_b_branches_from_pre_compaction_msg", func(t *testing.T) {
		env := newTestEnv(t)

		// A creates messages
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 3)

		// A compacts
		cwACompacted := env.compact(chatA, threadA)
		env.addMessages(chatA, threadA, cwACompacted.ID, 4, 2)

		// B branches from A's ORIGINAL CW (pre-compaction) at ordinal 1
		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 1, "Chat B")
		env.addMessages(chatB, threadB, cwB, 2, 2)

		// Load from B
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadB)
		require.NoError(t, err)

		// B should see original messages from cwA: (0,1) + B's (2,3) = 4
		assert.Len(t, msgs, 4)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3}, threadB)
	})

	t.Run("a_compacts_then_b_branches_from_post_compaction_msg", func(t *testing.T) {
		env := newTestEnv(t)

		// A creates and compacts
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 3)
		cwACompacted := env.compact(chatA, threadA)
		env.addMessages(chatA, threadA, cwACompacted.ID, 4, 3)

		// B branches from compacted CW at ordinal 5
		chatB, threadB, cwB := env.branch(chatA, threadA, cwACompacted.ID, 5, "Chat B")
		env.addMessages(chatB, threadB, cwB, 6, 2)

		// Load from B
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadB)
		require.NoError(t, err)

		// summary(3) + A's post-compact (4,5) + B's (6,7) = 5
		assert.Len(t, msgs, 5)
		env.verifyMessages(msgs, []int64{3, 4, 5, 6, 7}, threadB)
	})

	t.Run("a_to_b_to_c_then_a_compacts_verify_c_still_resolves", func(t *testing.T) {
		env := newTestEnv(t)

		// A creates messages
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 3)

		// B branches from A
		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 2, "Chat B")
		env.addMessages(chatB, threadB, cwB, 3, 2)

		// C branches from B
		chatC, threadC, cwC := env.branch(chatB, threadB, cwB, 4, "Chat C")
		env.addMessages(chatC, threadC, cwC, 5, 2)

		// NOW A compacts (after B and C already exist)
		env.compact(chatA, threadA)

		// Load from C - should still resolve correctly
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadC)
		require.NoError(t, err)

		// A's msgs (0,1,2) + B's (3,4) + C's (5,6) = 7
		assert.Len(t, msgs, 7)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3, 4, 5, 6}, threadC)
	})

	t.Run("a_to_b_then_b_compacts_then_c_branches_from_b_pre_compaction", func(t *testing.T) {
		env := newTestEnv(t)

		// A creates messages
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 2)

		// B branches from A
		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 1, "Chat B")
		env.addMessages(chatB, threadB, cwB, 2, 3)

		// B compacts
		env.compact(chatB, threadB)

		// C branches from B's ORIGINAL CW (before compaction)
		chatC, threadC, cwC := env.branch(chatB, threadB, cwB, 3, "Chat C")
		env.addMessages(chatC, threadC, cwC, 4, 2)

		// Load from C
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadC)
		require.NoError(t, err)

		// A's msgs (0,1) + B's msgs up to fork (2,3) + C's (4,5) = 6
		assert.Len(t, msgs, 6)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3, 4, 5}, threadC)
	})

	// =============================================================================
	// VISUAL THREAD NORMALIZATION TESTS
	// =============================================================================

	t.Run("verify_all_messages_normalized_to_requesting_thread", func(t *testing.T) {
		env := newTestEnv(t)

		// Create chain A → B → C
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 2)

		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 1, "Chat B")
		env.addMessages(chatB, threadB, cwB, 2, 2)

		chatC, threadC, cwC := env.branch(chatB, threadB, cwB, 3, "Chat C")
		env.addMessages(chatC, threadC, cwC, 4, 2)

		// Load from C
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadC)
		require.NoError(t, err)

		// ALL messages must have ThreadID = threadC
		for i, msg := range msgs {
			assert.Equal(t, threadC, msg.ThreadID,
				"msg[%d] ordinal=%d should have ThreadID=%s but got %s",
				i, msg.Ordinal, threadC, msg.ThreadID)
		}
	})

	t.Run("verify_messages_in_ordinal_order", func(t *testing.T) {
		env := newTestEnv(t)

		// Create chain with gaps in ordinals
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 3)

		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 1, "Chat B")
		env.addMessages(chatB, threadB, cwB, 2, 2)

		// Load from B
		msgs, err := env.service.LoadCurrentMessages(env.ctx, threadB)
		require.NoError(t, err)

		// Verify messages are strictly increasing by ordinal
		for i := 1; i < len(msgs); i++ {
			assert.Greater(t, msgs[i].Ordinal, msgs[i-1].Ordinal,
				"messages should be in ordinal order")
		}
	})

	// =============================================================================
	// EDGE CASE TESTS
	// =============================================================================

	t.Run("empty_branch_no_messages_added_after_branching", func(t *testing.T) {
		env := newTestEnv(t)

		// Parent with messages
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessages(parentChatID, parentThreadID, parentCWID, 0, 4)

		// Branch but don't add any messages
		_, childThreadID, _ := env.branch(parentChatID, parentThreadID, parentCWID, 2, "Empty Child")

		// Load from empty child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// Should only have inherited messages (0,1,2)
		assert.Len(t, msgs, 3)
		env.verifyMessages(msgs, []int64{0, 1, 2}, childThreadID)
	})

	t.Run("single_message_parent", func(t *testing.T) {
		env := newTestEnv(t)

		// Parent with only 1 message
		parentChatID, parentThreadID, parentCWID := env.createChat("Parent")
		env.addMessage(parentChatID, parentThreadID, parentCWID, 0, reliantv1.MessageRole_MESSAGE_ROLE_USER)

		// Branch and add messages
		childChatID, childThreadID, childCWID := env.branch(parentChatID, parentThreadID, parentCWID, 0, "Child")
		env.addMessages(childChatID, childThreadID, childCWID, 1, 4)

		// Load from child
		msgs, err := env.service.LoadCurrentMessages(env.ctx, childThreadID)
		require.NoError(t, err)

		// 1 from parent + 4 from child = 5
		assert.Len(t, msgs, 5)
		env.verifyMessages(msgs, []int64{0, 1, 2, 3, 4}, childThreadID)
	})

	t.Run("branch_adds_messages_then_gets_more_branches", func(t *testing.T) {
		env := newTestEnv(t)

		// A creates messages
		chatA, threadA, cwA := env.createChat("Chat A")
		env.addMessages(chatA, threadA, cwA, 0, 2)

		// B branches and adds messages
		chatB, threadB, cwB := env.branch(chatA, threadA, cwA, 1, "Chat B")
		env.addMessages(chatB, threadB, cwB, 2, 3)

		// C branches from B
		chatC, threadC, cwC := env.branch(chatB, threadB, cwB, 3, "Chat C")
		env.addMessages(chatC, threadC, cwC, 4, 2)

		// D also branches from B (different point)
		chatD, threadD, cwD := env.branch(chatB, threadB, cwB, 4, "Chat D")
		env.addMessages(chatD, threadD, cwD, 5, 2)

		// Load from C
		msgsC, err := env.service.LoadCurrentMessages(env.ctx, threadC)
		require.NoError(t, err)
		assert.Len(t, msgsC, 6) // A(0,1) + B(2,3) + C(4,5)
		env.verifyMessages(msgsC, []int64{0, 1, 2, 3, 4, 5}, threadC)

		// Load from D
		msgsD, err := env.service.LoadCurrentMessages(env.ctx, threadD)
		require.NoError(t, err)
		assert.Len(t, msgsD, 7) // A(0,1) + B(2,3,4) + D(5,6)
		env.verifyMessages(msgsD, []int64{0, 1, 2, 3, 4, 5, 6}, threadD)
	})
}
