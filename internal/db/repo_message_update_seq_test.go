package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// A live message update must carry the chat-global seq the client sorts by.
// Without it the message deserializes at seq 0 and renders at the top of the
// transcript instead of the bottom, which presents as "my message never showed
// up" even though it was delivered.
func TestEnrichMessageUpdate_CarriesSeq(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-seq-payload"
	createActivityTestChat(t, repo, chatID)

	if _, err := repo.SaveMessageToThread(ctx, chatID, chatID, 1, "hello", nil, nil, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Save a second message so seq is non-zero and thus distinguishable from
	// an absent field defaulting to 0.
	msg, err := repo.SaveMessageToThread(ctx, chatID, chatID, 1, "second", nil, nil, nil)
	if err != nil {
		t.Fatalf("save 2: %v", err)
	}
	if msg.Seq == 0 {
		t.Fatal("second message should not have seq 0")
	}

	raw, _ := json.Marshal(map[string]any{"id": msg.ID})
	enriched, err := repo.EnrichMessageUpdate(ctx, ChatUpdate{
		Data: raw, EntityID: msg.ID, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(enriched, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	seq, ok := got["seq"]
	if !ok {
		t.Fatal("live message update has no `seq` field — the client sorts by seq, so this message would render at position 0")
	}
	if int64(seq.(float64)) != msg.Seq {
		t.Fatalf("seq = %v, want %d", seq, msg.Seq)
	}
}
