// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A chat's background calls — its title, its compaction summary, an
// automation drafted in an org's project — follow §10 read-only: the org's
// accounts first; the person's own only where consent is already on record
// (their setting or order, a lease, their pick); never a question, and the
// chat's question is never touched. The owner's decision after reliant#2723's
// browser pass, whose chat titled itself three times on the member's own key
// while the org's was limited and the member had said nothing.

// fundOrgChat is a person's chat (chat-1) in fundOrg.
func fundOrgChat() *db.Chat {
	org := fundOrg
	return &db.Chat{ID: "chat-1", UserID: "u1", OrgID: &org, LaunchKind: string(core.TriggerEventKindChatStart)}
}

// backgroundEnv is a funding env whose drivers answer a title, recording the
// accounts a driver is built for.
func backgroundEnv(t *testing.T, onLimit orgfunding.OnOrgLimit) (*fundingEnv, *titleAccounts) {
	t.Helper()
	env := newFundingEnv(t, onLimit, "")
	accounts := &titleAccounts{titleOf: "Billing Page Copy"}
	env.routing.DriverFor = func(ctx context.Context, userID string, cfg models.DriverConfig, m models.Model, opts ...llm.DriverOption) (llm.Driver, error) {
		env.built = append(env.built, cfg.AccountID)
		return accounts.driverFor(ctx, userID, cfg, m, opts...)
	}
	return env, accounts
}

// The org's Anthropic key is limited; the person has their own Claude and
// Codex accounts; the org gave nothing else.
func limitedOrgWithMine(env *fundingEnv) {
	env.routing.Store.(*routerFake).bench("acme-key", time.Date(2099, 1, 2, 5, 0, 0, 0, time.UTC), "five_hour")
}

func TestBackgroundFunding_TitleNeverSpendsThePersonsAccountWithoutConsent(t *testing.T) {
	for name, row := range map[string]*core.PersonalFunding{
		"no question":  nil,
		"asked":        {State: core.PersonalFundingAsked, Reason: core.PersonalFundingLimited},
		"keep waiting": {State: core.PersonalFundingKeepWaiting, Reason: core.PersonalFundingLimited},
	} {
		t.Run(name, func(t *testing.T) {
			withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0)), myCodex("my-codex")))
			env, accounts := backgroundEnv(t, "")
			limitedOrgWithMine(env)
			if row != nil {
				row.ChatID, row.UserID, row.OrgID, row.Model = "chat-1", "u1", fundOrg, routeModel
				env.store.rows["chat-1"] = row
			}
			before := env.store.row("chat-1")

			_, err := NewGenerateTitleActivity(nil, nil).WithAccountRouting(env.routing).generateTitleForChat(context.Background(), fundOrgChat(), "rename the billing page")
			var refused *personalFundingRefused
			require.ErrorAs(t, err, &refused, "only the person's own accounts could title it")
			assert.Empty(t, env.built, "no driver on any account")
			assert.Empty(t, accounts.calls())
			assert.Zero(t, env.store.asks, "a title never asks")
			assert.Equal(t, before, env.store.row("chat-1"), "and never touches the chat's question")
		})
	}
}

func TestBackgroundFunding_TitleUsesThePersonsAccountWithConsent(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name    string
		onLimit orgfunding.OnOrgLimit
		order   orgfunding.Order
		row     *core.PersonalFunding
	}{
		{name: "their setting: use my accounts", onLimit: orgfunding.OnOrgLimitUsePersonal},
		{name: "their order: my accounts first", order: orgfunding.OrderPersonalFirst},
		{name: "an unexpired answer", row: &core.PersonalFunding{State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingLimited, LeaseUntil: &future}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0)), myCodex("my-codex")))
			env, accounts := backgroundEnv(t, tc.onLimit)
			if tc.order != "" {
				env.settings[orgfunding.SettingKey] = orgfunding.Settings{}.With(fundOrg, orgfunding.Preference{Order: tc.order}, nil).Value()
			}
			limitedOrgWithMine(env)
			if tc.row != nil {
				tc.row.ChatID, tc.row.UserID, tc.row.OrgID, tc.row.Model = "chat-1", "u1", fundOrg, routeModel
				env.store.rows["chat-1"] = tc.row
			}
			before := env.store.row("chat-1")

			title, err := NewGenerateTitleActivity(nil, nil).WithAccountRouting(env.routing).generateTitleForChat(context.Background(), fundOrgChat(), "rename the billing page")
			require.NoError(t, err)
			assert.Equal(t, "Billing Page Copy", title)
			require.NotEmpty(t, accounts.calls())
			assert.NotContains(t, accounts.calls(), "acme-key", "the limited org key is never called")
			assert.Zero(t, env.store.asks)
			assert.Equal(t, before, env.store.row("chat-1"), "a lease is neither written nor announced by a title")
		})
	}
}

