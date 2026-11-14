// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// LatestOrgChatCalls is what GetPolicyImpact judges a proposed org policy on:
// each of the org's live chats' latest chat-turn call in the window, and
// whether a stored trigger launched the chat.
func TestLatestOrgChatCalls(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
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

	trigger := newTestTrigger("trg-impact", "u1", "test-project", "nightly")
	require.NoError(t, repo.CreateTrigger(ctx, trigger))
	ev := newTestTriggerEvent("ev-impact", trigger.ID, "u1", "impact-1", now)
	ev.Outcome, ev.ChatID = core.TriggerEventLaunched, strPtrForTest("c-auto")
	_, err := repo.CreateTriggerEvent(ctx, ev)
	require.NoError(t, err)

	got, err := repo.LatestOrgChatCalls(ctx, acme, now.Add(-14*24*time.Hour))
	require.NoError(t, err)
	sort.Slice(got, func(i, j int) bool { return got[i].ChatID < got[j].ChatID })
	assert.Equal(t, []OrgChatCall{
		{ChatID: "c-auto", Provider: "codex", Model: "claude-4.8-opus", Funding: "personal", Automation: true},
		{ChatID: "c-live", Provider: "anthropic", Model: "claude-4.8-opus", Funding: "org_key"},
	}, got, "the latest chat turn of each live chat of the org in the window; titles, archived chats, other orgs and idle chats are not counted")

	_, err = repo.LatestOrgChatCalls(ctx, "", now)
	assert.Error(t, err)
}

func strPtrForTest(s string) *string { return &s }
