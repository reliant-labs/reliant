// Copyright (c) 2025 Reliant Labs
//
// Regression tests for WHICH ACCOUNT titles a chat.
//
// Titling resolved its model through the shared request path but never routed
// an account: the request always went to the provider's default account (its
// first by priority) and a refusal recorded nothing. In production a user with
// nine Claude accounts had the priority-0 one on a seven-day limit; every new
// chat's title met that account first, got a non-retryable 429, and settled
// for the truncated first message, while the same chat's own turn, two seconds
// later, was routed to a healthy account and answered. These pin that titling
// gets the same account routing, benching and key retirement a chat turn does,
// and that a title (unlike a chat turn, by the owner's decision) moves to
// another connected provider when every account of its provider is limited.
package handlers

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// titleAccounts serves each account through its own driver: a refusal for the
// accounts listed in refuse, a set_title call for every other one.
type titleAccounts struct {
	mu      sync.Mutex
	refuse  map[string]error
	called  []string
	titleOf string
}

func (f *titleAccounts) driverFor(_ context.Context, _ string, cfg models.DriverConfig, _ models.Model, _ ...llm.DriverOption) (llm.Driver, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.refuse[cfg.AccountID]; ok {
		return &titleAccountDriver{f: f, account: cfg.AccountID, failing: &failingTitleDriver{err: err}}, nil
	}
	return &titleAccountDriver{f: f, account: cfg.AccountID, ok: &titleDriver{resp: setTitleCall(`{"title":"` + f.titleOf + `"}`)}}, nil
}

func (f *titleAccounts) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.called)
}

// titleAccountDriver records which account a request was sent from.
type titleAccountDriver struct {
	mockLLMDriverForIdempotency
	f       *titleAccounts
	account string
	failing *failingTitleDriver
	ok      *titleDriver
}

func (d *titleAccountDriver) StreamResponse(ctx context.Context, prompts []string, msgs []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	d.f.mu.Lock()
	d.f.called = append(d.f.called, d.account)
	d.f.mu.Unlock()
	if d.failing != nil {
		return d.failing.StreamResponse(ctx, prompts, msgs, tls)
	}
	return d.ok.StreamResponse(ctx, prompts, msgs, tls)
}

func titleRoutingEnv(t *testing.T, accounts *titleAccounts) (*routingEnv, *GenerateTitleActivity) {
	t.Helper()
	env := newRoutingEnv(t)
	env.routing.DriverFor = accounts.driverFor
	return env, NewGenerateTitleActivity(nil, nil).WithAccountRouting(env.routing)
}

func sevenDayLimit(reset time.Time) error {
	return &ratelimit.UsageLimitError{Provider: "Claude subscription", StatusCode: 429, ResetAt: reset, Window: "seven_day"}
}

// The production shape: the default account is already known to be limited.
// The title goes to a healthy account and the limited one is never called.
func TestGenerateTitle_ALimitedAccountIsNotTried(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	accounts := &titleAccounts{refuse: map[string]error{"a": sevenDayLimit(time.Now().Add(120 * time.Hour))}, titleOf: "Merged PR Count"}
	env, activity := titleRoutingEnv(t, accounts)
	env.store.bench("a", time.Now().Add(120*time.Hour), "seven_day")

	title, err := activity.generateTitle(context.Background(), "u1", "how many PRs have we merged?")
	require.NoError(t, err)
	assert.Equal(t, "Merged PR Count", title)
	assert.Equal(t, []string{"b"}, accounts.calls(), "the limited default account is never called")
}

// An account the provider refuses mid-request is benched, the same title
// retries on another account of the provider, and the next chat's title does
// not try the refused account first.
func TestGenerateTitle_ARefusedAccountIsBenchedAndSkippedNextTime(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	reset := time.Now().Add(120 * time.Hour).UTC().Truncate(time.Second)
	accounts := &titleAccounts{refuse: map[string]error{"a": sevenDayLimit(reset)}, titleOf: "Old Worktrees"}
	env, activity := titleRoutingEnv(t, accounts)

	title, err := activity.generateTitle(context.Background(), "u1", "can you find old worktrees we can remove")
	require.NoError(t, err)
	assert.Equal(t, "Old Worktrees", title)
	assert.Equal(t, []string{"a", "b"}, accounts.calls(), "a refuses (ties break to a), then b serves")
	assert.Equal(t, []string{"a"}, env.recorder.benched, "the refusal is recorded against the account")

	_, err = activity.generateTitle(context.Background(), "u1", "another chat")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "b"}, accounts.calls(), "the next title goes straight to b")
}

