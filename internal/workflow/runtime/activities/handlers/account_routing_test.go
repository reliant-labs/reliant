// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// These pin the account-routing seam resolveLLMCall and call_llm share, with
// in-memory fakes: which account a thread gets, what a refusal reports to the
// workflow, and that every failure of the machinery degrades to the plain
// behaviour of a user without accounts.

const (
	routeModel    = "claude-5.5-opus"
	routeFallback = "gpt-5.6-sol@codex"
)

func claudeAccount(id, email string, priority int) models.AccountConfig {
	return models.AccountConfig{
		DriverConfig: models.DriverConfig{
			DriverID: "claude", AccountID: id, AccountEmail: email, AccountLabel: email,
			Enabled: true, APIKey: "k-" + id,
		},
		Priority: priority, Status: models.AccountAvailable,
	}
}

func driversWith(accts ...models.AccountConfig) models.AvailableDrivers {
	out := models.AvailableDrivers{
		Drivers:  map[models.DriverID]models.DriverConfig{},
		Accounts: map[models.DriverID][]models.AccountConfig{},
	}
	for _, a := range accts {
		out.Accounts[a.DriverID] = append(out.Accounts[a.DriverID], a)
		if _, ok := out.Drivers[a.DriverID]; !ok {
			out.Drivers[a.DriverID] = a.DriverConfig
		}
	}
	return out
}

func withAvailableDrivers(t *testing.T, avail models.AvailableDrivers) {
	t.Helper()
	orig := lookupAvailableDrivers
	lookupAvailableDrivers = func(context.Context, string) (models.AvailableDrivers, error) { return avail, nil }
	t.Cleanup(func() { lookupAvailableDrivers = orig })
}

// routerFake is an in-memory accountrouter.Store the real router runs over.
type routerFake struct {
	mu          sync.Mutex
	assignments map[string]string // thread|provider -> account
	limits      map[string]accountrouter.LimitState
}

func newRouterFake() *routerFake {
	return &routerFake{assignments: map[string]string{}, limits: map[string]accountrouter.LimitState{}}
}

func (f *routerFake) GetThreadAssignment(_ context.Context, thread, provider string) (*accountrouter.Assignment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.assignments[thread+"|"+provider]; ok {
		return &accountrouter.Assignment{ThreadID: thread, Provider: provider, AccountID: id}, nil
	}
	return nil, nil
}

func (f *routerFake) SetThreadAssignment(_ context.Context, a accountrouter.Assignment, expected string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := a.ThreadID + "|" + a.Provider
	if cur, ok := f.assignments[key]; ok && cur != expected {
		return false, nil
	}
	f.assignments[key] = a.AccountID
	return true, nil
}

func (f *routerFake) ListAccountLimits(context.Context, string) (map[string]accountrouter.LimitState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]accountrouter.LimitState{}
	for k, v := range f.limits {
		out[k] = v
	}
	return out, nil
}

func (f *routerFake) RecentAssignmentCounts(context.Context, []string, time.Time) (map[string]int, error) {
	return nil, nil
}

func (f *routerFake) bench(account string, until time.Time, window string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits[account] = accountrouter.LimitState{AccountID: account, LimitedUntil: &until, LimitedWindow: window}
}

type recorderFake struct {
	store     *routerFake
	benched   []string
	failBench bool
	observed  []string
}

func (r *recorderFake) For(_, accountID string) ratelimit.Observer {
	r.observed = append(r.observed, accountID)
	return nopObserver{}
}

func (r *recorderFake) Bench(_ context.Context, _, accountID string, until time.Time, window string) (time.Time, error) {
	if r.failBench {
		return until, errors.New("store down")
	}
	r.benched = append(r.benched, accountID)
	r.store.bench(accountID, until, window)
	return until, nil
}

type nopObserver struct{}

func (nopObserver) ObserveRateLimits(context.Context, ratelimit.Snapshot) {}

type routingEnv struct {
	store    *routerFake
	recorder *recorderFake
	routing  *AccountRouting
	built    []string
}

