package threads

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// =============================================================================
// Tests
// =============================================================================

func TestCreateThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("creates root thread with context window", func(t *testing.T) {
		thread, cw, err := h.svc.CreateThread(ctx, CreateThreadOpts{
			ChatID: h.chatID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if thread.ChatID != h.chatID {
			t.Errorf("expected conversation ID %s, got %s", h.chatID, thread.ChatID)
		}
		if thread.ParentThreadID != nil {
			t.Error("expected root thread to have nil parent")
		}
		if cw.Sequence != 0 {
			t.Errorf("expected sequence 0, got %d", cw.Sequence)
		}
		if cw.ThreadID != thread.ID {
			t.Errorf("context window thread mismatch")
		}
	})

	t.Run("generates ID if not provided", func(t *testing.T) {
		thread, _, err := h.svc.CreateThread(ctx, CreateThreadOpts{
			ChatID: h.chatID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if thread.ID == "" {
			t.Error("expected generated ID")
		}
	})

	t.Run("uses provided ID", func(t *testing.T) {
		thread, _, err := h.svc.CreateThread(ctx, CreateThreadOpts{
			ID:     "custom-id",
			ChatID: h.chatID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if thread.ID != "custom-id" {
			t.Errorf("expected ID custom-id, got %s", thread.ID)
		}
	})

	t.Run("requires conversation ID", func(t *testing.T) {
		_, _, err := h.svc.CreateThread(ctx, CreateThreadOpts{})
		if err == nil {
			t.Error("expected error for missing conversation ID")
		}
	})
}

func TestForkThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	// Create a parent thread
	parentThread, parentCW := h.createThread("parent-thread", h.chatID)
	forkMsg := h.addMessageWithID("parent-fork-msg", h.chatID, parentThread.ID, parentCW.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

	t.Run("creates forked thread inheriting sequence", func(t *testing.T) {
		forkedThread, forkedCW, err := h.svc.ForkThread(ctx, ForkThreadOpts{
			ChatID:                h.chatID,
			ParentThreadID:        parentThread.ID,
			ForkAtContextWindowID: parentCW.ID,
			ForkAtMessageID:       &forkMsg.ID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify fork metadata
		if forkedThread.ParentThreadID == nil || *forkedThread.ParentThreadID != parentThread.ID {
			t.Error("expected parent thread ID to be set")
		}
		if forkedThread.ForkAtMessageID == nil || *forkedThread.ForkAtMessageID != forkMsg.ID {
			t.Errorf("expected fork message %s", forkMsg.ID)
		}

		// Verify sequence inheritance
		if forkedCW.Sequence != parentCW.Sequence {
			t.Errorf("expected inherited sequence %d, got %d", parentCW.Sequence, forkedCW.Sequence)
		}
	})

	t.Run("inherits post-compaction sequence", func(t *testing.T) {
		// Create a compacted context window on parent
		compactedCW := h.compact(parentThread.ID, "summary-msg")
		compactMsg := h.addMessageWithID("compact-fork-msg", h.chatID, parentThread.ID, compactedCW.ID, 10, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		forkedThread, forkedCW, err := h.svc.ForkThread(ctx, ForkThreadOpts{
			ChatID:                h.chatID,
			ParentThreadID:        parentThread.ID,
			ForkAtContextWindowID: compactedCW.ID,
			ForkAtMessageID:       &compactMsg.ID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should inherit sequence 1 (post-compaction)
		if forkedCW.Sequence != 1 {
			t.Errorf("expected sequence 1 (post-compaction), got %d", forkedCW.Sequence)
		}
		_ = forkedThread
	})

	t.Run("requires parent thread ID", func(t *testing.T) {
		_, _, err := h.svc.ForkThread(ctx, ForkThreadOpts{
			ChatID:                h.chatID,
			ForkAtContextWindowID: parentCW.ID,
			ForkAtMessageID:       &forkMsg.ID,
		})
		if err == nil {
			t.Error("expected error for missing parent thread ID")
		}
	})

	t.Run("validates parent exists", func(t *testing.T) {
		_, _, err := h.svc.ForkThread(ctx, ForkThreadOpts{
			ChatID:                h.chatID,
			ParentThreadID:        "non-existent",
			ForkAtContextWindowID: parentCW.ID,
			ForkAtMessageID:       &forkMsg.ID,
		})
		if err == nil {
			t.Error("expected error for non-existent parent")
		}
	})
}

func TestResolveMessages(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("resolves simple thread messages", func(t *testing.T) {
		thread, cw := h.createThread("simple-thread", h.chatID)

		// Add messages
		h.addMessageWithID("msg-1", h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		h.addMessageWithID("msg-2", h.chatID, thread.ID, cw.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID: thread.ID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) != 2 {
			t.Errorf("expected 2 messages, got %d", len(msgs))
		}
	})

	t.Run("resolves forked thread with inherited messages", func(t *testing.T) {
		// Create parent with messages
		parentThread, parentCW := h.createThread("fork-parent", h.chatID)
		h.addMessageWithID("parent-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		h.addMessageWithID("parent-2", h.chatID, parentThread.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
		h.addMessageWithID("parent-3", h.chatID, parentThread.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Fork at ordinal 2 (should inherit parent-1, parent-2)
		childThread, childCW := h.forkThread("fork-child", h.chatID, parentThread.ID, 2, parentCW.ID)
		h.addMessageWithID("child-1", h.chatID, childThread.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID: childThread.ID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should have: parent-1, parent-2 (inherited), child-1 (local)
		if len(msgs) != 3 {
			t.Errorf("expected 3 messages, got %d", len(msgs))
		}

		// Verify order: inherited first, then local
		if msgs[0].ID != "parent-1" || msgs[1].ID != "parent-2" || msgs[2].ID != "child-1" {
			t.Errorf("unexpected message order: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
		}
	})

	t.Run("fork from compacted parent inherits compaction summary", func(t *testing.T) {
		// Create parent with messages, then compact
		parentThread, parentCW := h.createThread("compact-parent", h.chatID)
		h.addMessageWithID("cp-pre-compact-1", h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Compact parent - creates new context window with compaction_summary_message_id set
		compactedCW := h.compact(parentThread.ID, "cp-summary-msg")
		// Add the summary message to the compacted context window (this is what compact activity does)
		h.addMessageWithID("cp-summary-msg", h.chatID, parentThread.ID, compactedCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
		h.addMessageWithID("cp-post-compact-1", h.chatID, parentThread.ID, compactedCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Fork from post-compaction state
		// Child inherits sequence=1, but does NOT have compaction_summary_message_id
		childThread, childCW := h.forkThread("compact-child", h.chatID, parentThread.ID, 3, compactedCW.ID)
		h.addMessageWithID("cp-child-1", h.chatID, childThread.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID:        childThread.ID,
			ContextWindowID: &childCW.ID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Child does NOT have compaction_summary_message_id, so it SHOULD traverse
		// to parent and inherit the compaction summary message + post-compact-1
		// Result: cp-summary-msg (from parent's compacted CW), cp-post-compact-1, cp-child-1
		if len(msgs) != 3 {
			t.Errorf("expected 3 messages (inherited summary + post-compact + child), got %d", len(msgs))
			for i, m := range msgs {
				t.Logf("  msg[%d]: %s", i, m.ID)
			}
		}
	})

	t.Run("stops at compaction boundary when context window has summary", func(t *testing.T) {
		// Create thread and compact it
		thread, initialCW := h.createThread("compacted-thread", h.chatID)
		h.addMessageWithID("cb-pre-compact", h.chatID, thread.ID, initialCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Compact - creates context window with compaction_summary_message_id
		compactedCW := h.compact(thread.ID, "cb-summary-msg-2")
		// Add the summary message to the compacted context window
		h.addMessageWithID("cb-summary-msg-2", h.chatID, thread.ID, compactedCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
		h.addMessageWithID("cb-post-compact", h.chatID, thread.ID, compactedCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Resolve from the compacted context window (which HAS compaction_summary_message_id)
		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID:        thread.ID,
			ContextWindowID: &compactedCW.ID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Context window has compaction_summary_message_id, so we DON'T go to parent
		// Result: just the summary message and post-compact message
		if len(msgs) != 2 {
			t.Errorf("expected 2 messages (compaction boundary), got %d", len(msgs))
			for i, m := range msgs {
				t.Logf("  msg[%d]: %s", i, m.ID)
			}
		}
	})

	t.Run("nested forks (A -> B -> C)", func(t *testing.T) {
		// Thread A (root) with messages
		threadA, cwA := h.createThread("nested-A", h.chatID)
		h.addMessageWithID("A-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		h.addMessageWithID("A-2", h.chatID, threadA.ID, cwA.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		// Thread B forks from A at ordinal 2
		threadB, cwB := h.forkThread("nested-B", h.chatID, threadA.ID, 2, cwA.ID)
		h.addMessageWithID("B-1", h.chatID, threadB.ID, cwB.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		h.addMessageWithID("B-2", h.chatID, threadB.ID, cwB.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		// Thread C forks from B at ordinal 4
		threadC, cwC := h.forkThread("nested-C", h.chatID, threadB.ID, 4, cwB.ID)
		h.addMessageWithID("C-1", h.chatID, threadC.ID, cwC.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Resolve from C - should walk full chain
		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID: threadC.ID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should have: A-1, A-2, B-1, B-2, C-1
		if len(msgs) != 5 {
			t.Errorf("expected 5 messages, got %d", len(msgs))
		}

		expectedOrder := []string{"A-1", "A-2", "B-1", "B-2", "C-1"}
		for i, expected := range expectedOrder {
			if i < len(msgs) && msgs[i].ID != expected {
				t.Errorf("msg[%d]: expected %s, got %s", i, expected, msgs[i].ID)
			}
		}
	})
}

func TestCompact(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("creates new context window with incremented sequence", func(t *testing.T) {
		thread, initialCW := h.createThread("compact-thread", h.chatID)

		// Compact
		newCW, err := h.svc.Compact(ctx, thread.ID, "summary-msg")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify sequence is incremented
		if newCW.Sequence != initialCW.Sequence+1 {
			t.Errorf("expected sequence %d, got %d", initialCW.Sequence+1, newCW.Sequence)
		}

		// Verify compaction summary message ID is set
		if newCW.CompactionSummaryMessageID == nil || *newCW.CompactionSummaryMessageID != "summary-msg" {
			t.Errorf("expected compaction summary message ID summary-msg")
		}
	})

	t.Run("multiple compactions increment sequence", func(t *testing.T) {
		thread, _ := h.createThread("multi-compact", h.chatID)

		cw1, _ := h.svc.Compact(ctx, thread.ID, "summary-1")
		cw2, _ := h.svc.Compact(ctx, thread.ID, "summary-2")

		if cw1.Sequence != 1 || cw2.Sequence != 2 {
			t.Errorf("expected sequences 1,2 got %d,%d", cw1.Sequence, cw2.Sequence)
		}
	})
}

func TestGetThreadTokenCount(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("returns token count from latest message", func(t *testing.T) {
		thread, cw := h.createThread("token-thread", h.chatID)

		h.addMessageWithTokens(h.chatID, thread.ID, cw.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), 100, 50)

		tokens, err := h.svc.GetThreadTokenCount(ctx, thread.ID, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokens != 150 {
			t.Errorf("expected 150 tokens, got %d", tokens)
		}
	})

	t.Run("inherits tokens from parent when no local tokens", func(t *testing.T) {
		// Create parent with token data
		parentThread, parentCW := h.createThread("parent-token-thread", h.chatID)
		h.addMessageWithTokens(h.chatID, parentThread.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), 200, 0)

		// Fork with no messages
		childThread, _ := h.forkThread("child-token-thread", h.chatID, parentThread.ID, 1, parentCW.ID)

		tokens, err := h.svc.GetThreadTokenCount(ctx, childThread.ID, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokens != 200 {
			t.Errorf("expected inherited 200 tokens, got %d", tokens)
		}
	})
}

func TestContextUsage(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("returns context usage", func(t *testing.T) {
		thread, _ := h.createThread("usage-thread", h.chatID)

		usage, err := h.svc.GetContextUsage(ctx, thread.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if usage.ThreadID != thread.ID {
			t.Error("expected thread ID to match")
		}
		if usage.CompactionThreshold != DefaultCompactionThreshold {
			t.Errorf("expected default threshold %d, got %d", DefaultCompactionThreshold, usage.CompactionThreshold)
		}
	})
}

func TestWalkForkChain(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("walks simple chain", func(t *testing.T) {
		threadA, cwA := h.createThread("walk-A", h.chatID)
		threadB, cwB := h.forkThread("walk-B", h.chatID, threadA.ID, 1, cwA.ID)

		var visited []string
		err := h.svc.walkForkChain(ctx, threadB.ID, &cwB.ID, func(thread *db.Thread, cw *db.ContextWindow) (bool, error) {
			visited = append(visited, thread.ID)
			return true, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should visit B then A
		if len(visited) != 2 {
			t.Errorf("expected 2 visits, got %d", len(visited))
		}
		if visited[0] != "walk-B" || visited[1] != "walk-A" {
			t.Errorf("unexpected visit order: %v", visited)
		}
	})

	// Note: Circular reference test removed - real DB constraints prevent this
}

func TestCreateWorkflowWithThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("creates workflow and new thread atomically", func(t *testing.T) {
		workflow := &db.Workflow{
			ID:           "wf-1",
			ChatID:       h.chatID,
			WorkflowName: "test-workflow",
			Thread:       "thread-1",
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now().UTC(),
		}

		wf, thread, cw, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow: workflow,
			ThreadID: "thread-1",
			ChatID:   h.chatID,
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify workflow was created
		if wf.ID != "wf-1" {
			t.Errorf("expected workflow ID wf-1, got %s", wf.ID)
		}

		// Verify thread was created with workflow_id
		if thread.ID != "thread-1" {
			t.Errorf("expected thread ID thread-1, got %s", thread.ID)
		}
		if thread.WorkflowID == nil || *thread.WorkflowID != "wf-1" {
			t.Errorf("expected thread workflow_id wf-1, got %v", thread.WorkflowID)
		}
		if thread.ChatID != h.chatID {
			t.Errorf("expected conversation ID %s, got %s", h.chatID, thread.ChatID)
		}

		// Verify context window was created
		if cw.ThreadID != "thread-1" {
			t.Errorf("expected context window thread ID thread-1, got %s", cw.ThreadID)
		}
		if cw.Sequence != 0 {
			t.Errorf("expected sequence 0, got %d", cw.Sequence)
		}
	})

	t.Run("updates existing thread with workflow_id", func(t *testing.T) {
		// Create a thread first without workflow_id
		existingThread, existingCW := h.createThread("existing-thread", h.chatID)

		if existingThread.WorkflowID != nil {
			t.Errorf("expected nil workflow_id on new thread, got %v", existingThread.WorkflowID)
		}

		// Now create workflow with existing thread
		workflow := &db.Workflow{
			ID:           "wf-2",
			ChatID:       h.chatID,
			WorkflowName: "test-workflow",
			Thread:       "existing-thread",
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now().UTC(),
		}

		wf, thread, cw, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow: workflow,
			ThreadID: "existing-thread",
			ChatID:   h.chatID,
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify workflow was created
		if wf.ID != "wf-2" {
			t.Errorf("expected workflow ID wf-2, got %s", wf.ID)
		}

		// Verify thread was updated (not created again)
		if thread.ID != "existing-thread" {
			t.Errorf("expected thread ID existing-thread, got %s", thread.ID)
		}
		if thread.WorkflowID == nil || *thread.WorkflowID != "wf-2" {
			t.Errorf("expected thread workflow_id wf-2, got %v", thread.WorkflowID)
		}

		// Verify context window is the same (inherited)
		if cw.ID != existingCW.ID {
			t.Errorf("expected same context window, got %s instead of %s", cw.ID, existingCW.ID)
		}
	})

	t.Run("creates workflow and forked thread atomically using ForkFromThread", func(t *testing.T) {
		// Create parent thread first
		parentThread, parentCW := h.createThread("parent-thread", h.chatID)

		// Create workflow with forked thread using ForkFromThread
		workflow := &db.Workflow{
			ID:           "wf-3",
			ChatID:       h.chatID,
			WorkflowName: "test-workflow",
			Thread:       "forked-thread",
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now().UTC(),
		}

		wf, thread, cw, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow:       workflow,
			ThreadID:       "forked-thread",
			ChatID:         h.chatID,
			ForkFromThread: &parentThread.ID,
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify workflow was created
		if wf.ID != "wf-3" {
			t.Errorf("expected workflow ID wf-3, got %s", wf.ID)
		}

		// Verify forked thread was created with fork metadata and workflow_id
		if thread.ID != "forked-thread" {
			t.Errorf("expected thread ID forked-thread, got %s", thread.ID)
		}
		if thread.WorkflowID == nil || *thread.WorkflowID != "wf-3" {
			t.Errorf("expected thread workflow_id wf-3, got %v", thread.WorkflowID)
		}
		if thread.ParentThreadID == nil || *thread.ParentThreadID != "parent-thread" {
			t.Errorf("expected parent thread ID parent-thread, got %v", thread.ParentThreadID)
		}
		// ForkFromThread with an empty parent thread has no message to
		// reference: ForkAtMessageID stays nil, meaning "inherit nothing".
		if thread.ForkAtMessageID != nil {
			t.Errorf("expected nil fork message (empty parent), got %v", *thread.ForkAtMessageID)
		}

		// Verify context window inherits sequence from parent
		if cw.Sequence != parentCW.Sequence {
			t.Errorf("expected sequence %d (inherited), got %d", parentCW.Sequence, cw.Sequence)
		}
	})

	// Regression: inline/loop child workflows reuse the parent's workflow ID, so
	// the INSERT in step 1 hits an existing row. On Postgres a raw duplicate-key
	// violation aborts the whole transaction (SQLSTATE 25P02), poisoning every
	// later statement in the tx — including the fork's parent-thread lookup, which
	// then surfaces as a misleading "parent thread not found". CreateWorkflow must
	// be idempotent (INSERT ... ON CONFLICT DO NOTHING) so a pre-existing workflow
	// ID is a silent no-op and the fork proceeds normally.
	t.Run("existing workflow ID forking a new thread succeeds (inline loop)", func(t *testing.T) {
		// Parent thread to fork from.
		parentThread, _ := h.createThread("loop-parent-thread", h.chatID)

		// First iteration creates the workflow + its thread.
		workflow := &db.Workflow{
			ID:           "wf-loop",
			ChatID:       h.chatID,
			WorkflowName: "loop-workflow",
			Thread:       "loop-thread-0",
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now().UTC(),
		}
		if _, _, _, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow:       workflow,
			ThreadID:       "loop-thread-0",
			ChatID:         h.chatID,
			ForkFromThread: &parentThread.ID,
		}); err != nil {
			t.Fatalf("first iteration failed: %v", err)
		}

		// Second iteration reuses the SAME workflow ID (as inline loops do) while
		// forking a brand-new thread. This is the exact path that previously failed.
		workflow2 := &db.Workflow{
			ID:           "wf-loop", // same ID -> duplicate INSERT
			ChatID:       h.chatID,
			WorkflowName: "loop-workflow",
			Thread:       "loop-thread-1",
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now().UTC(),
		}
		_, thread, cw, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow:       workflow2,
			ThreadID:       "loop-thread-1",
			ChatID:         h.chatID,
			ForkFromThread: &parentThread.ID,
		})
		if err != nil {
			t.Fatalf("second iteration with existing workflow ID failed: %v", err)
		}
		if thread == nil || thread.ID != "loop-thread-1" {
			t.Fatalf("expected forked thread loop-thread-1, got %v", thread)
		}
		if thread.WorkflowID == nil || *thread.WorkflowID != "wf-loop" {
			t.Errorf("expected thread workflow_id wf-loop, got %v", thread.WorkflowID)
		}
		if cw == nil {
			t.Error("expected a context window for the forked thread")
		}
	})

	t.Run("requires workflow", func(t *testing.T) {
		_, _, _, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow: nil,
			ThreadID: "thread-x",
			ChatID:   h.chatID,
		})

		if err == nil {
			t.Error("expected error for nil workflow")
		}
	})

	t.Run("requires chat ID", func(t *testing.T) {
		workflow := &db.Workflow{
			ID:           "wf-x",
			ChatID:       h.chatID,
			WorkflowName: "test",
			Thread:       "thread-x",
			Status:       db.WorkflowStatusRunning,
		}

		_, _, _, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow: workflow,
			ThreadID: "thread-x",
			ChatID:   "",
		})

		if err == nil {
			t.Error("expected error for empty chat ID")
		}
	})

	t.Run("generates thread ID if not provided", func(t *testing.T) {
		workflow := &db.Workflow{
			ID:           "wf-gen",
			ChatID:       h.chatID,
			WorkflowName: "test",
			Thread:       "", // Will be generated
			Status:       db.WorkflowStatusRunning,
			CreatedAt:    time.Now().UTC(),
		}

		_, thread, _, err := h.svc.CreateWorkflowWithThread(ctx, CreateWorkflowWithThreadOpts{
			Workflow: workflow,
			ThreadID: "", // Empty = generate
			ChatID:   h.chatID,
		})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if thread.ID == "" {
			t.Error("expected generated thread ID")
		}
	})
}

// =============================================================================
// Integration Tests for Context Window Lineage
// =============================================================================

func TestContextWindowLineage_ForkThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("fork sets ParentContextWindowID and ForkAtMessageID", func(t *testing.T) {
		// Create parent thread
		parentThread, parentCW, err := h.svc.CreateThread(ctx, CreateThreadOpts{
			ID:     "cw-lineage-parent-thread",
			ChatID: h.chatID,
		})
		if err != nil {
			t.Fatalf("failed to create parent thread: %v", err)
		}
		forkMsg := h.addMessageWithID("cw-lineage-fork-msg", h.chatID, parentThread.ID, parentCW.ID, 5, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Fork at forkMsg
		_, childCW, err := h.svc.ForkThread(ctx, ForkThreadOpts{
			ID:                    "cw-lineage-child-thread",
			ChatID:                h.chatID,
			ParentThreadID:        parentThread.ID,
			ForkAtContextWindowID: parentCW.ID,
			ForkAtMessageID:       &forkMsg.ID,
		})
		if err != nil {
			t.Fatalf("failed to fork thread: %v", err)
		}

		// Verify ParentContextWindowID links to parent's CW
		if childCW.ParentContextWindowID == nil {
			t.Fatal("expected ParentContextWindowID to be set")
		}
		if *childCW.ParentContextWindowID != parentCW.ID {
			t.Errorf("expected ParentContextWindowID=%s, got %s", parentCW.ID, *childCW.ParentContextWindowID)
		}

		// Verify ForkAtMessageID is set to forkMsg
		if childCW.ForkAtMessageID == nil {
			t.Fatal("expected ForkAtMessageID to be set")
		}
		if *childCW.ForkAtMessageID != forkMsg.ID {
			t.Errorf("expected ForkAtMessageID=%s, got %s", forkMsg.ID, *childCW.ForkAtMessageID)
		}

		// Verify CompactionSummaryMessageID is nil (forking doesn't create compaction)
		if childCW.CompactionSummaryMessageID != nil {
			t.Errorf("expected CompactionSummaryMessageID to be nil for fork, got %v", *childCW.CompactionSummaryMessageID)
		}
	})

	t.Run("nested fork creates correct lineage chain", func(t *testing.T) {
		// Create A -> B -> C fork chain
		threadA, cwA, _ := h.svc.CreateThread(ctx, CreateThreadOpts{
			ID:     "lineage-A",
			ChatID: h.chatID,
		})
		msgA := h.addMessageWithID("lineage-A-msg", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		_, cwB, _ := h.svc.ForkThread(ctx, ForkThreadOpts{
			ID:                    "lineage-B",
			ChatID:                h.chatID,
			ParentThreadID:        threadA.ID,
			ForkAtContextWindowID: cwA.ID,
			ForkAtMessageID:       &msgA.ID,
		})
		msgB := h.addMessageWithID("lineage-B-msg", h.chatID, "lineage-B", cwB.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		threadC, cwC, _ := h.svc.ForkThread(ctx, ForkThreadOpts{
			ID:                    "lineage-C",
			ChatID:                h.chatID,
			ParentThreadID:        "lineage-B",
			ForkAtContextWindowID: cwB.ID,
			ForkAtMessageID:       &msgB.ID,
		})
		_ = threadC

		// Verify chain: C -> B -> A
		if cwC.ParentContextWindowID == nil || *cwC.ParentContextWindowID != cwB.ID {
			t.Error("expected C's parent CW to be B's CW")
		}
		if cwB.ParentContextWindowID == nil || *cwB.ParentContextWindowID != cwA.ID {
			t.Error("expected B's parent CW to be A's CW")
		}
		if cwA.ParentContextWindowID != nil {
			t.Error("expected root context window to have nil parent")
		}
	})
}

func TestContextWindowLineage_Compact(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("compact sets ParentContextWindowID and nil ForkAtMessageID", func(t *testing.T) {
		// Create thread with initial context window
		thread, initialCW, err := h.svc.CreateThread(ctx, CreateThreadOpts{
			ID:     "lineage-compact-thread",
			ChatID: h.chatID,
		})
		if err != nil {
			t.Fatalf("failed to create thread: %v", err)
		}

		// Add messages
		h.addMessageWithID("lmsg-1", h.chatID, thread.ID, initialCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		h.addMessageWithID("lmsg-2", h.chatID, thread.ID, initialCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		// Compact the thread
		compactedCW, err := h.svc.Compact(ctx, thread.ID, "summary-msg")
		if err != nil {
			t.Fatalf("failed to compact: %v", err)
		}

		// Verify ParentContextWindowID points to initial CW
		if compactedCW.ParentContextWindowID == nil {
			t.Fatal("expected ParentContextWindowID to be set")
		}
		if *compactedCW.ParentContextWindowID != initialCW.ID {
			t.Errorf("expected ParentContextWindowID=%s, got %s", initialCW.ID, *compactedCW.ParentContextWindowID)
		}

		// Verify ForkAtMessageID is nil (compaction is not a fork)
		if compactedCW.ForkAtMessageID != nil {
			t.Errorf("expected ForkAtMessageID to be nil for compaction, got %v", *compactedCW.ForkAtMessageID)
		}

		// Verify CompactionSummaryMessageID is set
		if compactedCW.CompactionSummaryMessageID == nil {
			t.Fatal("expected CompactionSummaryMessageID to be set")
		}
		if *compactedCW.CompactionSummaryMessageID != "summary-msg" {
			t.Errorf("expected summary message ID=summary-msg, got %s", *compactedCW.CompactionSummaryMessageID)
		}

		// Verify sequence is incremented
		if compactedCW.Sequence != initialCW.Sequence+1 {
			t.Errorf("expected sequence=%d, got %d", initialCW.Sequence+1, compactedCW.Sequence)
		}
	})

	t.Run("multiple compactions create chain", func(t *testing.T) {
		thread, cw0, _ := h.svc.CreateThread(ctx, CreateThreadOpts{
			ID:     "multi-compact-lineage-thread",
			ChatID: h.chatID,
		})

		// First compaction
		cw1, _ := h.svc.Compact(ctx, thread.ID, "summary-1")

		// Second compaction
		cw2, _ := h.svc.Compact(ctx, thread.ID, "summary-2")

		// Verify chain: cw2 -> cw1 -> cw0
		if cw2.ParentContextWindowID == nil || *cw2.ParentContextWindowID != cw1.ID {
			t.Errorf("expected cw2->cw1 link, got %v", cw2.ParentContextWindowID)
		}
		if cw1.ParentContextWindowID == nil || *cw1.ParentContextWindowID != cw0.ID {
			t.Errorf("expected cw1->cw0 link, got %v", cw1.ParentContextWindowID)
		}

		// Verify neither compaction has ForkAtMessageID
		if cw1.ForkAtMessageID != nil {
			t.Error("expected cw1.ForkAtMessageID to be nil")
		}
		if cw2.ForkAtMessageID != nil {
			t.Error("expected cw2.ForkAtMessageID to be nil")
		}

		// Verify sequences increment
		if cw1.Sequence != 1 || cw2.Sequence != 2 {
			t.Errorf("expected sequences 1,2 got %d,%d", cw1.Sequence, cw2.Sequence)
		}
	})
}

func TestContextWindowLineage_EndToEndResolution(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	t.Run("fork and resolve messages using CW lineage", func(t *testing.T) {
		// Create parent thread with messages
		parent, parentCW := h.createThread("e2e-parent", h.chatID)
		h.addMessageWithID("parent-1", h.chatID, parent.ID, parentCW.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		h.addMessageWithID("parent-2", h.chatID, parent.ID, parentCW.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
		h.addMessageWithID("parent-3", h.chatID, parent.ID, parentCW.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Fork at ordinal 2 (should inherit parent-1, parent-2 only)
		child, childCW := h.forkThread("e2e-child", h.chatID, parent.ID, 2, parentCW.ID)
		h.addMessageWithID("child-1", h.chatID, child.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		// Resolve child messages - should get parent-1, parent-2, child-1
		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID: child.ID,
		})
		if err != nil {
			t.Fatalf("failed to resolve messages: %v", err)
		}

		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(msgs))
		}
		if msgs[0].ID != "parent-1" || msgs[1].ID != "parent-2" || msgs[2].ID != "child-1" {
			t.Errorf("unexpected message order: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
		}
	})

	t.Run("compaction stops traversal at summary boundary", func(t *testing.T) {
		// Create thread with messages
		thread, cw0 := h.createThread("e2e-compact", h.chatID)
		h.addMessageWithID("old-1", h.chatID, thread.ID, cw0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))
		h.addMessageWithID("old-2", h.chatID, thread.ID, cw0.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		// Compact
		cw1 := h.compact(thread.ID, "summary-msg")
		h.addMessageWithID("summary-msg", h.chatID, thread.ID, cw1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
		h.addMessageWithID("new-1", h.chatID, thread.ID, cw1.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Resolve from compacted context window - should NOT traverse to cw0
		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID:        thread.ID,
			ContextWindowID: &cw1.ID,
		})
		if err != nil {
			t.Fatalf("failed to resolve: %v", err)
		}

		// Should only get messages from cw1 (summary + new-1), NOT old-1 or old-2
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages (compaction boundary), got %d", len(msgs))
		}
		if msgs[0].ID != "summary-msg" || msgs[1].ID != "new-1" {
			t.Errorf("unexpected messages: %v", []string{msgs[0].ID, msgs[1].ID})
		}
	})

	t.Run("fork from compacted parent inherits summary", func(t *testing.T) {
		// Create parent and compact
		parent, cw0 := h.createThread("e2e-compact-parent", h.chatID)
		h.addMessageWithID("pre-compact", h.chatID, parent.ID, cw0.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		cw1 := h.compact(parent.ID, "parent-summary")
		h.addMessageWithID("parent-summary", h.chatID, parent.ID, cw1.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM))
		h.addMessageWithID("post-compact", h.chatID, parent.ID, cw1.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Fork from compacted parent
		child, childCW := h.forkThread("e2e-compact-child", h.chatID, parent.ID, 3, cw1.ID)
		h.addMessageWithID("child-msg", h.chatID, child.ID, childCW.ID, 4, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		// Child CW should link to parent's compacted CW
		if childCW.ParentContextWindowID == nil || *childCW.ParentContextWindowID != cw1.ID {
			t.Errorf("expected child->parent compacted CW link, got %v", childCW.ParentContextWindowID)
		}

		// Resolve child - should get parent-summary, post-compact, child-msg
		// (stops at parent's compaction boundary)
		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID: child.ID,
		})
		if err != nil {
			t.Fatalf("failed to resolve: %v", err)
		}

		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages (parent summary stops traversal), got %d", len(msgs))
		}
		if msgs[0].ID != "parent-summary" || msgs[1].ID != "post-compact" || msgs[2].ID != "child-msg" {
			t.Errorf("unexpected messages: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
		}
	})

	t.Run("nested forks traverse entire CW chain", func(t *testing.T) {
		// A -> B -> C fork chain
		threadA, cwA := h.createThread("e2e-nested-A", h.chatID)
		h.addMessageWithID("A-1", h.chatID, threadA.ID, cwA.ID, 1, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		threadB, cwB := h.forkThread("e2e-nested-B", h.chatID, threadA.ID, 1, cwA.ID)
		h.addMessageWithID("B-1", h.chatID, threadB.ID, cwB.ID, 2, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))

		threadC, cwC := h.forkThread("e2e-nested-C", h.chatID, threadB.ID, 2, cwB.ID)
		h.addMessageWithID("C-1", h.chatID, threadC.ID, cwC.ID, 3, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER))

		// Verify CW lineage: C -> B -> A
		if cwC.ParentContextWindowID == nil || *cwC.ParentContextWindowID != cwB.ID {
			t.Error("expected C->B lineage")
		}
		if cwB.ParentContextWindowID == nil || *cwB.ParentContextWindowID != cwA.ID {
			t.Error("expected B->A lineage")
		}

		// Resolve C - should traverse C -> B -> A and get all messages
		msgs, err := h.svc.ResolveMessages(ctx, ResolveMessagesOpts{
			ThreadID: threadC.ID,
		})
		if err != nil {
			t.Fatalf("failed to resolve: %v", err)
		}

		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages (full chain), got %d", len(msgs))
		}
		if msgs[0].ID != "A-1" || msgs[1].ID != "B-1" || msgs[2].ID != "C-1" {
			t.Errorf("unexpected message order: %v", []string{msgs[0].ID, msgs[1].ID, msgs[2].ID})
		}
	})
}

// Override time for testing
func init() {
	now = func() time.Time {
		return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
}