// A provider that rejects an account's API key takes it out of rotation, the
// title is served by the next account, and later titles never offer the key
// again.
func TestGenerateTitle_ARejectedKeyLeavesRotation(t *testing.T) {
	a, b := apiKeyAccount("a", "alice@acme.com", 0), apiKeyAccount("b", "bob@acme.com", 1)
	accounts := &titleAccounts{refuse: map[string]error{"a": rejectedKey()}, titleOf: "Usage Dashboards"}
	env, activity := titleRoutingEnv(t, accounts)
	marker := &markerFake{store: env.store}
	env.routing.Credentials = marker

	// Resolution reads the user's accounts per call and leaves out one needing
	// sign-in, as drivers.LookupAvailableDrivers does.
	orig := lookupAvailableDrivers
	lookupAvailableDrivers = func(context.Context, string) (models.AvailableDrivers, error) {
		var live []models.AccountConfig
		for _, acct := range []models.AccountConfig{a, b} {
			if !slices.Contains(marker.marked, acct.AccountID) {
				live = append(live, acct)
			}
		}
		return driversWith(live...), nil
	}
	t.Cleanup(func() { lookupAvailableDrivers = orig })

	title, err := activity.generateTitle(context.Background(), "u1", "can we add dashboards around user usage?")
	require.NoError(t, err)
	assert.Equal(t, "Usage Dashboards", title)
	assert.Equal(t, []string{"a"}, marker.marked, "the rejected key needs sign-in")
	assert.Equal(t, []string{"a", "b"}, accounts.calls())

	_, err = activity.generateTitle(context.Background(), "u1", "another chat")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "b"}, accounts.calls(), "the rejected key is not tried again")
}

func codexAccount(id string) models.AccountConfig {
	return models.AccountConfig{
		DriverConfig: models.DriverConfig{DriverID: "codex", AccountID: id, AccountEmail: id + "@x.com", Enabled: true, APIKey: "k-" + id},
		Status:       models.AccountAvailable,
	}
}

// Every Claude account is known to be limited: the title moves to the user's
// other provider without calling Claude at all.
func TestGenerateTitle_AFullyLimitedProviderMovesTheTitleToAHealthyOne(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1), codexAccount("cx")))
	accounts := &titleAccounts{titleOf: "Load Balancing"}
	env, activity := titleRoutingEnv(t, accounts)
	env.store.bench("a", time.Now().Add(120*time.Hour), "seven_day")
	env.store.bench("b", time.Now().Add(3*time.Hour), "five_hour")

	title, err := activity.generateTitle(context.Background(), "u1", "improve load balancing")
	require.NoError(t, err)
	assert.Equal(t, "Load Balancing", title)
	assert.Equal(t, []string{"cx"}, accounts.calls(), "only the healthy provider is called")
}

// The provider's only account refuses mid-request: it is benched, and the same
// title is served by the other provider.
func TestGenerateTitle_ARefusalWithNoOtherAccountMovesToAnotherProvider(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), codexAccount("cx")))
	accounts := &titleAccounts{refuse: map[string]error{"a": sevenDayLimit(time.Now().Add(time.Hour))}, titleOf: "Old Worktrees"}
	env, activity := titleRoutingEnv(t, accounts)

	title, err := activity.generateTitle(context.Background(), "u1", "find old worktrees")
	require.NoError(t, err)
	assert.Equal(t, "Old Worktrees", title)
	assert.Equal(t, []string{"a", "cx"}, accounts.calls())
	assert.Equal(t, []string{"a"}, env.recorder.benched)

	_, err = activity.generateTitle(context.Background(), "u1", "another chat")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "cx", "cx"}, accounts.calls(), "the benched provider is not tried first again")
}

// When every account of every provider is limited the title fails without a
// provider call, so the workflow settles for the fallback title at once
// instead of spending a request on a known refusal, and the failure it reports
// is the limit, not "no provider".
func TestGenerateTitle_EveryAccountLimitedMakesNoProviderCall(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	accounts := &titleAccounts{titleOf: "Unused"}
	env, activity := titleRoutingEnv(t, accounts)
	env.store.bench("a", time.Now().Add(120*time.Hour), "seven_day")
	env.store.bench("b", time.Now().Add(3*time.Hour), "five_hour")

	_, err := activity.generateTitle(context.Background(), "u1", "hello")
	var failure *types.UsageLimitFailure
	require.ErrorAs(t, err, &failure)
	assert.False(t, failure.Facts.AlternativeAccount)
	assert.Empty(t, accounts.calls(), "no provider is called")
	assert.Empty(t, env.built, "no driver is built")
}

// The retry is bounded: accounts that each refuse in turn stop the title after
// titleAttempts requests, and the last refusal is what the workflow sees.
func TestGenerateTitle_AccountRetriesAreBounded(t *testing.T) {
	var accts []models.AccountConfig
	refuse := map[string]error{}
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		accts = append(accts, claudeAccount(id, id+"@acme.com", i))
		refuse[id] = sevenDayLimit(time.Now().Add(time.Hour))
	}
	withAvailableDrivers(t, driversWith(accts...))
	accounts := &titleAccounts{refuse: refuse}
	_, activity := titleRoutingEnv(t, accounts)

	_, err := activity.generateTitle(context.Background(), "u1", "hello")
	var failure *types.UsageLimitFailure
	require.ErrorAs(t, err, &failure)
	assert.Len(t, accounts.calls(), titleAttempts)
}

// A failure that is not about the account is not retried on another one.
func TestGenerateTitle_NonAccountFailureIsNotRetried(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	accounts := &titleAccounts{refuse: map[string]error{"a": errors.New("500 internal server error"), "b": errors.New("500 internal server error")}}
	env, activity := titleRoutingEnv(t, accounts)

	_, err := activity.generateTitle(context.Background(), "u1", "hello")
	require.Error(t, err)
	assert.Len(t, accounts.calls(), 1, "the workflow's own retry owns transient failures")
	assert.Empty(t, env.recorder.benched)
}
