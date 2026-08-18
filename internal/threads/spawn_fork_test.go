package threads

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// LEVEL 1: E2E/Integration Test - Full spawn fork flow
// ============================================================================
//
// This test simulates the full spawn workflow flow:
// 1. Parent thread with messages exists
// 2. CreateWorkflowWithThread is called with ForkFromThread (simulating spawn)
// 3. Verify child thread has correct fork metadata
// 4. Verify LoadCurrentMessages on child returns parent messages
//
// Expected: Child thread inherits parent messages
// ============================================================================

func TestSpawnFork_E2E_ChildInheritsParentMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Create parent thread with messages
	parentThread, parentCW := h.createThread("parent-thread", h.chatID)
	h.addMessageWithID("parent-msg-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("parent-msg-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("parent-msg-3", h.chatID, parentThread.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// ACT: Create child workflow+thread via spawn (uses ForkFromThread)
	childWorkflow := &db.Workflow{
		ID:           "child-wf-1",
		ChatID:       h.chatID,
		WorkflowName: "spawned-researcher",
		Thread:       "child-thread-1",
		Status:       db.Active(),
		CreatedAt:    time.Now().UTC(),
	}

	wf, childThread, childCW, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
		Workflow:       childWorkflow,
		ThreadID:       "child-thread-1",
		ChatID:         h.chatID,
		ForkFromThread: &parentThread.ID, // This is what spawn tool sets
	})

	// ASSERT: Workflow and thread created successfully
	if err != nil {
		t.Fatalf("CreateWorkflowWithThread failed: %v", err)
	}
	if wf.ID != "child-wf-1" {
		t.Errorf("expected workflow ID child-wf-1, got %s", wf.ID)
	}

	// ASSERT: Child thread has fork metadata
	if childThread.ParentThreadID == nil {
		t.Fatal("FAIL: child thread has nil ParentThreadID - fork metadata not set")
	}
	if *childThread.ParentThreadID != parentThread.ID {
		t.Errorf("expected parent thread ID %s, got %s", parentThread.ID, *childThread.ParentThreadID)
	}
	if childThread.ForkAtMessageID == nil {
		t.Fatal("FAIL: child thread has nil ForkAtMessageID - fork metadata not set")
	}
	if forkMsg, err := h.repo.GetMessage(ctx, *childThread.ForkAtMessageID); err != nil || forkMsg.ContextWindowID != parentCW.ID {
		t.Errorf("expected fork message in CW %s, got message in %s (err=%v)", parentCW.ID, forkMsg.ContextWindowID, err)
	}

	t.Logf("✓ Child thread fork metadata correct: ParentThreadID=%s, ForkAtMessageID=%s",
		*childThread.ParentThreadID, *childThread.ForkAtMessageID)

	// ASSERT: Child context window has parent chain
	if childCW.ParentContextWindowID == nil {
		t.Fatal("FAIL: child context window has nil ParentContextWindowID - CW chain broken")
	}
	if *childCW.ParentContextWindowID != parentCW.ID {
		t.Errorf("expected parent CW ID %s, got %s", parentCW.ID, *childCW.ParentContextWindowID)
	}

	t.Logf("✓ Child context window chain correct: ParentContextWindowID=%s", *childCW.ParentContextWindowID)

	// CRITICAL TEST: LoadCurrentMessages on child should return parent messages
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages failed: %v", err)
	}

	// Should have all 3 parent messages
	if len(msgs) != 3 {
		t.Errorf("FAIL: expected 3 parent messages, got %d", len(msgs))
		t.Logf("Messages returned:")
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s (role=%d, ordinal=%d)", i, m.ID, m.Role, m.Ordinal)
		}
		t.Fatalf("BUG CONFIRMED: Child thread not inheriting parent messages")
	}

	// Verify correct messages in order
	expectedIDs := []string{"parent-msg-1", "parent-msg-2", "parent-msg-3"}
	for i, expectedID := range expectedIDs {
		if i >= len(msgs) {
			t.Errorf("missing message at index %d", i)
			continue
		}
		if msgs[i].ID != expectedID {
			t.Errorf("msg[%d]: expected %s, got %s", i, expectedID, msgs[i].ID)
		}
	}

	t.Logf("✓ PASS: LoadCurrentMessages returned %d parent messages correctly", len(msgs))
}

