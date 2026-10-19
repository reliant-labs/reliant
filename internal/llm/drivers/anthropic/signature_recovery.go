// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A thinking block carries a signature only the account (or model family) that
// produced it can verify. When a chat's history crosses accounts and the API
// does not silently drop the foreign block, the request 400s with
// "Invalid `signature` in `thinking` block". The history is still good — only
// the replayed reasoning is unusable — so the request is retried once with
// every thinking / redacted_thinking block removed. Removing all of them is
// valid: removal from the start, the end, or everywhere keeps the prefix rules.
//
// Only the request is rewritten. Stored history is never touched.

// isInvalidSignatureError reports whether err is the API refusing a replayed
// thinking block it cannot verify.
func isInvalidSignatureError(err error) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	text := strings.ToLower(apiErr.RawJSON())
	return strings.Contains(text, "invalid `signature` in `thinking` block") ||
		(strings.Contains(text, "invalid `data` in `redacted_thinking` block"))
}

// withoutThinkingBlocks returns params with every thinking and
// redacted_thinking block removed from its messages, and how many were
// removed. It copies on write: the caller's params and the message slices
// behind them are not modified. An assistant message left with no blocks is
// dropped (the API rejects an empty content list).
func withoutThinkingBlocks(params anthropic.MessageNewParams) (anthropic.MessageNewParams, int) {
	removed := 0
	msgs := make([]anthropic.MessageParam, 0, len(params.Messages))
	for _, m := range params.Messages {
		kept := make([]anthropic.ContentBlockParamUnion, 0, len(m.Content))
		for _, blk := range m.Content {
			if blk.OfThinking != nil || blk.OfRedactedThinking != nil {
				removed++
				continue
			}
			kept = append(kept, blk)
		}
		if len(kept) == len(m.Content) {
			msgs = append(msgs, m)
			continue
		}
		if len(kept) == 0 {
			continue
		}
		m.Content = kept
		msgs = append(msgs, m)
	}
	if removed == 0 {
		return params, 0
	}
	params.Messages = msgs
	return params, removed
}

// recoverInvalidSignature decides whether the failed request should be
// retried without its thinking blocks. When it returns true, params has been
// replaced by the stripped request and *done is set, so a second signature
// failure surfaces instead of looping.
func (b *baseClient) recoverInvalidSignature(err error, params *anthropic.MessageNewParams, done *bool) bool {
	if *done || !isInvalidSignatureError(err) {
		return false
	}
	stripped, removed := withoutThinkingBlocks(*params)
	if removed == 0 {
		return false
	}
	*done = true
	*params = stripped
	chatID := ""
	if b.options.SessionID != nil {
		chatID = *b.options.SessionID
	}
	logging.Warn("Anthropic rejected a replayed thinking signature; retrying once without thinking blocks",
		"chat_id", chatID, "removed_blocks", removed, "error", err)
	return true
}

// newMessage is Messages.New with the same one-shot signature recovery the
// streaming path has.
func (b *baseClient) newMessage(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	msg, err := b.client.Messages.New(ctx, params)
	var done bool
	if err != nil && b.recoverInvalidSignature(err, &params, &done) {
		return b.client.Messages.New(ctx, params)
	}
	return msg, err
}
