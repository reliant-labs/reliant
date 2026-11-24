// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/llmusage"
	"github.com/reliant-labs/reliant/internal/store"
)

// LLMCallUsage is one model call's counts: tokens, model, provider, auth mode,
// outcome and latency. It has no field for prompt, reply, tool I/O or error
// text, and must not gain one (TestLLMUsageTablesHoldNoContent).
type LLMCallUsage = store.LLMCallUsageRecord

// LLMUsageDay is one rollup row: a UTC day's calls of one user, org, provider,
// model and auth mode.
type LLMUsageDay = store.LlmCallUsageDaily

// LLMCallUsageRetention is how long the per-call rows live. The daily rollup
// is kept; docs/help/privacy.mdx states this number.
const LLMCallUsageRetention = llmusage.Retention

// ListLLMUsageDaily returns the user's rollup rows for the UTC days from..to
// inclusive, oldest first. This is what the usage API reads; it only ever
// returns the caller's own rows.
func (r *Repo) ListLLMUsageDaily(ctx context.Context, userID string, from, to time.Time) ([]*LLMUsageDay, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	rows, err := store.ListLLMUsageDaily(ctx, r.ORM(), userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list llm usage: %w", err)
	}
	return rows, nil
}

// LLMUsageDetail is the user's per-call rows summed per UTC day and every
// recorded dimension, funding and account included.
type LLMUsageDetail = store.LLMUsageDetail

// LLMOrgUsageDay is one row of an org's rollup of ORG-FUNDED calls.
type LLMOrgUsageDay = store.LlmOrgUsageDaily

// OrgKeyModelUsage is the tokens one model used through an org's own API keys.
type OrgKeyModelUsage = store.OrgKeyModelUsage

// ListLLMUsageDetail returns the user's per-call rows for the UTC days
// from..to inclusive, summed per day and dimension (funding and account
// included). Days older than LLMCallUsageRetention are partly or wholly gone.
// It only ever returns the caller's own rows.
func (r *Repo) ListLLMUsageDetail(ctx context.Context, userID string, from, to time.Time) ([]*LLMUsageDetail, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	rows, err := store.ListLLMUsageDetail(ctx, r.ORM(), userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list llm usage detail: %w", err)
	}
	return rows, nil
}

// ListLLMOrgUsageDaily returns orgID's rollup of org-funded calls for the
// UTC days from..to inclusive. It is the only table an org admin's usage read
// touches, and it never holds a personal-account call.
func (r *Repo) ListLLMOrgUsageDaily(ctx context.Context, orgID string, from, to time.Time) ([]*LLMOrgUsageDay, error) {
	rows, err := store.ListLLMOrgUsageDaily(ctx, r.ORM(), orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list org llm usage: %w", err)
	}
	return rows, nil
}

// ChatLLMCall is one model call of a chat, as llm_call_usage holds it.
type ChatLLMCall = store.LlmCallUsage

// ChatLLMCallQuery selects a chat's model calls (ListChatLLMCalls).
type ChatLLMCallQuery = store.ChatLLMCallQuery

// ListChatLLMCalls is one chat's model calls, oldest first, for its
// Trace/Cost tab. It does not decide who may read them: the caller has.
func (r *Repo) ListChatLLMCalls(ctx context.Context, q ChatLLMCallQuery) ([]*ChatLLMCall, error) {
	return store.ListChatLLMCalls(ctx, r.ORM(), q)
}

// OrgChatCall is one of an org's live chats' latest chat-turn model call.
type OrgChatCall = store.OrgChatCall

// LatestOrgChatCalls is, for each of orgID's live chats that made a
// chat-turn call at or after since, its latest one, the limit most recent:
// what a proposed org policy judges it on (GetPolicyImpact).
func (r *Repo) LatestOrgChatCalls(ctx context.Context, orgID string, since time.Time, limit int) ([]OrgChatCall, error) {
	if orgID == "" {
		return nil, fmt.Errorf("org ID cannot be empty")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	return store.LatestOrgChatCalls(ctx, r.ORM(), orgID, since, limit)
}