// ============================================================================
// LEVEL 2: Thread Service Test - CreateWorkflowWithThread with ForkFromThread
// ============================================================================
//
// Test just the CreateWorkflowWithThread method's fork handling.
// This isolates whether the service correctly:
// 1. Resolves fork point from thread ID
// 2. Creates thread with correct fork metadata
// 3. Creates context window with parent chain
//
// Expected: Thread and CW have correct fork metadata
// ============================================================================

func TestCreateWorkflowWithThread_ForkFromThread_SetsForkMetadata(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Create parent thread with messages
	parentThread, parentCW := h.createThread("parent-for-service-test", h.chatID)
	h.addMessageWithID("setup-msg", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// ACT: Call CreateWorkflowWithThread with ForkFromThread
	workflow := &db.Workflow{
		ID:           "test-wf-2",
		ChatID:       h.chatID,
		WorkflowName: "test-workflow",
		Thread:       "child-thread-2",
		Status:       db.Active(),
		CreatedAt:    time.Now().UTC(),
	}

	_, thread, cw, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
		Workflow:       workflow,
		ThreadID:       "child-thread-2",
		ChatID:         h.chatID,
		ForkFromThread: &parentThread.ID,
	})

	// ASSERT: No error
	if err != nil {
		t.Fatalf("CreateWorkflowWithThread failed: %v", err)
	}

	// ASSERT: Thread has ParentThreadID set
	if thread.ParentThreadID == nil {
		t.Fatal("FAIL: ParentThreadID is nil")
	}
	if *thread.ParentThreadID != parentThread.ID {
		t.Errorf("expected ParentThreadID=%s, got %s", parentThread.ID, *thread.ParentThreadID)
	}
	t.Logf("✓ Thread.ParentThreadID = %s", *thread.ParentThreadID)

	// ASSERT: Thread has ForkAtMessageID set, in the parent's CW
	if thread.ForkAtMessageID == nil {
		t.Fatal("FAIL: ForkAtMessageID is nil")
	}
	forkMsg, err := h.repo.GetMessage(ctx, *thread.ForkAtMessageID)
	if err != nil || forkMsg.ContextWindowID != parentCW.ID {
		t.Errorf("expected fork message in CW %s, got message in %s (err=%v)", parentCW.ID, forkMsg.ContextWindowID, err)
	}
	t.Logf("✓ Thread.ForkAtMessageID = %s", *thread.ForkAtMessageID)

	// ASSERT: Thread's fork message is the parent's latest (ForkFromThread
	// calculates this). Parent has 1 message at ordinal 1, so that's it.
	if forkMsg.Ordinal != 1 {
		t.Errorf("expected fork message ordinal=1, got %d", forkMsg.Ordinal)
	}
	t.Logf("✓ Thread's fork message ordinal = %d", forkMsg.Ordinal)

	// ASSERT: Context window has ParentContextWindowID set
	if cw.ParentContextWindowID == nil {
		t.Fatal("FAIL: Context window ParentContextWindowID is nil")
	}
	if *cw.ParentContextWindowID != parentCW.ID {
		t.Errorf("expected CW.ParentContextWindowID=%s, got %s", parentCW.ID, *cw.ParentContextWindowID)
	}
	t.Logf("✓ ContextWindow.ParentContextWindowID = %s", *cw.ParentContextWindowID)

	// ASSERT: Context window has ForkAtMessageID matching the thread's
	if cw.ForkAtMessageID == nil {
		t.Fatal("FAIL: Context window ForkAtMessageID is nil")
	}
	if *cw.ForkAtMessageID != *thread.ForkAtMessageID {
		t.Errorf("expected CW.ForkAtMessageID=%s, got %s", *thread.ForkAtMessageID, *cw.ForkAtMessageID)
	}
	t.Logf("✓ ContextWindow.ForkAtMessageID = %s", *cw.ForkAtMessageID)

	t.Logf("✓ PASS: All fork metadata correctly set")
}

// ============================================================================
// LEVEL 3: Message Loading Test - LoadCurrentMessages on forked thread
// ============================================================================
//
// Test message loading specifically for a forked thread.
// This isolates whether LoadCurrentMessages correctly:
// 1. Walks the CW chain via ParentContextWindowID
// 2. Returns parent messages up to ForkAtOrdinal
//
// Expected: Parent messages are returned
// ============================================================================

