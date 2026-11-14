// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// §10 of control-plane docs/design/org-provider-accounts.md on the server:
// before a chat in an org moves from the org's accounts to the person's own,
// it asks; with nobody to answer it waits; nothing but the person's answer (or
// their remembered preference) spends their money.

// fundingFake is an in-memory PersonalFundingStore with the row semantics of
// the real one (one question per chat).
type fundingFake struct {
	mu          sync.Mutex
	rows        map[string]*core.PersonalFunding
	asks        int
	assignments map[string][]core.Assignment
	orgs        map[string][2]string // account -> {owner org, attributed org}
	serving     map[string]string    // thread -> account
	// threadFunding is who pays for each thread: {funding, org, account}.
	threadFunding map[string][3]string
	// threadFundingErr fails ThreadFunding.
	threadFundingErr error
	// chatOrder is the order a person chose for each chat.
	chatOrder map[string]string
	// recordErr fails RecordThreadFunding.
	recordErr error
	// chatOrderErr fails ChatFundingOrder.
	chatOrderErr error
	// personPick is the provider each chat's owner picked, by org:
	// chat -> {org, driver}.
	personPick map[string][2]string
	// personPickErr fails ChatPersonPickDriver.
	personPickErr error
}

func newFundingFake() *fundingFake {
	return &fundingFake{rows: map[string]*core.PersonalFunding{}, assignments: map[string][]core.Assignment{},
		orgs: map[string][2]string{}, serving: map[string]string{}, threadFunding: map[string][3]string{}, chatOrder: map[string]string{}}
}

func (f *fundingFake) GetChatPersonalFunding(_ context.Context, chatID string) (*core.PersonalFunding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[chatID]; ok {
		c := *r
		return &c, nil
	}
	return nil, nil
}

func (f *fundingFake) AskPersonalFunding(_ context.Context, q core.PersonalFunding, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.rows[q.ChatID]; ok && cur.State != core.PersonalFundingUsePersonal && cur.Reason == q.Reason && cur.Model == q.Model {
		return false, nil
	}
	q.State, q.AskedAt = core.PersonalFundingAsked, now
	f.rows[q.ChatID] = &q
	f.asks++
	return true, nil
}

func (f *fundingFake) GrantPersonalFunding(_ context.Context, q core.PersonalFunding, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	q.State, q.AnsweredAt = core.PersonalFundingUsePersonal, &now
	f.rows[q.ChatID] = &q
	return nil
}

func (f *fundingFake) ClearPersonalFunding(_ context.Context, chatID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[chatID]
	delete(f.rows, chatID)
	return ok, nil
}

func (f *fundingFake) MarkPersonalFundingAnnounced(_ context.Context, chatID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[chatID]
	if !ok || r.Announced || r.State != core.PersonalFundingUsePersonal {
		return false, nil
	}
	r.Announced = true
	return true, nil
}

func (f *fundingFake) ProviderAccountOrgs(_ context.Context, accountID string) (string, string, bool, error) {
	o, ok := f.orgs[accountID]
	return o[0], o[1], ok, nil
}

func (f *fundingFake) ListThreadAssignments(_ context.Context, threadID string) ([]core.Assignment, error) {
	return f.assignments[threadID], nil
}

func (f *fundingFake) LatestServingAccount(_ context.Context, threadID string) (string, error) {
	return f.serving[threadID], nil
}

func (f *fundingFake) ThreadFunding(_ context.Context, threadID string) (string, string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.threadFundingErr != nil {
		return "", "", "", false, f.threadFundingErr
	}
	c, ok := f.threadFunding[threadID]
	return c[0], c[1], c[2], ok, nil
}

func (f *fundingFake) RecordThreadFunding(_ context.Context, threadID, orgID, funding, accountID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return f.recordErr
	}
	f.threadFunding[threadID] = [3]string{funding, orgID, accountID}
	return nil
}

func (f *fundingFake) ChatPersonPickDriver(_ context.Context, chatID, orgID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.personPickErr != nil {
		return "", f.personPickErr
	}
	if p, ok := f.personPick[chatID]; ok && p[0] == orgID {
		return p[1], nil
	}
	return "", nil
}

func (f *fundingFake) ChatFundingOrder(_ context.Context, chatID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chatOrderErr != nil {
		return "", f.chatOrderErr
	}
	return f.chatOrder[chatID], nil
}

func (f *fundingFake) answer(chatID, state string, until *time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[chatID]
	r.State, r.LeaseUntil, r.Announced = state, until, false
}

func (f *fundingFake) row(chatID string) *core.PersonalFunding {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[chatID]; ok {
		c := *r
		return &c
	}
	return nil
}

const fundOrg = "org-acme"

func acmeKey(id string) models.AccountConfig {
	a := apiKeyAccount(id, id, 0)
	a.AccountOrgID, a.Tier, a.Funding = fundOrg, models.TierFirst, models.FundingOrgKey
	return a
}

func personal(a models.AccountConfig) models.AccountConfig {
	a.Funding = models.FundingPersonal
	return a
}

func myCodex(id string) models.AccountConfig {
	a := claudeAccount(id, id+"@me.com", 0)
	a.DriverID = "codex"
	return personal(a)
}

// tiered is driversWith plus the rank the loader would compute.
func tiered(accts ...models.AccountConfig) models.AvailableDrivers {
	avail := driversWith(accts...)
	rank := map[models.DriverID]models.AccountTier{}
	any := false
	for d, list := range avail.Accounts {
		best := models.TierSecond
		for _, a := range list {
			if a.Tier < best {
				best = a.Tier
			}
		}
		rank[d] = best
		any = any || best == models.TierFirst
	}
	if any {
		avail.DriverRank = rank
	}
	return avail
}

type fundingEnv struct {
	*routingEnv
	store    *fundingFake
	settings fakeSettings
}