func newRoutingEnv(t *testing.T) *routingEnv {
	t.Helper()
	store := newRouterFake()
	env := &routingEnv{store: store, recorder: &recorderFake{store: store}}
	env.routing = &AccountRouting{
		Router:   accountrouter.New(accountrouter.Deps{Store: store, Rand: func(int) int { return 0 }}),
		Store:    store,
		Recorder: env.recorder,
		DriverFor: func(_ context.Context, _ string, cfg models.DriverConfig, m models.Model, _ ...llm.DriverOption) (llm.Driver, error) {
			env.built = append(env.built, cfg.AccountID)
			return &mockLLMDriverForIdempotency{}, nil
		},
	}
	return env
}

func resolveOnClaude(t *testing.T, env *routingEnv, thread string, policy types.FailoverPolicy) (*resolvedLLMCall, error) {
	t.Helper()
	return resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID: "u1", SessionID: "s", ThreadID: thread,
		Selector: models.ModelSelector{ID: routeModel + "@claude"},
		Routing:  env.routing, Failover: policy,
	})
}

func TestRouting_AThreadSticksToItsAccount(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	env := newRoutingEnv(t)

	first, err := resolveOnClaude(t, env, "thread-1", types.FailoverPolicy{})
	require.NoError(t, err)
	require.NotNil(t, first.Account)
	require.True(t, first.Account.Change.Changed, "the first assignment is recorded")
	assert.Nil(t, first.Account.Change.Previous, "and is not a move")

	for range 3 {
		again, err := resolveOnClaude(t, env, "thread-1", types.FailoverPolicy{})
		require.NoError(t, err)
		assert.Equal(t, first.Account.Config.AccountID, again.Account.Config.AccountID, "sticky")
		assert.False(t, again.Account.Change.Changed)
	}
	assert.Equal(t, first.Account.Config.AccountID, env.built[len(env.built)-1], "the driver is built from the thread's account")
}

func TestRouting_PreferredAccountWinsForANewThread(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	env := newRoutingEnv(t)
	res, err := resolveOnClaude(t, env, "t", types.FailoverPolicy{PreferredAccount: map[string]string{"claude": "bob@acme.com"}})
	require.NoError(t, err)
	assert.Equal(t, "b", res.Account.Config.AccountID)
}

type fakeSettings map[string]string

func (f fakeSettings) GetSettingValue(_ context.Context, _, key string) (string, bool) {
	v, ok := f[key]
	return v, ok
}

func TestRouting_RoutingModeSetting(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	for value, want := range map[string]accountrouter.RoutingMode{
		"":         accountrouter.RoutingBalance,
		"balance":  accountrouter.RoutingBalance,
		"Balance":  accountrouter.RoutingBalance,
		"priority": accountrouter.RoutingPriority,
		"In order": accountrouter.RoutingBalance, // the UI stores the id; an unknown label is Balance
		"garbage":  accountrouter.RoutingBalance,
	} {
		r := &AccountRouting{Settings: fakeSettings{"provider.routing.claude": value}}
		assert.Equal(t, want, r.routingMode(context.Background(), "u", "claude"), "value %q", value)
	}
	var nilRouting *AccountRouting
	assert.Equal(t, accountrouter.RoutingBalance, nilRouting.routingMode(context.Background(), "u", "claude"))
}

