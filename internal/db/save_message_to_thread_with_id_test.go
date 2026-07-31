// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Delta identity: producers without a Temporal workflow (e.g. discuss mode)
// mint the assistant message id before streaming and must persist under it.

func TestSaveMessageToThreadWithID(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-fixed-id-test"
	createActivityTestChat(t, repo, chatID)
	thread := chatID // root thread id equals chat id

	t.Run("honors provided message id", func(t *testing.T) {
		fixedID := "discuss-preallocated-id"
		msg, err := repo.SaveMessageToThreadWithID(ctx, chatID, thread,
			int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), "streamed reply", nil, nil, nil, fixedID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.ID != fixedID {
			t.Errorf("message ID = %q, want %q", msg.ID, fixedID)
		}
		got, err := repo.GetMessage(ctx, fixedID)
		if err != nil || got == nil {
			t.Fatalf("message not persisted under fixed id: %v", err)
		}
	})

	t.Run("empty id falls back to uuid", func(t *testing.T) {
		msg, err := repo.SaveMessageToThreadWithID(ctx, chatID, thread,
			int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT), "no fixed id", nil, nil, nil, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.ID == "" {
			t.Error("expected generated uuid id")
		}
	})

	t.Run("SaveMessageToThread delegates with empty id", func(t *testing.T) {
		msg, err := repo.SaveMessageToThread(ctx, chatID, thread,
			int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "legacy path", nil, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.ID == "" {
			t.Error("expected generated uuid id")
		}
	})
}