func newFundingEnv(t *testing.T, onLimit orgfunding.OnOrgLimit, order orgfunding.Order) *fundingEnv {
	t.Helper()
	env := &fundingEnv{routingEnv: newRoutingEnv(t), store: newFundingFake(), settings: fakeSettings{}}
	if onLimit != "" || order != "" {
		env.settings[orgfunding.SettingKey] = orgfunding.Settings{}.With(fundOrg, orgfunding.Preference{Order: order, OnOrgLimit: onLimit}, nil).Value()
	}
	env.routing.Funding, env.routing.Settings = env.store, env.settings
	return env
}

func (e *fundingEnv) resolve(t *testing.T, thread, selector string, mutate ...func(*llmCallSpec)) (*resolvedLLMCall, error) {
	t.Helper()
	spec := llmCallSpec{UserID: "u1", OrgID: fundOrg, Attended: true, ChatID: "chat-1", SessionID: "chat-1", ThreadID: thread,
		Selector: models.ModelSelector{ID: selector}, Routing: e.routing, TurnStart: true}
	for _, m := range mutate {
		m(&spec)
	}
	return resolveLLMCall(context.Background(), nil, spec)
}

// isWait reports a refusal the workflow waits out (a usage-limit failure with
// no alternative and no fallback): never a failed turn.
func isWait(t *testing.T, err error) {
	t.Helper()
	var failure *types.UsageLimitFailure
	require.ErrorAs(t, err, &failure, "a wait, not a failure: %v", err)
	assert.False(t, failure.Facts.AlternativeAccount)
	assert.Empty(t, failure.Facts.FallbackModel)
	var ule *ratelimit.UsageLimitError
	require.ErrorAs(t, err, &ule)
}

// Case 3 (unserved): the org gave the person an Anthropic key; the model the
// turn asks for is Codex's, which only the person's own account serves.
func TestPersonalFunding_UnservedModel(t *testing.T) {
	avail := tiered(acmeKey("acme-key"), myCodex("my-codex"))
	for _, tc := range []struct {
		name    string
		onLimit orgfunding.OnOrgLimit
		wantRun bool
		asked   bool
	}{
		{"ask (the default) asks and waits", "", false, true},
		{"wait waits without asking", orgfunding.OnOrgLimitWait, false, false},
		{"use_personal goes on, with a notice", orgfunding.OnOrgLimitUsePersonal, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAvailableDrivers(t, avail)
			env := newFundingEnv(t, tc.onLimit, "")
			res, err := env.resolve(t, "root", "gpt-5.6-sol")
			if tc.wantRun {
				require.NoError(t, err)
				assert.Equal(t, "my-codex", res.Account.Config.AccountID)
				assert.Contains(t, res.FundingNotice, "do not serve this model")
				assert.Contains(t, res.FundingNotice, "Settings → Providers")
				assert.Equal(t, core.PersonalFundingUsePersonal, env.store.row("chat-1").State)
				assert.Equal(t, core.PersonalFundingUnserved, env.store.row("chat-1").Reason)
				return
			}
			isWait(t, err)
			assert.NotContains(t, env.built, "my-codex", "no provider call on the person's account")
			if tc.asked {
				q := env.store.row("chat-1")
				require.NotNil(t, q)
				assert.Equal(t, core.PersonalFundingAsked, q.State)
				assert.Equal(t, core.PersonalFundingUnserved, q.Reason)
				assert.Equal(t, "gpt-5.6-sol", q.Model)
				assert.Equal(t, "codex", q.PersonalProvider)
			} else {
				assert.Nil(t, env.store.row("chat-1"))
			}
		})
	}
}

// An unanswered question waits, at every retry and every thread of the chat
// (a sub-agent never prompts on its own and never fails): one question.
// An old client's Resume is just another retry, and keeps waiting.
func TestPersonalFunding_UnansweredWaitsOneQuestionPerChat(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
	env := newFundingEnv(t, "", "")
	for _, thread := range []string{"root", "root", "sub-agent-1", "sub-agent-2", "root"} {
		_, err := env.resolve(t, thread, "gpt-5.6-sol", func(s *llmCallSpec) { s.TurnStart = thread == "root" })
		isWait(t, err)
	}
	assert.Equal(t, 1, env.store.asks, "one question for the chat")

	// The person answers: every thread goes on, and says so once.
	env.store.answer("chat-1", core.PersonalFundingUsePersonal, nil)
	for i, thread := range []string{"root", "sub-agent-1"} {
		res, err := env.resolve(t, thread, "gpt-5.6-sol")
		require.NoError(t, err)
		assert.Equal(t, "my-codex", res.Account.Config.AccountID)
		if i == 0 {
			assert.Contains(t, res.FundingNotice, "do not serve this model", "the first call under the answer says so")
		} else {
			assert.Empty(t, res.FundingNotice, "once for the chat")
		}
	}
	// The unserved answer lasts while the chat keeps the model; another model
	// asks again.
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
	_, err := env.resolve(t, "root", "gpt-5.5@codex", func(s *llmCallSpec) { s.Selector.ID = "gpt-5.5" })
	if err != nil {
		isWait(t, err)
	}
}

// No question when nothing of the org's is being replaced: the person has no
// org-funded account in the org; they picked their own provider explicitly;
// their order for the org is personal_first; the call names no org.
func TestPersonalFunding_NoQuestion(t *testing.T) {
	t.Run("the org gave the person nothing", func(t *testing.T) {
		withAvailableDrivers(t, tiered(myCodex("my-codex")))
		env := newFundingEnv(t, "", "")
		res, err := env.resolve(t, "root", "gpt-5.6-sol")
		require.NoError(t, err)
		assert.Equal(t, "my-codex", res.Account.Config.AccountID)
		assert.Empty(t, res.FundingNotice)
		assert.Nil(t, env.store.row("chat-1"))
	})
	t.Run("personal_first", func(t *testing.T) {
		withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
		env := newFundingEnv(t, "", orgfunding.OrderPersonalFirst)
		_, err := env.resolve(t, "root", "gpt-5.6-sol")
		require.NoError(t, err)
		assert.Nil(t, env.store.row("chat-1"))
	})
	t.Run("a call that names no org", func(t *testing.T) {
		withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
		env := newFundingEnv(t, "", "")
		_, err := env.resolve(t, "root", "gpt-5.6-sol", func(s *llmCallSpec) { s.OrgID = "" })
		require.NoError(t, err)
		assert.Nil(t, env.store.row("chat-1"))
	})
}

