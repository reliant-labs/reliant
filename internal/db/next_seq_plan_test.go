// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
)

// store's TestNextSeqInChatDoesNotFuseBranches pins the query SHAPE. This
// pins the VALUE: the allocator must
// still return a high-water mark spanning the fork chain, which is the whole
// reason the query is not simply MAX(seq) WHERE chat_id = $1. A branch that
// restarts at seq 0 puts every new reply numerically beneath the inherited
// history, so it renders at the top of the transcript and reads as "my message
// never arrived".
func TestGetNextSeqByChatSpansForkChain(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-seq-value"
	createActivityTestChat(t, repo, chatID)

	for i := 0; i < 5; i++ {
		if _, err := repo.SaveMessageToThread(ctx, chatID, chatID, 1, "m", nil, nil, nil); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	next, err := repo.GetNextSeq(ctx, chatID, chatID)
	if err != nil {
		t.Fatalf("GetNextSeq: %v", err)
	}
	if next < 5 {
		t.Fatalf("next seq must exceed the 5 existing messages, got %d", next)
	}

	// And it must keep advancing rather than handing out a duplicate, which
	// messages_chat_seq_key would reject.
	if _, err := repo.SaveMessageToThread(ctx, chatID, chatID, 1, "m", nil, nil, nil); err != nil {
		t.Fatalf("save after allocation: %v", err)
	}
	after, err := repo.GetNextSeq(ctx, chatID, chatID)
	if err != nil {
		t.Fatalf("GetNextSeq after: %v", err)
	}
	if after <= next {
		t.Fatalf("seq must advance after a save: was %d, now %d", next, after)
	}
}
