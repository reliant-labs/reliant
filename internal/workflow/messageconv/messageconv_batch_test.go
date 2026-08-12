// Copyright (c) 2025 Reliant Labs
package messageconv

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// countingRepo records how many queries the conversion issues. Only the methods
// the conversion path touches are implemented; anything else nil-panics loudly
// rather than silently returning a zero value.
type countingRepo struct {
	db.Repository

	blocks   map[string][]*db.MessageContentBlock
	windows  map[string]*db.ContextWindow
	perMsg   int
	batched  int
	windowed int
}

func (r *countingRepo) ListContentBlocks(_ context.Context, messageID string) ([]*db.MessageContentBlock, error) {
	r.perMsg++
	return r.blocks[messageID], nil
}

func (r *countingRepo) ListContentBlocksForMessages(_ context.Context, messageIDs []string) ([]*db.MessageContentBlock, error) {
	r.batched++
	var out []*db.MessageContentBlock
	for _, id := range messageIDs {
		out = append(out, r.blocks[id]...)
	}
	return out, nil
}

func (r *countingRepo) GetContextWindow(_ context.Context, id string) (*db.ContextWindow, error) {
	r.windowed++
	return r.windows[id], nil
}

func textBlock(messageID, text string, position int) *db.MessageContentBlock {
	return &db.MessageContentBlock{
		ID:        messageID + "-b",
		MessageID: messageID,
		Position:  position,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Content:   &text,
	}
}

func newFixture(messageCount int) ([]*db.Message, *countingRepo) {
	repo := &countingRepo{
		blocks:  map[string][]*db.MessageContentBlock{},
		windows: map[string]*db.ContextWindow{"cw-1": {ID: "cw-1", Sequence: 7}},
	}
	msgs := make([]*db.Message, 0, messageCount)
	for i := 0; i < messageCount; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs, &db.Message{
			ID:              id,
			ChatID:          "chat-1",
			Ordinal:         int64(i),
			ContextWindowID: "cw-1",
			Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		})
		repo.blocks[id] = []*db.MessageContentBlock{textBlock(id, "hello "+id, 0)}
	}
	return msgs, repo
}

// Converting a thread must not scale round trips with message count: holding the
// activity context open across hundreds of sequential queries is what turned a
// single cancellation into a failed history load.
func TestDbMessagesToMessagesBatchesQueries(t *testing.T) {
	msgs, repo := newFixture(5)

	got, err := DbMessagesToMessages(context.Background(), msgs, repo)
	if err != nil {
		t.Fatalf("DbMessagesToMessages() error = %v", err)
	}

	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5", len(got))
	}
	if repo.perMsg != 0 {
		t.Errorf("per-message ListContentBlocks called %d times, want 0", repo.perMsg)
	}
	if repo.batched != 1 {
		t.Errorf("ListContentBlocksForMessages called %d times, want 1", repo.batched)
	}
	// All five messages share one context window, so it resolves once.
	if repo.windowed != 1 {
		t.Errorf("GetContextWindow called %d times, want 1", repo.windowed)
	}
}

// The batched path must produce exactly what the per-message path produced.
func TestDbMessagesToMessagesMatchesPerMessage(t *testing.T) {
	msgs, repo := newFixture(3)

	batched, err := DbMessagesToMessages(context.Background(), msgs, repo)
	if err != nil {
		t.Fatalf("DbMessagesToMessages() error = %v", err)
	}

	for i, dbMsg := range msgs {
		want, err := DbMessageToMessage(context.Background(), dbMsg, repo)
		if err != nil {
			t.Fatalf("DbMessageToMessage() error = %v", err)
		}
		got := batched[i]

		if got.ID != want.ID || got.Role != want.Role || got.Ordinal != want.Ordinal {
			t.Errorf("message %d identity = %+v, want %+v", i, got, want)
		}
		if got.ContextSequence != want.ContextSequence {
			t.Errorf("message %d ContextSequence = %d, want %d", i, got.ContextSequence, want.ContextSequence)
		}
		if len(got.Parts) != len(want.Parts) {
			t.Fatalf("message %d has %d parts, want %d", i, len(got.Parts), len(want.Parts))
		}
		if got.Content() != want.Content() {
			t.Errorf("message %d content = %q, want %q", i, got.Content(), want.Content())
		}
	}
}

func TestDbMessagesToMessagesEmpty(t *testing.T) {
	repo := &countingRepo{}
	got, err := DbMessagesToMessages(context.Background(), nil, repo)
	if err != nil {
		t.Fatalf("DbMessagesToMessages() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d messages, want 0", len(got))
	}
	if repo.batched != 0 {
		t.Errorf("issued %d queries for an empty batch, want 0", repo.batched)
	}
}