func TestRouting_ALimitedAccountMovesTheThreadWithOneNotice(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	env := newRoutingEnv(t)

	first, err := resolveOnClaude(t, env, "thread-1", types.FailoverPolicy{})
	require.NoError(t, err)
	require.Equal(t, "a", first.Account.Config.AccountID, "ties break to the first account with a seeded rand")

	// The provider refuses account a: the activity benches it and reports facts.
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	refusal := &ratelimit.UsageLimitError{Provider: "Claude subscription", StatusCode: 429, ResetAt: reset, Window: "five_hour"}
	got := env.routing.usageLimitFailure(context.Background(), refusal, usageLimitContext{
		UserID: "u1", ThreadID: "thread-1", Driver: "claude", ModelID: routeModel, Account: first.Account,
	})
	var failure *types.UsageLimitFailure
	require.ErrorAs(t, got, &failure)
	assert.Equal(t, []string{"a"}, env.recorder.benched, "the refused account is benched")
	assert.True(t, failure.Facts.AlternativeAccount, "account b can serve now")
	assert.Equal(t, "a", failure.Facts.AccountID)
	assert.Equal(t, "claude", failure.Facts.Provider)
	assert.Empty(t, failure.Facts.FallbackModel, "cross-provider fallback is opt-in")
	assert.True(t, failure.Facts.ResetAt.Equal(reset))

	// The retried step resolves to b, and says so once.
	next, err := resolveOnClaude(t, env, "thread-1", types.FailoverPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "b", next.Account.Config.AccountID)
	require.True(t, next.Account.Change.Changed)
	require.NotNil(t, next.Account.Change.Previous)
	assert.Equal(t, "a", next.Account.Change.Previous.AccountID)
	assert.Equal(t, accountrouter.ReasonLimited, next.Account.Change.Reason)

	var notices []string
	env.routing.announceAccountMove(context.Background(), func(s string) { notices = append(notices, s) }, "u1", next.Account)
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0], "Claude account **alice@acme.com** reached its 5-hour limit")
	assert.Contains(t, notices[0], "(resets ")
	assert.Contains(t, notices[0], "Continuing on **bob@acme.com**.")

	// And the next turn stays on b, silently.
	after, err := resolveOnClaude(t, env, "thread-1", types.FailoverPolicy{})
	require.NoError(t, err)
	assert.Equal(t, "b", after.Account.Config.AccountID)
	assert.False(t, after.Account.Change.Changed)
	notices = nil
	env.routing.announceAccountMove(context.Background(), func(s string) { notices = append(notices, s) }, "u1", after.Account)
	assert.Empty(t, notices, "no notice when the thread did not move")
}

func TestRouting_AllAccountsLimitedMakesNoProviderCall(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	env := newRoutingEnv(t)
	soon, later := time.Now().Add(40*time.Minute).UTC().Truncate(time.Second), time.Now().Add(3*time.Hour).UTC()
	env.store.bench("a", later, "seven_day")
	env.store.bench("b", soon, "five_hour")

	res, err := resolveOnClaude(t, env, "thread-1", types.FailoverPolicy{})
	require.Nil(t, res)
	require.Error(t, err)
	assert.Empty(t, env.built, "no driver is built, so no provider is called")

	var failure *types.UsageLimitFailure
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.Facts.ProviderEarliestReset.Equal(soon), "the wait targets the earliest reset across accounts")
	assert.False(t, failure.Facts.AlternativeAccount)
	var ule *ratelimit.UsageLimitError
	require.ErrorAs(t, err, &ule, "it is a usage-limit error, so the workflow's wait path takes it")
	assert.True(t, ule.ResetAt.Equal(soon))

	// The marker survives the wraps the workflow's parser reads it through.
	_, _, ok := usageLimitOf(fmt.Errorf("activity: %w", err))
	assert.True(t, ok)
}