// org_only (the chat's order, or the policy-forced value) never uses the
// person's own account and never asks: it waits, an explicit pick included.
func TestPersonalFunding_OrgOnlyWaitsWithoutAsking(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
	for _, sel := range []string{"gpt-5.6-sol", "gpt-5.6-sol@codex"} {
		env := newFundingEnv(t, orgfunding.OnOrgLimitUsePersonal, "")
		_, err := env.resolve(t, "root", sel, func(s *llmCallSpec) { s.Failover.Funding = types.FundingOrgOnly })
		isWait(t, err)
		assert.Nil(t, env.store.row("chat-1"), "%s: org_only never asks", sel)
	}
}

// Case 2 (unusable): the thread was on the org's key, which is gone from the
// candidates (revoked, paused, grant withdrawn); the person's own account of
// another provider serves the model.
func TestPersonalFunding_UnusableOrgAccount(t *testing.T) {
	for _, tc := range []struct {
		onLimit orgfunding.OnOrgLimit
		run     bool
	}{{"", false}, {orgfunding.OnOrgLimitWait, false}, {orgfunding.OnOrgLimitUsePersonal, true}} {
		withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
		env := newFundingEnv(t, tc.onLimit, "")
		env.store.assignments["root"] = []core.Assignment{{ThreadID: "root", Provider: "anthropic", AccountID: "revoked-key"}}
		env.store.orgs["revoked-key"] = [2]string{fundOrg, ""}
		res, err := env.resolve(t, "root", routeModel)
		if tc.run {
			require.NoError(t, err)
			assert.Equal(t, "my-claude", res.Account.Config.AccountID)
			assert.Contains(t, res.FundingNotice, "unavailable")
			continue
		}
		isWait(t, err)
		if tc.onLimit == "" {
			require.NotNil(t, env.store.row("chat-1"))
			assert.Equal(t, core.PersonalFundingUnusable, env.store.row("chat-1").Reason)
		}
	}
}

// Case 1 (limited): every org key serving the model is limited and the
// person's own account could serve now.
func TestPersonalFunding_LimitedOrgTier(t *testing.T) {
	// A reset well after the test runs: the router benches the org's key until then.
	reset := time.Date(2099, 1, 2, 5, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		onLimit orgfunding.OnOrgLimit
		lease   bool
		asked   bool
	}{{"", false, true}, {orgfunding.OnOrgLimitWait, false, false}, {orgfunding.OnOrgLimitUsePersonal, true, false}} {
		withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
		env := newFundingEnv(t, tc.onLimit, "")
		env.routing.Store.(*routerFake).bench("acme-key", reset, "five_hour")
		_, err := env.resolve(t, "root", routeModel)
		var failure *types.UsageLimitFailure
		require.ErrorAs(t, err, &failure, "a limited org tier is a usage-limit refusal")
		assert.Equal(t, tc.lease, failure.Facts.AlternativeAccount, "use_personal retries at once on the person's account")
		q := env.store.row("chat-1")
		switch {
		case tc.lease:
			require.NotNil(t, q)
			assert.Equal(t, core.PersonalFundingUsePersonal, q.State)
			require.NotNil(t, q.LeaseUntil)
			assert.True(t, q.LeaseUntil.Equal(reset), "the lease lasts until the org's reset")
			// The retry goes to the person's account, and says so.
			res, err := env.resolve(t, "root", routeModel)
			require.NoError(t, err)
			assert.Equal(t, "my-claude", res.Account.Config.AccountID)
			assert.Contains(t, res.FundingNotice, "rate-limited")
			assert.Contains(t, res.FundingNotice, "Settings → Providers", "the person's setting chose it")
		case tc.asked:
			require.NotNil(t, q)
			assert.Equal(t, core.PersonalFundingAsked, q.State)
			assert.Equal(t, core.PersonalFundingLimited, q.Reason)
			require.NotNil(t, q.OrgResetAt)
		default:
			assert.Nil(t, q)
		}
	}
}

// The lease ends at the org's reset: the first call after it is the org's
// again, the question row is cleared and the chat says so.
func TestPersonalFunding_LeaseReturnsAtReset(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	orig := fundingNow
	fundingNow = func() time.Time { return now }
	t.Cleanup(func() { fundingNow = orig })

	withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env := newFundingEnv(t, "", "")
	until := now.Add(time.Hour)
	env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: "u1", OrgID: fundOrg,
		State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingLimited, Model: routeModel, LeaseUntil: &until}

	res, err := env.resolve(t, "root", routeModel)
	require.NoError(t, err)
	assert.Equal(t, "my-claude", res.Account.Config.AccountID, "during the lease")

	now = until.Add(time.Second)
	res, err = env.resolve(t, "root-2", routeModel)
	require.NoError(t, err)
	assert.Equal(t, "acme-key", res.Account.Config.AccountID, "after the reset, the org's again")
	assert.Equal(t, orgAccountsBackNotice(), res.FundingNotice)
	assert.Nil(t, env.store.row("chat-1"))
}