func TestLoadCurrentMessages_ForkedThread_ReturnsParentMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Create parent thread with messages
	parentThread, parentCW := h.createThread("parent-msg-test", h.chatID)
	h.addMessageWithID("parent-a", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("parent-b", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// ARRANGE: Create forked child thread (using helper with maxOrdinal=2 to include all)
	childThread, childCW := h.forkThread("child-msg-test", h.chatID, parentThread.ID, 2, parentCW.ID)

	// Verify fork setup is correct before testing message loading
	if childThread.ParentThreadID == nil || *childThread.ParentThreadID != parentThread.ID {
		t.Fatalf("test setup broken: child ParentThreadID not set correctly")
	}
	if childCW.ParentContextWindowID == nil || *childCW.ParentContextWindowID != parentCW.ID {
		t.Fatalf("test setup broken: child ParentContextWindowID not set correctly")
	}

	t.Logf("Fork chain setup: Parent CW %s -> Child CW %s", parentCW.ID, childCW.ID)

	// ACT: Load messages for child thread
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages failed: %v", err)
	}

	// ASSERT: Should return parent messages (ForkAtOrdinal=2 includes ordinals 1-2)
	if len(msgs) != 2 {
		t.Errorf("FAIL: expected 2 parent messages, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s", i, m.ID)
		}
	} else {
		if msgs[0].ID != "parent-a" || msgs[1].ID != "parent-b" {
			t.Errorf("wrong messages: got [%s, %s], expected [parent-a, parent-b]",
				msgs[0].ID, msgs[1].ID)
		} else {
			t.Logf("✓ PASS: LoadCurrentMessages returned parent messages correctly")
		}
	}
}

// ============================================================================
// LEVEL 4: Context Window Chain Test - CW parent chain walkability
// ============================================================================
//
// Test the context window chain resolution in isolation.
// This tests whether ResolveMessagesFromCW correctly:
// 1. Follows ParentContextWindowID links
// 2. Loads messages from parent CW
//
// Expected: Parent CW messages are resolved
// ============================================================================

func TestResolveMessagesFromCW_FollowsParentChain(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Create parent context window with messages
	parentThread, parentCW := h.createThread("cw-chain-parent", h.chatID)
	h.addMessageWithID("cw-parent-msg", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// ARRANGE: Create child context window linked to parent (maxOrdinal=1 to include all)
	_, childCW := h.forkThread("cw-chain-child", h.chatID, parentThread.ID, 1, parentCW.ID)

	// Verify CW chain setup
	if childCW.ParentContextWindowID == nil {
		t.Fatal("test setup broken: child CW has no ParentContextWindowID")
	}
	if *childCW.ParentContextWindowID != parentCW.ID {
		t.Fatalf("test setup broken: child CW parent is %s, expected %s",
			*childCW.ParentContextWindowID, parentCW.ID)
	}

	t.Logf("CW chain: %s (parent) <- %s (child)", parentCW.ID, childCW.ID)

	// ACT: Resolve messages from child CW (should walk to parent)
	msgs, err := h.svc.ResolveMessagesFromCW(ctx, childCW.ID)
	if err != nil {
		t.Fatalf("ResolveMessagesFromCW failed: %v", err)
	}

	// ASSERT: Should return message from parent CW
	if len(msgs) != 1 {
		t.Errorf("FAIL: expected 1 message from parent CW, got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s (from CW %s)", i, m.ID, m.ContextWindowID)
		}
	} else {
		if msgs[0].ID != "cw-parent-msg" {
			t.Errorf("wrong message: got %s, expected cw-parent-msg", msgs[0].ID)
		} else {
			t.Logf("✓ PASS: ResolveMessagesFromCW followed parent chain correctly")
		}
	}
}

// ============================================================================
// DIAGNOSTIC TEST: Verify fork resolution with ordinal 0
// ============================================================================
//
// This test verifies that ForkFromThread correctly uses ordinal 0,
// which should include ALL parent messages (not filter them out).
//
// This is critical because ordinal filtering only applies to direct parent CW.
// ============================================================================

func TestForkFromThread_OrdinalZero_IncludesAllMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Parent with many messages
	parentThread, parentCW := h.createThread("ordinal-test-parent", h.chatID)
	h.addMessageWithID("msg-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-3", h.chatID, parentThread.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-4", h.chatID, parentThread.ID, parentCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-5", h.chatID, parentThread.ID, parentCW.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// ACT: Fork using maxOrdinal=5 (ForkFromThread behavior - include all 5 messages)
	childThread, _ := h.forkThread("ordinal-test-child", h.chatID, parentThread.ID, 5, parentCW.ID)

	// Verify fork used the message at ordinal 5
	if childThread.ForkAtMessageID == nil || *childThread.ForkAtMessageID != "msg-5" {
		t.Fatalf("test setup: expected ForkAtMessageID=msg-5, got %v", childThread.ForkAtMessageID)
	}

	// ACT: Load messages
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages failed: %v", err)
	}

	// ASSERT: With fork at msg-5 (ordinal 5), all messages should be included
	t.Logf("ForkAtMessageID=msg-5 returned %d messages", len(msgs))

	// Verify all 5 messages are included
	if len(msgs) != 5 {
		t.Errorf("FAIL: expected 5 messages, got %d", len(msgs))
	} else {
		t.Logf("✓ maxOrdinal=5 correctly included all 5 messages")
	}

	for i, m := range msgs {
		t.Logf("  msg[%d]: %s (ordinal=%d)", i, m.ID, m.Ordinal)
	}
}