// The org's key serves: the title is the org's, and the chat's open question
// (its turn's to settle, and to announce) is left alone.
func TestBackgroundFunding_TitleOnTheOrgLeavesTheQuestion(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env, accounts := backgroundEnv(t, "")
	env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: "u1", OrgID: fundOrg,
		State: core.PersonalFundingKeepWaiting, Reason: core.PersonalFundingUnserved, Model: "gpt-5.6-sol"}
	_, err := NewGenerateTitleActivity(nil, nil).WithAccountRouting(env.routing).generateTitleForChat(context.Background(), fundOrgChat(), "rename the billing page")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme-key"}, accounts.calls())
	require.NotNil(t, env.store.row("chat-1"), "only the turn ends the episode")
	assert.Equal(t, core.PersonalFundingKeepWaiting, env.store.row("chat-1").State)
}

// A compaction follows the turn's decision without making it: under a lease it
// summarizes on the person's account; with no consent it is refused (the
// activity then skips it, and the turn asks); it never asks or announces.
func TestBackgroundFunding_CompactionFollowsTheTurn(t *testing.T) {
	history := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
	summarizeWith := &reliantv1.ModelSelector{Id: routeModel}

	t.Run("no consent: refused, nothing asked", func(t *testing.T) {
		withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
		env, _ := backgroundEnv(t, "")
		limitedOrgWithMine(env)
		a := &CompactActivity{routing: env.routing}
		_, err := a.generateCompactionSummary(context.Background(), fundOrgChat(), "root", "", types.FailoverPolicy{}, history, summarizeWith)
		var refused *personalFundingRefused
		require.ErrorAs(t, err, &refused)
		assert.Empty(t, env.built)
		assert.Nil(t, env.store.row("chat-1"), "the turn asks, not the compaction")
	})
	t.Run("under the chat's lease: on the person's account, the notice left to the turn", func(t *testing.T) {
		withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
		env, _ := backgroundEnv(t, "")
		limitedOrgWithMine(env)
		future := time.Now().Add(time.Hour)
		env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: "u1", OrgID: fundOrg,
			State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingLimited, Model: routeModel, LeaseUntil: &future}
		a := &CompactActivity{routing: env.routing}
		_, err := a.generateCompactionSummary(context.Background(), fundOrgChat(), "root", "", types.FailoverPolicy{}, history, summarizeWith)
		var refused *personalFundingRefused
		assert.False(t, errors.As(err, &refused), "consent on record: %v", err)
		assert.Equal(t, []string{"my-claude"}, env.built)
		assert.False(t, env.store.row("chat-1").Announced, "the turn announces the lease, not the compaction")
	})
}

// The thread is on the person's own account from an answer whose lease has
// ended (a turn moves on mid-turn, §4.5): a compaction is no tool step of that
// turn, so the account the thread is on is no consent. It is refused, and the
// chat's question is untouched.
func TestBackgroundFunding_CompactionOnAThreadServedByAnEndedLease(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env, _ := backgroundEnv(t, "")
	limitedOrgWithMine(env)
	ended := time.Now().Add(-time.Minute)
	env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: "u1", OrgID: fundOrg,
		State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingLimited, Model: routeModel, LeaseUntil: &ended, Announced: true}
	env.store.serving["root"] = "my-claude"
	env.store.threadFunding["root"] = [3]string{models.FundingPersonal, fundOrg, "my-claude"}
	env.routing.Store.(*routerFake).assignments["root|claude"] = "my-claude"
	before := env.store.row("chat-1")

	a := &CompactActivity{routing: env.routing}
	history := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
	_, err := a.generateCompactionSummary(context.Background(), fundOrgChat(), "root", "", types.FailoverPolicy{}, history, &reliantv1.ModelSelector{Id: routeModel})
	var refused *personalFundingRefused
	require.ErrorAs(t, err, &refused)
	assert.Empty(t, env.built, "no provider call on the person's account")
	assert.Equal(t, before, env.store.row("chat-1"))
	assert.Zero(t, env.store.asks)
}

// An automation drafted in an org's project is scoped to the org: with no
// consent and only the person's own account able to draft, it fails at once
// (not retried) and says why; with their setting, their account drafts it.
func TestBackgroundFunding_DraftInAnOrgsProject(t *testing.T) {
	input := DraftAutomationInput{UserID: "u1", OrgID: fundOrg, Description: "every hour, summarise failed CI"}

	withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env, _ := backgroundEnv(t, "")
	limitedOrgWithMine(env)
	_, err := NewDraftAutomationActivity(nil).WithAccountRouting(env.routing).Execute(context.Background(), input)
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, DraftNeedsConsentErrorType, appErr.Type())
	assert.True(t, appErr.NonRetryable())
	assert.Empty(t, env.built)

	env, _ = backgroundEnv(t, orgfunding.OnOrgLimitUsePersonal)
	limitedOrgWithMine(env)
	_, _ = NewDraftAutomationActivity(nil).WithAccountRouting(env.routing).Execute(context.Background(), input)
	assert.Equal(t, []string{"my-claude"}, env.built, "their setting is consent")
	assert.Nil(t, env.store.row("chat-1"))
}