// An automation stamped "any" has nobody to ask: its owner's stamp is the
// consent, so it goes on without a question.
func TestPersonalFunding_UnattendedAnyGoesOn(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
	env := newFundingEnv(t, "", "")
	res, err := env.resolve(t, "root", "gpt-5.6-sol", func(s *llmCallSpec) { s.Attended, s.UnattendedAnySource = false, true })
	require.NoError(t, err)
	assert.Equal(t, "my-codex", res.Account.Config.AccountID)
	assert.Equal(t, core.PersonalFundingUsePersonal, env.store.row("chat-1").State)
}

// Without a question store (a worker built without one) consent can be
// neither read nor recorded: a call that would use the person's own account
// waits, and no driver is built for it. An org-funded call goes on.
func TestPersonalFunding_NoStoreNeverSpends(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env := newFundingEnv(t, orgfunding.OnOrgLimitUsePersonal, "")
	env.routing.Funding = nil
	_, err := env.resolve(t, "root", "gpt-5.6-sol")
	isWait(t, err)
	assert.NotContains(t, env.built, "my-codex")
	_, err = env.resolve(t, "root", "gpt-5.6-sol@codex")
	isWait(t, err)
	assert.Empty(t, env.built, "not even an explicit pick spends without the store")

	res, err := env.resolve(t, "root", routeModel)
	require.NoError(t, err)
	assert.Equal(t, "acme-key", res.Account.Config.AccountID, "the org's account still serves")
}

// org_only honours no lease and no explicit pick, and holds when the org has
// no account left: the person's own account never serves (B1).
func TestPersonalFunding_OrgOnlyIsAbsolute(t *testing.T) {
	orgOnly := func(s *llmCallSpec) { s.Failover.Funding = types.FundingOrgOnly }
	leased := func(env *fundingEnv) {
		until := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: "u1", OrgID: fundOrg,
			State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingLimited, Model: routeModel, LeaseUntil: &until, Announced: true}
	}

	t.Run("a live lease is ignored: the org's account serves", func(t *testing.T) {
		withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
		env := newFundingEnv(t, orgfunding.OnOrgLimitUsePersonal, "")
		leased(env)
		res, err := env.resolve(t, "root", routeModel, orgOnly)
		require.NoError(t, err)
		assert.Equal(t, "acme-key", res.Account.Config.AccountID)
		assert.NotContains(t, env.built, "my-claude")
	})
	t.Run("a live lease is ignored: with the org's key gone, it waits", func(t *testing.T) {
		withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
		env := newFundingEnv(t, orgfunding.OnOrgLimitUsePersonal, "")
		leased(env)
		_, err := env.resolve(t, "root", routeModel, orgOnly)
		isWait(t, err)
		assert.Empty(t, env.built)
	})
	t.Run("an org that gave nothing: it waits", func(t *testing.T) {
		withAvailableDrivers(t, tiered(myCodex("my-codex")))
		env := newFundingEnv(t, "", "")
		_, err := env.resolve(t, "root", "gpt-5.6-sol", orgOnly)
		isWait(t, err)
		assert.Empty(t, env.built)
		assert.Nil(t, env.store.row("chat-1"), "org_only never asks")
	})
	t.Run("the person's own account on the org's provider is never placed", func(t *testing.T) {
		mine := personal(apiKeyAccount("my-anthropic", "me@me.com", 0))
		withAvailableDrivers(t, tiered(acmeKey("acme-key"), mine))
		env := newFundingEnv(t, "", "")
		env.routing.Store.(*routerFake).bench("acme-key", time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "five_hour")
		_, err := env.resolve(t, "root", routeModel+"@anthropic", orgOnly)
		var failure *types.UsageLimitFailure
		require.ErrorAs(t, err, &failure, "the org's key is limited: a wait")
		assert.False(t, failure.Facts.AlternativeAccount)
		assert.Empty(t, env.built)
	})
}

// Case 2 when the replaced account was the org's last, on the same provider:
// by the time the call is checked the router has already stored the person's
// account as the thread's, and the org has nothing left (B2). The provenance
// is the account the assignment left, or, once that account is deleted, who
// paid for this thread's calls (thread_funding).
func TestPersonalFunding_ReplacedOnTheSameProvider(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(env *fundingEnv)
		unknown bool
	}{
		{name: "the org's key, paused or revoked", setup: func(env *fundingEnv) {
			env.store.orgs["acme-key"] = [2]string{fundOrg, ""}
		}},
		{name: "the org's key, deleted", setup: func(env *fundingEnv) {
			env.store.threadFunding["root"] = [3]string{models.FundingOrgKey, fundOrg, "acme-key"}
		}},
		{name: "the thread's history cannot be read", setup: func(env *fundingEnv) {
			env.store.threadFundingErr = errors.New("db down")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
			env := newFundingEnv(t, "", "")
			env.routing.Store.(*routerFake).assignments["root|claude"] = "acme-key"
			// What the real store returns once the router moved the thread.
			env.store.assignments["root"] = []core.Assignment{{ThreadID: "root", Provider: "claude", AccountID: "my-claude"}}
			tc.setup(env)
			_, err := env.resolve(t, "root", routeModel)
			isWait(t, err)
			assert.Empty(t, env.built, "no provider call on the person's account")
			q := env.store.row("chat-1")
			require.NotNil(t, q)
			assert.Equal(t, core.PersonalFundingUnusable, q.Reason)
		})
	}
	t.Run("a thread whose calls were the person's own goes on", func(t *testing.T) {
		withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
		env := newFundingEnv(t, "", "")
		env.store.threadFunding["root"] = [3]string{models.FundingPersonal, fundOrg, "my-claude"}
		res, err := env.resolve(t, "root", routeModel)
		require.NoError(t, err)
		assert.Equal(t, "my-claude", res.Account.Config.AccountID)
	})
}

