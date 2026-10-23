// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/messageconv"
)

// TestSaveMessage_ProviderAccountRoundTrip walks the persistence leg of
// per-account reasoning provenance against a real Postgres: SaveMessage writes
// the serving account onto messages.provider_account_id, and the history load
// (messageconv) hands it back as message.Message.ProviderAccountID — the value
// stripForeignReasoning compares against the account a request is going to.
func TestSaveMessage_ProviderAccountRoundTrip(t *testing.T) {
	t.Parallel()

	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()
	thread, _ := h.createThread("account-thread", h.chatID)

	load := func(t *testing.T) map[string]message.Message {
		t.Helper()
		msgs, err := h.svc.LoadCurrentMessages(ctx, thread.ID)
		if err != nil {
			t.Fatalf("LoadCurrentMessages: %v", err)
		}
		converted, err := messageconv.DbMessagesToMessages(ctx, msgs, h.repo)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		byID := map[string]message.Message{}
		for _, m := range converted {
			byID[m.ID] = m
		}
		return byID
	}

	served, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID: h.chatID, Thread: thread.ID,
		Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content: "Done.", ProviderAccountID: "acct-a",
	})
	if err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	unrecorded, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
		ChatID: h.chatID, Thread: thread.ID,
		Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content: "From before accounts were recorded.",
	})
	if err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	got := load(t)
	if g := got[served.MessageID].ProviderAccountID; g != "acct-a" {
		t.Errorf("served message ProviderAccountID = %q, want acct-a", g)
	}
	if g := got[unrecorded.MessageID].ProviderAccountID; g != "" {
		t.Errorf("unrecorded message ProviderAccountID = %q, want empty (never invented)", g)
	}
}
