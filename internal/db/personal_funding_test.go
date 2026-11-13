// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// chat_personal_funding: one question per chat, answered by its owner only
// (org-provider-accounts §10.2).
func TestPersonalFunding_OneQuestionPerChat(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := seedChatForRun(t, repo, ctx, "alice")
	now := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)

	q := core.PersonalFunding{ChatID: chatID, UserID: "alice", OrgID: "org-acme", Reason: core.PersonalFundingLimited,
		Model: "claude-5.5-opus", PersonalProvider: "claude", PersonalAccountLabel: "alice@example.com", OrgResetAt: &reset}
	asked, err := repo.AskPersonalFunding(ctx, q, now)
	require.NoError(t, err)
	assert.True(t, asked)

	// Another thread meeting the same case shares the question.
	asked, err = repo.AskPersonalFunding(ctx, q, now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, asked)
	got, err := repo.GetChatPersonalFunding(ctx, chatID)
	require.NoError(t, err)
	assert.Equal(t, core.PersonalFundingAsked, got.State)
	assert.True(t, got.AskedAt.Equal(now), "the first ask's time stands")

	// Only the chat's owner answers.
	_, err = repo.AnswerPersonalFunding(ctx, chatID, "mallory", core.PersonalFundingUsePersonal, now)
	assert.ErrorIs(t, err, ErrNoPersonalFundingQuestion)

	// keep_waiting holds; the same case does not ask again.
	ans, err := repo.AnswerPersonalFunding(ctx, chatID, "alice", core.PersonalFundingKeepWaiting, now)
	require.NoError(t, err)
	assert.Equal(t, core.PersonalFundingKeepWaiting, ans.State)
	asked, err = repo.AskPersonalFunding(ctx, q, now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.False(t, asked)

	// use_personal on a limited question leases until the org's reset.
	ans, err = repo.AnswerPersonalFunding(ctx, chatID, "alice", core.PersonalFundingUsePersonal, now)
	require.NoError(t, err)
	require.NotNil(t, ans.LeaseUntil)
	assert.True(t, ans.LeaseUntil.Equal(reset))
	assert.True(t, ans.LeaseActive(now, ""))
	assert.False(t, ans.LeaseActive(reset, ""), "the lease ends at the reset")

	// Announced once.
	marked, err := repo.MarkPersonalFundingAnnounced(ctx, chatID)
	require.NoError(t, err)
	assert.True(t, marked)
	marked, err = repo.MarkPersonalFundingAnnounced(ctx, chatID)
	require.NoError(t, err)
	assert.False(t, marked)

	// A different case asks anew.
	q.Reason, q.Model = core.PersonalFundingUnserved, "gpt-5.6-sol"
	asked, err = repo.AskPersonalFunding(ctx, q, now.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, asked)

	cleared, err := repo.ClearPersonalFunding(ctx, chatID)
	require.NoError(t, err)
	assert.True(t, cleared)
	got, err = repo.GetChatPersonalFunding(ctx, chatID)
	require.NoError(t, err)
	assert.Nil(t, got)
	_, err = repo.AnswerPersonalFunding(ctx, chatID, "alice", core.PersonalFundingUsePersonal, now)
	assert.ErrorIs(t, err, ErrNoPersonalFundingQuestion, "no open question, nothing to answer")
}

// LeaseActive per reason.
func TestPersonalFunding_LeaseActive(t *testing.T) {
	t.Parallel()
	now := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	var none *core.PersonalFunding
	assert.False(t, none.LeaseActive(now, "m"))
	assert.False(t, (&core.PersonalFunding{State: core.PersonalFundingAsked, Reason: core.PersonalFundingUnusable}).LeaseActive(now, "m"))
	assert.True(t, (&core.PersonalFunding{State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingUnusable}).LeaseActive(now, "m"))
	limited := &core.PersonalFunding{State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingLimited, LeaseUntil: &until}
	assert.True(t, limited.LeaseActive(now, ""))
	assert.False(t, limited.LeaseActive(until, ""))
	unserved := &core.PersonalFunding{State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingUnserved, Model: "m"}
	assert.True(t, unserved.LeaseActive(now, "m"))
	assert.False(t, unserved.LeaseActive(now, "other"), "changing the model ends it")
}