// Provenance is per thread (review round 2, B2): thread A ran on the org's
// key, thread B on the person's own (an answered question); then the org's
// last key is deleted. A must ask, whatever B did last.
func TestPersonalFunding_ProvenanceIsPerThread(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env := newFundingEnv(t, "", "")
	res, err := env.resolve(t, "thread-a", routeModel+"@anthropic")
	require.NoError(t, err)
	require.Equal(t, "acme-key", res.Account.Config.AccountID)
	// Thread B names the person's provider; the chat asks, the person says
	// yes, and B runs on their own account. The episode then ends.
	_, err = env.resolve(t, "thread-b", routeModel+"@claude")
	isWait(t, err)
	env.store.answer("chat-1", core.PersonalFundingUsePersonal, nil)
	res, err = env.resolve(t, "thread-b", routeModel+"@claude")
	require.NoError(t, err)
	require.Equal(t, "my-claude", res.Account.Config.AccountID)
	delete(env.store.rows, "chat-1")
	assert.Equal(t, [3]string{models.FundingOrgKey, fundOrg, "acme-key"}, env.store.threadFunding["thread-a"], "recorded when the call went ahead")
	assert.Equal(t, models.FundingPersonal, env.store.threadFunding["thread-b"][0])

	// The org deletes its last key: the account, its assignments and its
	// metadata are gone; only the thread's own provenance remains.
	withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
	env.routing.Store.(*routerFake).assignments = map[string]string{}
	_, err = env.resolve(t, "thread-a", routeModel)
	isWait(t, err)
	q := env.store.row("chat-1")
	require.NotNil(t, q, "thread A asks")
	assert.Equal(t, core.PersonalFundingUnusable, q.Reason)
	assert.Equal(t, models.FundingOrgKey, env.store.threadFunding["thread-a"][0], "a held call keeps the org's provenance")
}

// The chat's funding order is read from the person-set chat policy, never
// from the run: a preset's, a workflow's or a spawn's personal_first is no
// consent to spend, while their org_first and org_only still tighten (B1).
func TestPersonalFunding_RunOrderOnlyTightens(t *testing.T) {
	avail := tiered(acmeKey("acme-key"), myCodex("my-codex"))
	runOrder := func(order string) func(*llmCallSpec) {
		return func(s *llmCallSpec) { s.Failover.Funding = order }
	}

	t.Run("a run's personal_first is not consent", func(t *testing.T) {
		withAvailableDrivers(t, avail)
		env := newFundingEnv(t, "", "")
		_, err := env.resolve(t, "root", "gpt-5.6-sol", runOrder(types.FundingPersonalFirst))
		isWait(t, err)
		require.NotNil(t, env.store.row("chat-1"), "it asks")
		assert.NotContains(t, env.built, "my-codex")
	})
	t.Run("the person's chat order is", func(t *testing.T) {
		withAvailableDrivers(t, avail)
		env := newFundingEnv(t, "", "")
		env.store.chatOrder["chat-1"] = types.FundingPersonalFirst
		res, err := env.resolve(t, "root", "gpt-5.6-sol")
		require.NoError(t, err)
		assert.Equal(t, "my-codex", res.Account.Config.AccountID)
	})
	t.Run("a run's org_first undoes the person's personal_first", func(t *testing.T) {
		withAvailableDrivers(t, avail)
		env := newFundingEnv(t, "", "")
		env.store.chatOrder["chat-1"] = types.FundingPersonalFirst
		_, err := env.resolve(t, "root", "gpt-5.6-sol", runOrder(types.FundingOrgFirst))
		isWait(t, err)
	})
	t.Run("a run's org_only holds over the person's personal_first", func(t *testing.T) {
		withAvailableDrivers(t, avail)
		env := newFundingEnv(t, orgfunding.OnOrgLimitUsePersonal, orgfunding.OrderPersonalFirst)
		env.store.chatOrder["chat-1"] = types.FundingPersonalFirst
		_, err := env.resolve(t, "root", "gpt-5.6-sol", runOrder(types.FundingOrgOnly))
		isWait(t, err)
		assert.Nil(t, env.store.row("chat-1"), "org_only never asks")
	})
}

// A router outage routes no account, so the provider's default account would
// serve: it answers to the same consent (B5).
func TestPersonalFunding_RouterOutageChecksTheDefaultAccount(t *testing.T) {
	failing := &accountrouter.MockService{AssignFunc: func(context.Context, accountrouter.Request) (accountrouter.Result, error) {
		return accountrouter.Result{}, errors.New("db down")
	}}
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
	env := newFundingEnv(t, "", "")
	env.routing.Router = failing
	_, err := env.resolve(t, "root", "gpt-5.6-sol")
	isWait(t, err)
	q := env.store.row("chat-1")
	require.NotNil(t, q, "the default account is the person's own: the chat asks")
	assert.Equal(t, core.PersonalFundingUnserved, q.Reason)

	// A default that is the org's goes on as before.
	env = newFundingEnv(t, "", "")
	env.routing.Router = failing
	res, err := env.resolve(t, "root", routeModel+"@anthropic")
	if err == nil {
		assert.Nil(t, res.Account, "served by the default account, unrouted")
	} else {
		var failure *types.UsageLimitFailure
		assert.False(t, errors.As(err, &failure), "never a funding wait for the org's own account: %v", err)
	}
}

// Mid-turn the thread stays on its provider; a better tier is taken up at the
// next turn start only (D6), with a notice.
func TestPersonalFunding_UpgradeOnlyAtTurnStart(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env := newFundingEnv(t, "", "")
	env.store.serving["root"] = "my-claude"

	res, err := env.resolve(t, "root", routeModel, func(s *llmCallSpec) { s.TurnStart = false })
	require.NoError(t, err)
	assert.Equal(t, "my-claude", res.Account.Config.AccountID, "between tool steps the thread keeps its provider")

	res, err = env.resolve(t, "root", routeModel)
	require.NoError(t, err)
	assert.Equal(t, "acme-key", res.Account.Config.AccountID, "at the turn start it moves up to the org's account")
	assert.Contains(t, res.FundingNotice, "for this chat from now on")
}

