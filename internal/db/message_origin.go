// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// MessageOrigin is trusted in-process attribution, defined in core.
type MessageOrigin = core.MessageOrigin

// WithMessageOrigin returns ctx carrying trusted attribution.
func WithMessageOrigin(ctx context.Context, origin MessageOrigin) context.Context {
	return core.WithMessageOrigin(ctx, origin)
}

// senderTitle is store.SenderTitlesByThreadIDs for one thread, nil when it resolves to none.
// Best-effort: an untitled sender degrades a card's heading, never the
// transcript, so a failed lookup is no title rather than an error.
func (r *Repo) senderTitle(ctx context.Context, threadID string) *string {
	titles, err := store.SenderTitlesByThreadIDs(ctx, r.ORM(), []string{threadID})
	if err != nil {
		return nil
	}
	title, ok := titles[threadID]
	if !ok {
		return nil
	}
	return &title
}

// MessageOriginFrom reports the origin ctx carries, if any.
func MessageOriginFrom(ctx context.Context) (MessageOrigin, bool) { return core.MessageOriginFrom(ctx) }

// WithLLMOnlyDelivery marks a user message as hidden from the transcript.
func WithLLMOnlyDelivery(ctx context.Context) context.Context { return core.WithLLMOnlyDelivery(ctx) }

// LLMOnlyDelivery reports whether ctx is an LLM-only delivery.
func LLMOnlyDelivery(ctx context.Context) bool { return core.LLMOnlyDelivery(ctx) }

// QueuedDisplayStyle returns the display style for a model-only delivery.
func QueuedDisplayStyle(ctx context.Context) *int32 { return core.QueuedDisplayStyle(ctx) }