// ============================================================================
// EDGE CASE TEST: Empty parent thread
// ============================================================================

func TestSpawnFork_EmptyParent_NoMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Parent with no messages
	parentThread, _ := h.createThread("empty-parent", h.chatID)

	// ACT: Fork from empty parent
	workflow := &db.Workflow{
		ID:           "wf-empty",
		ChatID:       h.chatID,
		WorkflowName: "test",
		Thread:       "child-empty",
		Status:       db.Active(),
		CreatedAt:    time.Now().UTC(),
	}

	_, childThread, _, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
		Workflow:       workflow,
		ThreadID:       "child-empty",
		ChatID:         h.chatID,
		ForkFromThread: &parentThread.ID,
	})

	if err != nil {
		t.Fatalf("CreateWorkflowWithThread failed: %v", err)
	}

	// ACT: Load messages
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages failed: %v", err)
	}

	// ASSERT: Empty parent should give empty messages
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	} else {
		t.Logf("✓ Empty parent correctly returned 0 messages")
	}
}

// ============================================================================
// REGRESSION TEST: ForkFromMessage includes messages up to that point
// ============================================================================
//
// This test verifies that when forking from a specific message (using ordinal),
// only messages up to and including that ordinal are inherited.
//
// Expected: Child sees messages 1-3 only, not 4-5
// ============================================================================