func TestHistoryStartsTurn(t *testing.T) {
	msg := func(role message.MessageRole) message.Message { return message.Message{Role: role} }
	assert.True(t, historyStartsTurn([]message.Message{msg(message.User)}))
	assert.True(t, historyStartsTurn([]message.Message{msg(message.Assistant), msg(message.Agent), msg(message.System)}))
	assert.False(t, historyStartsTurn([]message.Message{msg(message.User), msg(message.Assistant), msg(message.Tool)}))
	assert.False(t, historyStartsTurn(nil))
}

// A question or lease is one person's answer about one org's accounts (review
// round 3, B2): once the chat's project moves to another org (or the row names
// another person), it is not honoured, and the next call asks afresh.
func TestPersonalFunding_LeaseIsBoundToItsOrgAndPerson(t *testing.T) {
	const otherOrg = "org-other"
	otherKey := func(id string) models.AccountConfig {
		a := acmeKey(id)
		a.AccountOrgID = otherOrg
		return a
	}
	for _, tc := range []struct {
		name       string
		user, org  string
		specOrg    string
		orgAccount models.AccountConfig
	}{
		{"the project moved to another org", "u1", fundOrg, otherOrg, otherKey("other-key")},
		{"another person's answer", "someone-else", fundOrg, fundOrg, acmeKey("acme-key")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAvailableDrivers(t, tiered(tc.orgAccount, myCodex("my-codex")))
			env := newFundingEnv(t, "", "")
			env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: tc.user, OrgID: tc.org,
				State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingUnserved, Model: "gpt-5.6-sol", Announced: true}
			_, err := env.resolve(t, "root", "gpt-5.6-sol", func(s *llmCallSpec) { s.OrgID = tc.specOrg })
			isWait(t, err)
			assert.NotContains(t, env.built, "my-codex", "the old answer spends nothing")
			q := env.store.row("chat-1")
			require.NotNil(t, q)
			assert.Equal(t, core.PersonalFundingAsked, q.State, "the chat asks afresh")
			assert.Equal(t, tc.specOrg, q.OrgID)
			assert.Equal(t, "u1", q.UserID)
		})
	}
}

// Provenance fails closed (review round 3, B3): an org-funded call whose
// provenance cannot be recorded is not made; once the write works it is, and
// after the org's last key is deleted the thread asks.
func TestPersonalFunding_ProvenanceWriteFailsClosed(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
	env := newFundingEnv(t, "", "")
	env.store.recordErr = errors.New("db down")
	_, err := env.resolve(t, "thread-a", routeModel+"@anthropic")
	require.Error(t, err, "no org-funded call without its provenance")
	var failure *types.UsageLimitFailure
	assert.False(t, errors.As(err, &failure), "a retryable error, not a funding wait")
	assert.Empty(t, env.built)

	env.store.recordErr = nil
	res, err := env.resolve(t, "thread-a", routeModel+"@anthropic")
	require.NoError(t, err)
	assert.Equal(t, "acme-key", res.Account.Config.AccountID)

	withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
	env.routing.Store.(*routerFake).assignments = map[string]string{}
	_, err = env.resolve(t, "thread-a", routeModel)
	isWait(t, err)
	require.NotNil(t, env.store.row("chat-1"))
	assert.Equal(t, core.PersonalFundingUnusable, env.store.row("chat-1").Reason)

	// A personal call's failed provenance write is not a reason to refuse it.
	env2 := newFundingEnv(t, "", "")
	env2.store.recordErr = errors.New("db down")
	res, err = env2.resolve(t, "thread-b", routeModel+"@claude")
	require.NoError(t, err)
	assert.Equal(t, "my-claude", res.Account.Config.AccountID)
}

// A provider a workflow, a preset, a run or a spawned agent names is not the
// person's consent (review round 4, B2): a "<model>@<driver>" selector, the
// run's providers and the selector's providers all ask before the person's own
// account serves, while the org has an account. Only the person's own order
// (or answer) goes on without a question.
func TestPersonalFunding_NamedProviderIsNotConsent(t *testing.T) {
	for name, mutate := range map[string]func(*llmCallSpec){
		"a model@driver selector":    func(s *llmCallSpec) { s.Selector.ID = "gpt-5.6-sol@codex" },
		"the run's providers":        func(s *llmCallSpec) { s.RunProviders = []string{"codex"} },
		"the selector's providers":   func(s *llmCallSpec) { s.Selector.Providers = []string{"codex"} },
		"the chat's provider switch": func(s *llmCallSpec) { s.ChatProvider = "codex" },
		"a spawned agent's pick": func(s *llmCallSpec) {
			s.Selector.ID, s.Descendant, s.TurnStart = "gpt-5.6-sol@codex", true, false
		},
	} {
		t.Run(name, func(t *testing.T) {
			withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
			env := newFundingEnv(t, "", "")
			_, err := env.resolve(t, "root", "gpt-5.6-sol", mutate)
			isWait(t, err)
			assert.NotContains(t, env.built, "my-codex")
			require.NotNil(t, env.store.row("chat-1"), "it asks")
		})
	}
}

// A chat order that cannot be read is taken as restricting (review round 4,
// B1): with org_only stored and use_personal remembered, a failing read spends
// nothing and asks nothing.
func TestPersonalFunding_UnreadableChatOrderFailsClosed(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
	env := newFundingEnv(t, orgfunding.OnOrgLimitUsePersonal, "")
	env.store.chatOrder["chat-1"] = types.FundingOrgOnly
	env.store.chatOrderErr = errors.New("db down")
	_, err := env.resolve(t, "root", "gpt-5.6-sol")
	isWait(t, err)
	assert.Empty(t, env.built, "no personal driver is built")
	assert.Nil(t, env.store.row("chat-1"), "no lease is granted")
}

