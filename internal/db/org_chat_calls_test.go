// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// LatestOrgChatCalls is what GetPolicyImpact judges a proposed org policy on:
// each of the org's live chats' latest chat-turn call in the window, the most
// recently active first, up to the limit.
func TestLatestOrgChatCalls(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	// A fixed time: every fixture and the window are relative to it.
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	acme, globex := "org-acme", "org-globex"

	chat := func(id string, org *string, state ChatState) {
		require.NoError(t, repo.CreateChat(ctx, &Chat{ID: id, Title: id, ProjectID: "test-project", UserID: "u1", OrgID: org,
			State: state, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	}
	call := func(id, chatID, org, provider, funding, purpose string, at time.Time) {
		rec := usageRec(id, at)
		rec.ChatID, rec.OrgID, rec.Provider, rec.Funding, rec.Purpose = chatID, org, provider, funding, purpose
		_, err := repo.RecordLLMCallUsage(ctx, rec)
		require.NoError(t, err)
	}
	chat("c-live", &acme, ChatStateIdle)
	chat("c-auto", &acme, ChatStateIdle)
	chat("c-archived", &acme, ChatStateArchived)
	chat("c-other-org", &globex, ChatStateIdle)
	chat("c-stale", &acme, ChatStateIdle)

	call("live/1", "c-live", acme, "claude", "personal", store.LLMPurposeChat, now.Add(-2*time.Hour))
	call("live/2", "c-live", acme, "anthropic", "org_key", store.LLMPurposeChat, now.Add(-time.Hour))
	call("live/title", "c-live", acme, "reliant", "org_wallet", store.LLMPurposeTitle, now.Add(-time.Minute))
	call("auto/1", "c-auto", acme, "codex", "personal", store.LLMPurposeChat, now.Add(-time.Hour))
	call("archived/1", "c-archived", acme, "claude", "personal", store.LLMPurposeChat, now.Add(-time.Hour))
	call("other/1", "c-other-org", globex, "claude", "personal", store.LLMPurposeChat, now.Add(-time.Hour))
	call("stale/1", "c-stale", acme, "claude", "personal", store.LLMPurposeChat, now.Add(-30*24*time.Hour))

	got, err := repo.LatestOrgChatCalls(ctx, acme, now.Add(-14*24*time.Hour), 10)
	require.NoError(t, err)
	assert.Equal(t, []OrgChatCall{
		{ChatID: "c-auto", Provider: "codex", Model: "claude-4.8-opus", Funding: "personal"},
		{ChatID: "c-live", Provider: "anthropic", Model: "claude-4.8-opus", Funding: "org_key"},
	}, sortedByChat(got), "the latest chat turn of each live chat of the org in the window; titles, archived chats, other orgs and idle chats are not counted")

	// The limit keeps the most recently active chats.
	call("auto/2", "c-auto", acme, "codex", "personal", store.LLMPurposeChat, now.Add(-time.Minute))
	got, err = repo.LatestOrgChatCalls(ctx, acme, now.Add(-14*24*time.Hour), 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "c-auto", got[0].ChatID)

	_, err = repo.LatestOrgChatCalls(ctx, "", now, 1)
	assert.Error(t, err)
	_, err = repo.LatestOrgChatCalls(ctx, acme, now, 0)
	assert.Error(t, err)
}

func sortedByChat(rows []OrgChatCall) []OrgChatCall {
	sort.Slice(rows, func(i, j int) bool { return rows[i].ChatID < rows[j].ChatID })
	return rows
}