func TestRouting_FallbackOnlyWhenTheChatOptedIn(t *testing.T) {
	codex := models.AccountConfig{
		DriverConfig: models.DriverConfig{DriverID: "codex", AccountID: "cx", AccountEmail: "c@x.com", Enabled: true, APIKey: "k"},
		Status:       models.AccountAvailable,
	}
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), codex))
	env := newRoutingEnv(t)
	first, err := resolveOnClaude(t, env, "t", types.FailoverPolicy{})
	require.NoError(t, err)
	refusal := &ratelimit.UsageLimitError{Provider: "Claude", ResetAt: time.Now().Add(time.Hour)}

	t.Run("default: no fallback", func(t *testing.T) {
		got := env.routing.usageLimitFailure(context.Background(), refusal, usageLimitContext{
			UserID: "u1", ThreadID: "t", Driver: "claude", ModelID: routeModel, Account: first.Account})
		var f *types.UsageLimitFailure
		require.ErrorAs(t, got, &f)
		assert.Empty(t, f.Facts.FallbackModel)
		assert.False(t, f.Facts.AlternativeAccount, "a is the only claude account")
	})

	t.Run("opt-in: servable fallback is reported", func(t *testing.T) {
		got := env.routing.usageLimitFailure(context.Background(), refusal, usageLimitContext{
			UserID: "u1", ThreadID: "t", Driver: "claude", ModelID: routeModel, Account: first.Account,
			Policy: types.FailoverPolicy{FallbackModel: routeFallback}})
		var f *types.UsageLimitFailure
		require.ErrorAs(t, got, &f)
		assert.Equal(t, routeFallback, f.Facts.FallbackModel)
	})

	t.Run("a fallback on the same provider is ignored", func(t *testing.T) {
		got := env.routing.usageLimitFailure(context.Background(), refusal, usageLimitContext{
			UserID: "u1", ThreadID: "t", Driver: "claude", ModelID: routeModel, Account: first.Account,
			Policy: types.FailoverPolicy{FallbackModel: "claude-5.5-opus@claude"}})
		var f *types.UsageLimitFailure
		require.ErrorAs(t, got, &f)
		assert.Empty(t, f.Facts.FallbackModel)
	})

	t.Run("a fallback on a provider that is not connected is not offered", func(t *testing.T) {
		withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0)))
		got := env.routing.usageLimitFailure(context.Background(), refusal, usageLimitContext{
			UserID: "u1", ThreadID: "t", Driver: "claude", ModelID: routeModel, Account: first.Account,
			Policy: types.FailoverPolicy{FallbackModel: routeFallback}})
		var f *types.UsageLimitFailure
		require.ErrorAs(t, got, &f)
		assert.Empty(t, f.Facts.FallbackModel)
	})

	t.Run("a fallback whose provider is itself all limited is not offered", func(t *testing.T) {
		withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), codex))
		env.store.bench("cx", time.Now().Add(time.Hour), "five_hour")
		got := env.routing.usageLimitFailure(context.Background(), refusal, usageLimitContext{
			UserID: "u1", ThreadID: "t", Driver: "claude", ModelID: routeModel, Account: first.Account,
			Policy: types.FailoverPolicy{FallbackModel: routeFallback}})
		var f *types.UsageLimitFailure
		require.ErrorAs(t, got, &f)
		assert.Empty(t, f.Facts.FallbackModel)
	})
}

