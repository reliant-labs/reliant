// Copyright (c) 2025 Reliant Labs

package viewertest

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// HiddenMessage is a DISPLAY_STYLE_HIDDEN message of the shared chat (what a
// compaction summary, a mailbox envelope or a sub-agent's result body is):
// it goes to the model and is never in the transcript. Its text block holds
// Secret.
type HiddenMessage struct {
	MessageID, BlockID, Secret string
}

// SeedHiddenMessage writes a hidden text message into the shared chat's
// thread, at SeededAt.
func (f *Fixture) SeedHiddenMessage(t *testing.T) HiddenMessage {
	t.Helper()
	ctx := context.Background()
	h := HiddenMessage{
		MessageID: "hidden-msg-" + f.ChatID,
		BlockID:   "hidden-block-" + f.ChatID,
		Secret:    "HIDDEN_SENTINEL_SECRET=compaction-summary-of-the-owners-files",
	}
	if err := f.Repo.CreateNumberedMessage(ctx, &db.Message{
		ID: h.MessageID, ChatID: f.ChatID, ThreadID: f.Transcript.ThreadID, ContextWindowID: f.Transcript.ContextWindowID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, DisplayStyle: reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN.Enum(),
		CreatedAt: SeededAt, UpdatedAt: SeededAt,
	}); err != nil {
		t.Fatalf("viewertest: hidden message: %v", err)
	}
	if err := f.Repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID: h.BlockID, MessageID: h.MessageID, Position: 0, Version: ptr.Of(1),
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of(h.Secret),
		CreatedAt: SeededAt, UpdatedAt: SeededAt,
	}); err != nil {
		t.Fatalf("viewertest: hidden block: %v", err)
	}
	return h
}