// The provider the chat's owner picked for its root through the composer,
// as a person (chat_person_picks), is consent for the root's attended calls
// on that provider in that org: the call goes on with no question.
func TestPersonalFunding_PersonPickIsConsentForTheRoot(t *testing.T) {
	withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
	env := newFundingEnv(t, "", "")
	env.store.personPick = map[string][2]string{"chat-1": {fundOrg, "codex"}}
	res, err := env.resolve(t, "root", "gpt-5.6-sol@codex")
	require.NoError(t, err)
	assert.Equal(t, "my-codex", res.Account.Config.AccountID)
	assert.Nil(t, env.store.row("chat-1"), "a person's pick asks nothing")
}

// Everything else that names a provider is a preference, not consent: with
// the org holding an account, a personal-only driver still asks (the default
// "ask"), and no personal driver is built.
func TestPersonalFunding_OnlyThePersonsRootPickIsConsent(t *testing.T) {
	codexPick := map[string][2]string{"chat-1": {fundOrg, "codex"}}
	for _, tc := range []struct {
		name   string
		pick   map[string][2]string
		pickEr error
		order  orgfunding.Order
		mutate func(*llmCallSpec)
		asks   bool
	}{
		{name: "a workflow's model@driver selector, no pick", mutate: func(s *llmCallSpec) { s.Selector.ID = "gpt-5.6-sol@codex" }, asks: true},
		{name: "the run's providers (a preset's or workflow's providers:), no pick", mutate: func(s *llmCallSpec) { s.RunProviders = []string{"codex"} }, asks: true},
		{name: "the selector's providers, no pick", mutate: func(s *llmCallSpec) { s.Selector.Providers = []string{"codex"} }, asks: true},
		{name: "the provider switch, no pick", mutate: func(s *llmCallSpec) { s.ChatProvider = "codex" }, asks: true},
		{name: "a sub-agent's selector, with the root's pick", pick: codexPick, mutate: func(s *llmCallSpec) {
			s.Selector.ID, s.Descendant, s.TurnStart = "gpt-5.6-sol@codex", true, false
		}, asks: false},
		{name: "a spawn's providers, with the root's pick", pick: codexPick, mutate: func(s *llmCallSpec) {
			s.RunProviders, s.Descendant = []string{"codex"}, true
		}, asks: false},
		{name: "an unattended root call, with a pick", pick: codexPick, mutate: func(s *llmCallSpec) {
			s.Attended, s.UnattendedAnySource = false, false
		}, asks: false},
		{name: "a pick made in another org", pick: map[string][2]string{"chat-1": {"org-other", "codex"}}, asks: true},
		{name: "a pick of another provider", pick: map[string][2]string{"chat-1": {fundOrg, "claude"}}, asks: true},
		{name: "a pick that cannot be read", pickEr: errors.New("db down"), asks: true},
		{name: "org_only outranks a pick", pick: codexPick, order: orgfunding.OrderOrgOnly, asks: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
			env := newFundingEnv(t, "", tc.order)
			env.store.personPick, env.store.personPickErr = tc.pick, tc.pickEr
			mutate := tc.mutate
			if mutate == nil {
				mutate = func(*llmCallSpec) {}
			}
			_, err := env.resolve(t, "root", "gpt-5.6-sol", mutate)
			require.Error(t, err, "no personal spending")
			assert.NotContains(t, env.built, "my-codex", "no personal driver is built")
			if tc.asks {
				require.NotNil(t, env.store.row("chat-1"), "it asks")
			}
		})
	}
}

// Case 1 (limited) with the person's own pick: the org's accounts of the
// picked provider are rate-limited and the person picked that provider for
// the chat's root. The root's call goes on on their account, asking nothing
// and granting no lease (promotePickWhileOrgLimited); anything else is as
// without a pick.
func TestPersonalFunding_LimitedOrgTierHonoursTheRootPick(t *testing.T) {
	reset := time.Date(2099, 1, 2, 5, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		pick   string
		order  orgfunding.Order
		mutate func(*llmCallSpec)
		serves bool
		asked  bool
	}{
		{name: "a matching pick serves on the person's account", pick: "claude", serves: true},
		{name: "a pick of another provider asks", pick: "codex", asked: true},
		{name: "a sub-agent of the root never gains the pick", pick: "claude", mutate: func(s *llmCallSpec) { s.Descendant = true }, asked: true},
		{name: "org_only outranks the pick", pick: "claude", order: orgfunding.OrderOrgOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The org's key and the person's own account are both Claude's:
			// the provider the person picked.
			orgClaude := acmeKey("acme-key")
			orgClaude.DriverID = "claude"
			withAvailableDrivers(t, tiered(orgClaude, personal(claudeAccount("my-claude", "me@me.com", 0))))
			env := newFundingEnv(t, "", tc.order)
			env.store.personPick = map[string][2]string{"chat-1": {fundOrg, tc.pick}}
			env.routing.Store.(*routerFake).bench("acme-key", reset, "five_hour")
			mutate := tc.mutate
			if mutate == nil {
				mutate = func(*llmCallSpec) {}
			}
			res, err := env.resolve(t, "root", routeModel, mutate)
			if tc.serves {
				require.NoError(t, err)
				assert.Equal(t, "my-claude", res.Account.Config.AccountID, "the picked provider's own account serves")
				assert.Nil(t, env.store.row("chat-1"), "no question and no lease")
				return
			}
			var failure *types.UsageLimitFailure
			require.ErrorAs(t, err, &failure, "a limited org tier is a usage-limit refusal")
			assert.False(t, failure.Facts.AlternativeAccount, "no retry on the person's account")
			assert.NotContains(t, env.built, "my-claude")
			q := env.store.row("chat-1")
			if tc.asked {
				require.NotNil(t, q)
				assert.Equal(t, core.PersonalFundingAsked, q.State)
			} else {
				assert.Nil(t, q, "no question and no lease")
			}
		})
	}
}