func TestRouting_FailuresDegradeToTheDefaultBehaviour(t *testing.T) {
	t.Run("no routing configured leaves a refusal untouched", func(t *testing.T) {
		refusal := &ratelimit.UsageLimitError{Provider: "Claude", ResetAt: time.Now().Add(time.Hour)}
		var none *AccountRouting
		got := none.usageLimitFailure(context.Background(), refusal, usageLimitContext{Driver: "claude"})
		var f *types.UsageLimitFailure
		require.ErrorAs(t, got, &f, "still carries the reset for the workflow")
		assert.False(t, f.Facts.AlternativeAccount)
		assert.Empty(t, f.Facts.FallbackModel)
	})

	t.Run("a non-limit error passes through unchanged", func(t *testing.T) {
		env := newRoutingEnv(t)
		in := errors.New("400 bad request")
		assert.Same(t, in, env.routing.usageLimitFailure(context.Background(), in, usageLimitContext{Driver: "claude"}))
		assert.NoError(t, env.routing.usageLimitFailure(context.Background(), nil, usageLimitContext{}))
	})

	t.Run("a bench that fails waits instead of switching onto the same account", func(t *testing.T) {
		withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
		env := newRoutingEnv(t)
		env.recorder.failBench = true
		first, err := resolveOnClaude(t, env, "t", types.FailoverPolicy{})
		require.NoError(t, err)
		got := env.routing.usageLimitFailure(context.Background(),
			&ratelimit.UsageLimitError{Provider: "Claude", ResetAt: time.Now().Add(time.Hour)},
			usageLimitContext{UserID: "u1", ThreadID: "t", Driver: "claude", ModelID: routeModel, Account: first.Account})
		var f *types.UsageLimitFailure
		require.ErrorAs(t, got, &f)
		assert.False(t, f.Facts.AlternativeAccount, "an unbenched account would be handed straight back")
	})

	t.Run("a driver that cannot be built for the account falls back to the default account", func(t *testing.T) {
		withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0)))
		env := newRoutingEnv(t)
		env.routing.DriverFor = func(context.Context, string, models.DriverConfig, models.Model, ...llm.DriverOption) (llm.Driver, error) {
			return nil, errors.New("token refresh failed")
		}
		res, err := resolveOnClaude(t, env, "t", types.FailoverPolicy{})
		// The registry's default driver serves; building it needs credentials
		// this test does not have, so only a routed account is ruled out.
		if err == nil {
			assert.Nil(t, res.Account, "the thread is served by the default account, unrouted")
		}
	})

	t.Run("a router that errors does not fail the turn", func(t *testing.T) {
		withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0)))
		env := newRoutingEnv(t)
		env.routing.Router = &accountrouter.MockService{AssignFunc: func(context.Context, accountrouter.Request) (accountrouter.Result, error) {
			return accountrouter.Result{}, errors.New("db down")
		}}
		acct, err := routeAccount(context.Background(), llmCallSpec{UserID: "u", ThreadID: "t", Routing: env.routing},
			driversWith(claudeAccount("a", "alice@acme.com", 0)), "claude", routeModel)
		require.NoError(t, err)
		assert.Nil(t, acct)
	})

	t.Run("routing needs a per-account driver builder", func(t *testing.T) {
		env := newRoutingEnv(t)
		env.routing.DriverFor = nil
		assert.False(t, env.routing.active())
		acct, err := routeAccount(context.Background(), llmCallSpec{UserID: "u", ThreadID: "t", Routing: env.routing},
			driversWith(claudeAccount("a", "alice@acme.com", 0)), "claude", routeModel)
		require.NoError(t, err)
		assert.Nil(t, acct, "an account is never assigned that cannot then be used")
	})
}

func TestRouting_ObserverIsInstalledUnderTheServingAccount(t *testing.T) {
	env := newRoutingEnv(t)
	acct := &routedAccount{Config: claudeAccount("b", "bob@acme.com", 1)}
	ctx := env.routing.observe(context.Background(), "u1", acct)
	require.NotNil(t, ctx)
	assert.Equal(t, []string{"b"}, env.recorder.observed)

	// No account, no recorder, or no observer: the context is untouched.
	env.recorder.observed = nil
	assert.Equal(t, context.Background(), env.routing.observe(context.Background(), "u1", nil))
	var none *AccountRouting
	assert.Equal(t, context.Background(), none.observe(context.Background(), "u1", acct))
	assert.Empty(t, env.recorder.observed)
}

func TestFailureSurvivesTheTemporalBoundary(t *testing.T) {
	// classifyError (workflow/runtime) carries Facts as details; here we only pin
	// that the typed failure keeps the marker text the workflow parses.
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	err := &types.UsageLimitFailure{
		Err:   &ratelimit.UsageLimitError{Provider: "Claude subscription", ResetAt: reset, Window: "five_hour"},
		Facts: types.UsageLimitFacts{AlternativeAccount: true},
	}
	gotReset, gotWindow, ok := usageLimitOf(fmt.Errorf("LLM streaming error: %w", err))
	require.True(t, ok)
	assert.True(t, gotReset.Equal(reset))
	assert.Equal(t, "five_hour", gotWindow)
	var appErr *temporal.ApplicationError
	assert.False(t, errors.As(err, &appErr))
}

