package threads

import (
	"context"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// An interrupted turn is persisted twice under one pre-allocated message id:
// CallLLM writes the partial itself on the way out (keyed on the stable
// call_llm|... idempotency key), and the re-dispatched save_message node
// writes the same turn under a DIFFERENT activity id. The id-based idempotency
// check only looks up activity_id, so it misses the first row entirely and the
// insert collides on messages_pkey.
//
// The pkey is the real uniqueness constraint on a pre-allocated id, so the save
// path has to reconcile against the id as well as the activity id.
func TestSaveMessage_FixedID_SecondWriterDifferentActivityID(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-id-collision", h.chatID)
	workflowID := "wf-collision"

	// Writer 1: CallLLM's interrupted-turn persist, under the stable
	// (workflow, step, iteration) key.
	callLLMKey := "call_llm|13:wf-collision|8:call_llm|10:agent_loop|6:iter:3"
	preallocatedID := "preallocated-collision-id"

	if _, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID:        h.chatID,
		Thread:        thread.ID,
		Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content:       "partial turn persisted by CallLLM",
		WorkflowID:    &workflowID,
		ActivityID:    &callLLMKey,
		AttemptNumber: 1,
		MessageID:     preallocatedID,
	}); err != nil {
		t.Fatalf("first writer failed: %v", err)
	}

	// Writer 2: the re-dispatched save_message node for the same turn. Same
	// pre-allocated message id, different activity id (Temporal-minted, so the
	// activity-id lookup cannot see writer 1's row).
	saveNodeKey := workflowID + "-run-abc-144"

	result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID:        h.chatID,
		Thread:        thread.ID,
		Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content:       "same turn, re-dispatched save_message node",
		WorkflowID:    &workflowID,
		ActivityID:    &saveNodeKey,
		AttemptNumber: 1,
		MessageID:     preallocatedID,
	})
	if err != nil {
		if strings.Contains(err.Error(), "messages_pkey") {
			t.Fatalf("second writer collided on messages_pkey instead of converging: %v", err)
		}
		t.Fatalf("second writer failed: %v", err)
	}

	if result.MessageID != preallocatedID {
		t.Errorf("MessageID = %q, want the pre-allocated id %q", result.MessageID, preallocatedID)
	}

	// One turn, one row.
	msgs, err := h.repo.ListMessages(ctx, h.chatID, db.MessageListOptions{Thread: &thread.ID})
	if err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}
	assistantCount := 0
	for _, m := range msgs {
		if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("assistant message count = %d, want 1 (the turn must converge on one row)", assistantCount)
	}
}