// A usable org account of the picked provider still serves first: the pick
// is consent, not a preference over the org's accounts.
func TestPersonalFunding_RootPickKeepsTheOrgFirst(t *testing.T) {
	orgClaude := acmeKey("acme-key")
	orgClaude.DriverID = "claude"
	withAvailableDrivers(t, tiered(orgClaude, personal(claudeAccount("my-claude", "me@me.com", 0))))
	env := newFundingEnv(t, "", "")
	env.store.personPick = map[string][2]string{"chat-1": {fundOrg, "claude"}}
	res, err := env.resolve(t, "root", routeModel)
	require.NoError(t, err)
	assert.Equal(t, "acme-key", res.Account.Config.AccountID)
}

// An unattended run that may spend only its org's sources, none of which
// serves its model, fails the call with the no_org_account reason through
// the real resolution path: no personal driver is built, and the failure is
// one the runtime pauses on rather than retries (§11.4).
func TestUnattendedOrgOnly_NoOrgSourceFailsWithTheReason(t *testing.T) {
	withAvailableDrivers(t, tiered())
	env := newFundingEnv(t, "", "")
	_, err := env.resolve(t, "root", "gpt-5.6-sol@codex", func(s *llmCallSpec) { s.Attended, s.UnattendedAnySource = false, false })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this automation runs only on its organization's AI accounts, and none of them can run gpt-5.6-sol")
	assert.ErrorIs(t, err, drivererrors.ErrNoServableProvider)
	assert.Empty(t, env.built)

	// An attended run on the same empty providers keeps today's wording.
	_, err = env.resolve(t, "root", "gpt-5.6-sol@codex")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "organization's AI accounts")
}

// A first turn held on the org's limited key made no call, so nothing else
// records whose accounts it waits for: the hold does (thread_funding). When
// the org then gives the person nothing at all (the key's access narrowed, the
// other key paused), the retry a capacity wake sends is case 2, under "ask"
// with Keep waiting answered and under "wait" alike: it never goes on on the
// person's own account (reliant#2723's browser pass).
func TestPersonalFunding_HeldOnTheOrgTierThenTheOrgGivesNothing(t *testing.T) {
	reset := time.Date(2099, 1, 2, 5, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		onLimit orgfunding.OnOrgLimit
		asks    bool
	}{{"", true}, {orgfunding.OnOrgLimitWait, false}} {
		t.Run("on_org_limit="+string(tc.onLimit), func(t *testing.T) {
			withAvailableDrivers(t, tiered(acmeKey("acme-key"), personal(claudeAccount("my-claude", "me@me.com", 0))))
			env := newFundingEnv(t, tc.onLimit, "")
			env.routing.Store.(*routerFake).bench("acme-key", reset, "five_hour")
			_, err := env.resolve(t, "root", routeModel)
			var failure *types.UsageLimitFailure
			require.ErrorAs(t, err, &failure)
			assert.False(t, failure.Facts.AlternativeAccount)
			assert.Equal(t, [3]string{models.FundingOrgKey, fundOrg, "acme-key"}, env.store.threadFunding["root"], "the hold records whose account it waits for")
			if tc.asks {
				env.store.answer("chat-1", core.PersonalFundingKeepWaiting, nil)
			}

			withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
			_, err = env.resolve(t, "root", routeModel)
			isWait(t, err)
			assert.Empty(t, env.built, "no provider call on the person's account")
			q := env.store.row("chat-1")
			if !tc.asks {
				assert.Nil(t, q, "wait never asks")
				return
			}
			require.NotNil(t, q)
			assert.Equal(t, core.PersonalFundingAsked, q.State)
			assert.Equal(t, core.PersonalFundingUnusable, q.Reason)
		})
	}
}

// A chat with a question about the org's accounts, answered or not, was
// waiting for them or leasing in their place. When the org gives the person
// nothing any more, that is case 2: a Keep waiting, an unanswered question or
// a lease that ended is never consent to go on.
func TestPersonalFunding_TheChatsQuestionOutlivesTheOrgsAccounts(t *testing.T) {
	ended := time.Now().Add(-time.Minute)
	for name, row := range map[string]core.PersonalFunding{
		"asked":        {State: core.PersonalFundingAsked, Reason: core.PersonalFundingLimited},
		"keep waiting": {State: core.PersonalFundingKeepWaiting, Reason: core.PersonalFundingLimited},
		"lease ended":  {State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingLimited, LeaseUntil: &ended},
	} {
		t.Run(name, func(t *testing.T) {
			withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
			env := newFundingEnv(t, "", "")
			row.ChatID, row.UserID, row.OrgID, row.Model = "chat-1", "u1", fundOrg, routeModel
			env.store.rows["chat-1"] = &row
			// The thread's calls were the person's own (the lease's).
			env.store.threadFunding["root"] = [3]string{models.FundingPersonal, fundOrg, "my-claude"}
			_, err := env.resolve(t, "root", routeModel)
			isWait(t, err)
			assert.Empty(t, env.built)
			q := env.store.row("chat-1")
			require.NotNil(t, q)
			assert.Equal(t, core.PersonalFundingAsked, q.State)
			assert.Equal(t, core.PersonalFundingUnusable, q.Reason)
		})
	}
	t.Run("another person's question says nothing", func(t *testing.T) {
		withAvailableDrivers(t, tiered(personal(claudeAccount("my-claude", "me@me.com", 0))))
		env := newFundingEnv(t, "", "")
		env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: "someone-else", OrgID: fundOrg,
			State: core.PersonalFundingKeepWaiting, Reason: core.PersonalFundingLimited, Model: routeModel}
		res, err := env.resolve(t, "root", routeModel)
		require.NoError(t, err)
		assert.Equal(t, "my-claude", res.Account.Config.AccountID)
	})
}