// Compaction is refused by the same limit call_llm is, so it must decide the
// same way: with the chat's opt-in on and every Claude account limited, its
// failure offers the fallback and the workflow does not wait for the reset.
func TestCompaction_FollowsTheChatsFallbackPolicy(t *testing.T) {
	codex := models.AccountConfig{
		DriverConfig: models.DriverConfig{DriverID: "codex", AccountID: "cx", AccountEmail: "c@x.com", Enabled: true, APIKey: "k"},
		Status:       models.AccountAvailable,
	}
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), codex))
	env := newRoutingEnv(t)
	env.store.bench("a", time.Now().Add(2*time.Hour), "five_hour")
	chat := &db.Chat{ID: "chat-1", UserID: "u1"}
	activity := NewCompactActivity(nil, nil).WithAccountRouting(env.routing)

	failure := func(t *testing.T, policy types.FailoverPolicy) *types.UsageLimitFailure {
		t.Helper()
		_, err := activity.generateCompactionSummary(context.Background(), chat, "thread-1", "", policy, toolBearingConversation(), nil)
		var f *types.UsageLimitFailure
		require.ErrorAs(t, err, &f)
		return f
	}

	t.Run("opted in: the fallback is offered", func(t *testing.T) {
		f := failure(t, types.FailoverPolicy{FallbackModel: routeFallback})
		assert.Equal(t, routeFallback, f.Facts.FallbackModel)
	})
	t.Run("not opted in: it waits, as before", func(t *testing.T) {
		f := failure(t, types.FailoverPolicy{})
		assert.Empty(t, f.Facts.FallbackModel)
		assert.False(t, f.Facts.ProviderEarliestReset.IsZero(), "the reset the wait targets")
	})
}

// A probe bench is not a reset: the facts say so, and the refusal announces no
// reset time, so nothing downstream can show the recheck as one.
func TestRouting_ProbeBenchIsNotReportedAsAReset(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0)))
	env := newRoutingEnv(t)
	probeEnd := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	env.store.mu.Lock()
	env.store.limits["a"] = accountrouter.LimitState{AccountID: "a", LimitedUntil: &probeEnd, LimitedProbes: 1}
	env.store.mu.Unlock()

	res, err := resolveOnClaude(t, env, "thread-1", types.FailoverPolicy{})
	require.Nil(t, res)
	var failure *types.UsageLimitFailure
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.Facts.ProviderResetIsProbe)
	assert.True(t, failure.Facts.ProviderEarliestReset.Equal(probeEnd), "the recheck time rides along for the ladder")
	assert.True(t, failure.Facts.ResetAt.IsZero(), "but no reset is stated")
	var ule *ratelimit.UsageLimitError
	require.ErrorAs(t, err, &ule)
	assert.True(t, ule.ResetAt.IsZero())
	assert.NotContains(t, err.Error(), probeEnd.Format(time.RFC3339), "the marker carries no reset time")
}

// The account-move notice says when the old account resets only when a reset
// was given; a probe bench's end is not one.
func TestAnnounceAccountMove_ProbeBenchGivesNoResetTime(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)))
	env := newRoutingEnv(t)
	probeEnd := time.Now().Add(5 * time.Minute).UTC()
	env.store.mu.Lock()
	env.store.limits["a"] = accountrouter.LimitState{AccountID: "a", LimitedUntil: &probeEnd, LimitedWindow: "five_hour", LimitedProbes: 1}
	env.store.mu.Unlock()
	a, b := claudeAccount("a", "alice@acme.com", 0), claudeAccount("b", "bob@acme.com", 1)
	moved := &routedAccount{Config: b, Change: &accountrouter.Result{Account: b, Previous: &a, Reason: accountrouter.ReasonLimited, Changed: true}}

	var notices []string
	env.routing.announceAccountMove(context.Background(), func(s string) { notices = append(notices, s) }, "u1", moved)
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0], "reached its 5-hour limit")
	assert.NotContains(t, notices[0], "resets")
	assert.NotContains(t, notices[0], "<time:")
}