func TestSpawnFork_ForkFromMessage_IncludesMessagesUpToPoint(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Create parent thread with 5 messages
	parentThread, parentCW := h.createThread("parent-5msg", h.chatID)
	h.addMessageWithID("msg-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-3", h.chatID, parentThread.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("msg-4", h.chatID, parentThread.ID, parentCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("msg-5", h.chatID, parentThread.ID, parentCW.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// ACT: Fork from message at ordinal 3
	childThread, _ := h.forkThread("fork-at-3", h.chatID, parentThread.ID, 3, parentCW.ID)

	// ASSERT: Verify fork metadata
	if childThread.ForkAtMessageID == nil || *childThread.ForkAtMessageID != "msg-3" {
		t.Fatalf("test setup: expected ForkAtMessageID=msg-3, got %v", childThread.ForkAtMessageID)
	}

	// ACT: Load messages
	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages failed: %v", err)
	}

	// ASSERT: Should have messages 1-3 only
	if len(msgs) != 3 {
		t.Errorf("FAIL: expected 3 messages (1-3), got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s (ordinal=%d)", i, m.ID, m.Ordinal)
		}
		t.FailNow()
	}

	// Verify correct messages
	expectedIDs := []string{"msg-1", "msg-2", "msg-3"}
	for i, expectedID := range expectedIDs {
		if msgs[i].ID != expectedID {
			t.Errorf("msg[%d]: expected %s, got %s", i, expectedID, msgs[i].ID)
		}
		if msgs[i].Ordinal != int64(i+1) {
			t.Errorf("msg[%d]: expected ordinal %d, got %d", i, i+1, msgs[i].Ordinal)
		}
	}

	t.Logf("✓ PASS: ForkFromMessage correctly returned messages 1-3 (ordinal <= 3)")
}

// ============================================================================
// REGRESSION TEST: Nested forks work correctly
// ============================================================================
//
// This test verifies that forking chains work: A -> B -> C
// When C loads messages, it should see A's messages through the chain.
//
// Expected: C sees A's messages
// ============================================================================

func TestSpawnFork_NestedForks_InheritThroughChain(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Create parent A with messages
	parentA, cwA := h.createThread("parent-a", h.chatID)
	h.addMessageWithID("a-msg-1", h.chatID, parentA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("a-msg-2", h.chatID, parentA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

	// ARRANGE: Fork B from A (include all messages, maxOrdinal=2)
	childB, cwB := h.forkThread("child-b", h.chatID, parentA.ID, 2, cwA.ID)
	h.addMessageWithID("b-msg-1", h.chatID, childB.ID, cwB.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// ARRANGE: Fork C from B (include all messages, maxOrdinal=1)
	// This creates the chain: A -> B -> C
	grandchildC, _ := h.forkThread("grandchild-c", h.chatID, childB.ID, 1, cwB.ID)

	// ACT: Load messages for C
	msgs, err := h.svc.LoadCurrentMessages(ctx, grandchildC.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages failed: %v", err)
	}

	// ASSERT: C should see A's messages through the chain
	// Expected: a-msg-1, a-msg-2, b-msg-1
	if len(msgs) != 3 {
		t.Errorf("FAIL: expected 3 messages (A's 2 + B's 1), got %d", len(msgs))
		for i, m := range msgs {
			t.Logf("  msg[%d]: %s (thread=%s, ordinal=%d)", i, m.ID, m.ThreadID, m.Ordinal)
		}
		t.FailNow()
	}

	// Verify messages from A and B are present
	expectedIDs := []string{"a-msg-1", "a-msg-2", "b-msg-1"}
	for i, expectedID := range expectedIDs {
		if msgs[i].ID != expectedID {
			t.Errorf("msg[%d]: expected %s, got %s", i, expectedID, msgs[i].ID)
		}
	}

	t.Logf("✓ PASS: Nested fork C correctly inherited messages from A through B")
}

// ============================================================================
// REGRESSION TEST: Token count inheritance with ordinal 0
// ============================================================================
//
// This test verifies that token counts are inherited correctly when
// ordinal=0 (ForkFromThread behavior). This tests the fix in repository_impl.go.
//
// Bug: GetThreadTokenCount was passing ordinal=0 to SQL query `ordinal <= ?`
// which would match no messages (ordinals start at 1).
//
// Expected: Child thread inherits parent's token count
// ============================================================================

func TestSpawnFork_TokenCount_InheritedWithOrdinalZero(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// ARRANGE: Create parent thread with a message that has token data
	parentThread, parentCW := h.createThread("parent-tokens", h.chatID)

	// Create a message with token counts using the helper
	// Helper creates InputTokens and OutputTokens, total = 100 + 50 = 150
	parentMsg := h.addMessageWithTokens(h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), 100, 50)

	// ACT: Get parent's token count
	parentTokens, err := h.repo.GetThreadTokenCount(ctx, parentThread.ID, nil)
	if err != nil {
		t.Fatalf("GetThreadTokenCount(parent) failed: %v", err)
	}

	// Verify parent has tokens
	expectedParentTokens := int64(150) // 100 input + 50 output
	if parentTokens != expectedParentTokens {
		t.Errorf("parent tokens: expected %d, got %d", expectedParentTokens, parentTokens)
	}
	t.Logf("Parent thread token count: %d", parentTokens)

	// ARRANGE: Fork child from parent using maxOrdinal=1 (to include all messages)
	// ForkFromThread would calculate this as nextOrdinal-1 = 2-1 = 1
	childThread, _ := h.forkThread("child-tokens", h.chatID, parentThread.ID, 1, parentCW.ID)

	// Verify fork metadata
	if childThread.ForkAtMessageID == nil || *childThread.ForkAtMessageID != parentMsg.ID {
		t.Fatalf("test setup: expected ForkAtMessageID=%s, got %v", parentMsg.ID, childThread.ForkAtMessageID)
	}

	// ACT: Get child's token count (should inherit from parent)
	childTokens, err := h.repo.GetThreadTokenCount(ctx, childThread.ID, nil)
	if err != nil {
		t.Fatalf("GetThreadTokenCount(child) failed: %v", err)
	}

	// ASSERT: Child should inherit parent's token count
	if childTokens != expectedParentTokens {
		t.Errorf("FAIL: child tokens: expected %d (inherited from parent), got %d", expectedParentTokens, childTokens)
		t.Logf("BUG: ordinal=0 in GetThreadTokenCount not handled correctly")
		t.FailNow()
	}

	t.Logf("✓ PASS: Child thread correctly inherited %d tokens from parent", childTokens)
}

// ============================================================================
// REGRESSION TEST: Forking in a multi-thread chat where ordinal and seq diverge
// ============================================================================
//
// Context: messages.seq replaced messages.ordinal as the chat-global total
// order (see 20260802000000_add_message_seq.sql). Fork filtering
// (resolveMessagesFromCW in messages.go, BranchChat in chat_branch.go)
// deliberately stayed on ordinal, since ordinal — not seq — is the
// per-thread position a fork point is defined against. This test proves
// that filtering still works correctly in the one case where a mistaken
// migration to seq would hide: a chat with multiple threads writing
// concurrently, so each thread's ordinal sequence and the chat's seq
// sequence numerically diverge (interleaved writes mean a message's
// ordinal is no longer equal to, or even ordered the same as, its seq).
//
// Setup: a chat with a "main" thread and a "spawn" thread (simulating a
// parallel spawned sub-agent) both writing messages, interleaved so that
// seq allocation order does NOT match either thread's ordinal order.
// We then fork the main thread partway through and assert the forked
// thread inherits exactly the right main-thread messages by identity —
// no spawn-thread messages, and nothing past the fork point.
// ============================================================================

func TestSpawnFork_MultiThreadChat_OrdinalSeqDiverge(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	mainThread, mainCW := h.createThread("main-thread", h.chatID)
	spawnThread, spawnCW := h.createThread("spawn-thread", h.chatID)

	// ARRANGE: interleave writes across the two threads so seq (chat-global,
	// allocated in call order below) and ordinal (per-thread) diverge.
	//
	//   write order   thread   ordinal   seq
	//   spawn-a       spawn    1         0
	//   spawn-b       spawn    2         1
	//   main-1        main     1         2
	//   spawn-c       spawn    3         3
	//   main-2        main     2         4   <- fork point (ordinal=2, seq=4)
	//   spawn-d       spawn    4         5
	//   main-3        main     3         6   <- must NOT be inherited
	h.addMessageWithID("spawn-a", h.chatID, spawnThread.ID, spawnCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("spawn-b", h.chatID, spawnThread.ID, spawnCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	main1 := h.addMessageWithID("main-1", h.chatID, mainThread.ID, mainCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	h.addMessageWithID("spawn-c", h.chatID, spawnThread.ID, spawnCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
	main2 := h.addMessageWithID("main-2", h.chatID, mainThread.ID, mainCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("spawn-d", h.chatID, spawnThread.ID, spawnCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	h.addMessageWithID("main-3", h.chatID, mainThread.ID, mainCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	// Sanity: confirm the fork point's ordinal and seq genuinely differ.
	// If they ever coincided, this test wouldn't exercise the ordinal-vs-seq
	// distinction it's designed to catch.
	if main2.Ordinal == main2.Seq {
		t.Fatalf("test setup invalid: fork point ordinal (%d) must differ from seq (%d) to exercise ordinal/seq divergence", main2.Ordinal, main2.Seq)
	}
	t.Logf("fork point: ordinal=%d seq=%d (diverged as required)", main2.Ordinal, main2.Seq)

	// ACT: fork the main thread at ordinal 2 (main-1, main-2), leaving out
	// main-3 and every spawn-thread message.
	childThread, _ := h.forkThread("child-of-main", h.chatID, mainThread.ID, main2.Ordinal, mainCW.ID)

	msgs, err := h.svc.LoadCurrentMessages(ctx, childThread.ID)
	if err != nil {
		t.Fatalf("LoadCurrentMessages failed: %v", err)
	}

	// ASSERT: exactly main-1 and main-2, in that order, by identity/content —
	// not by numeric ordinal/seq values (those are allocation details).
	expectedIDs := []string{"main-1", "main-2"}
	gotIDs := make([]string, len(msgs))
	for i, m := range msgs {
		gotIDs[i] = m.ID
	}
	if len(gotIDs) != len(expectedIDs) {
		t.Fatalf("FAIL: expected messages %v, got %v", expectedIDs, gotIDs)
	}
	for i, expectedID := range expectedIDs {
		if gotIDs[i] != expectedID {
			t.Fatalf("FAIL: expected messages %v, got %v", expectedIDs, gotIDs)
		}
	}

	// ASSERT: no spawn-thread message and no post-fork-point main message leaked in.
	forbidden := map[string]bool{"spawn-a": true, "spawn-b": true, "spawn-c": true, "spawn-d": true, "main-3": true}
	for _, m := range msgs {
		if forbidden[m.ID] {
			t.Fatalf("FAIL: forked thread inherited message %q which should not be visible (spawn-thread message or past the fork point)", m.ID)
		}
	}

	t.Logf("✓ PASS: fork correctly inherited exactly main-1, main-2 in a multi-thread chat with diverged ordinal/seq (main1=%+v)", main1.ID)
}
