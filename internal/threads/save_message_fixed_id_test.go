package threads

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Delta identity: the workflow pre-allocates the assistant message id and the
// save path must honor it, so streamed deltas (stamped with that id) and the
// persisted message line up. Empty MessageID keeps today's uuid behavior, and
// the retry delete-recreate path must KEEP the fixed id — a retry re-streams
// under the same identity.

func TestSaveMessage_FixedMessageID(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	thread, _ := h.createThread("thread-fixed-id", h.chatID)

	t.Run("honors provided message id", func(t *testing.T) {
		fixedID := "preallocated-message-id-1"
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:    h.chatID,
			Thread:    thread.ID,
			Role:      int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content:   "streamed response",
			MessageID: fixedID,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.MessageID != fixedID {
			t.Errorf("MessageID = %q, want %q", result.MessageID, fixedID)
		}
		msg, err := h.repo.GetMessage(ctx, fixedID)
		if err != nil || msg == nil {
			t.Fatalf("message not persisted under fixed id: %v", err)
		}
	})

	t.Run("empty message id falls back to uuid", func(t *testing.T) {
		result, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:  h.chatID,
			Thread:  thread.ID,
			Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
			Content: "no fixed id",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.MessageID == "" {
			t.Error("expected generated uuid message id")
		}
	})

	t.Run("retry delete-recreate keeps the fixed id", func(t *testing.T) {
		fixedID := "preallocated-message-id-retry"
		activityID := "fixed-id-retry-activity"

		result1, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:        h.chatID,
			Thread:        thread.ID,
			Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content:       "attempt 1",
			MessageID:     fixedID,
			ActivityID:    &activityID,
			AttemptNumber: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result1.MessageID != fixedID {
			t.Fatalf("attempt 1 MessageID = %q, want %q", result1.MessageID, fixedID)
		}

		// Retry (attempt > 1) deletes the incomplete message and recreates it
		// — under the SAME pre-allocated id.
		result2, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
			ChatID:        h.chatID,
			Thread:        thread.ID,
			Role:          int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
			Content:       "attempt 2",
			MessageID:     fixedID,
			ActivityID:    &activityID,
			AttemptNumber: 2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result2.WasExisting {
			t.Error("retry should recreate, not return existing")
		}
		if result2.MessageID != fixedID {
			t.Errorf("retry MessageID = %q, want fixed id %q", result2.MessageID, fixedID)
		}
	})
}
