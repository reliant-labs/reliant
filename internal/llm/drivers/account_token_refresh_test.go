// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Codex access tokens are JWTs whose exp claim IS the expiry, so these tests mint real
// (unsigned) JWTs. Nothing in the refresh path verifies the signature.
func mintCodexJWT(t *testing.T, accountID string, expiry time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"exp":                         expiry.Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	require.NoError(t, err)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// fakeAccountStore is an in-memory AccountTokenStore with hooks to simulate another process
// mutating the account between reads.
type fakeAccountStore struct {
	mu       sync.Mutex
	accounts map[string]*core.AccountWithSecrets
	getCalls int
	casCalls int
	patched  map[string]string
	upserts  []core.AccountUpsert
	// onGet runs (with the 1-based read count) before each read.
	onGet func(callNum int, s *fakeAccountStore)
}

func newFakeStore(accts ...*core.AccountWithSecrets) *fakeAccountStore {
	s := &fakeAccountStore{accounts: map[string]*core.AccountWithSecrets{}, patched: map[string]string{}}
	for _, a := range accts {
		c := *a // the store owns its copy: tests keep the original as the caller's held view
		s.accounts[a.ID] = &c
	}
	return s
}

func (s *fakeAccountStore) GetProviderAccountWithSecrets(_ context.Context, _, accountID string) (*core.AccountWithSecrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.onGet != nil {
		s.onGet(s.getCalls, s)
	}
	a, ok := s.accounts[accountID]
	if !ok {
		return nil, core.ErrProviderAccountNotFound
	}
	c := *a
	return &c, nil
}

func (s *fakeAccountStore) CompareAndSwapAccountTokens(_ context.Context, _, accountID, expected string, next core.AccountTokens) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.casCalls++
	a, ok := s.accounts[accountID]
	if !ok || a.Secrets.Refresh != expected {
		return false, nil
	}
	a.Secrets.Secret = next.Secret
	if next.Refresh != "" {
		a.Secrets.Refresh = next.Refresh
	}
	a.ExpiresAt = next.ExpiresAt
	return true, nil
}

func (s *fakeAccountStore) PatchProviderAccount(_ context.Context, _, accountID string, p core.AccountPatch) (core.ProviderAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[accountID]
	if !ok {
		return core.ProviderAccount{}, core.ErrProviderAccountNotFound
	}
	if p.NeedsReauth != nil {
		s.patched[accountID] = *p.NeedsReauth
		now := time.Now()
		a.NeedsReauthAt = &now
	}
	return a.ProviderAccount, nil
}

func (s *fakeAccountStore) UpsertProviderAccount(_ context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts = append(s.upserts, u)
	for _, a := range s.accounts {
		if a.IdentityKey == u.ReplaceIdentityOf {
			a.IdentityKey = u.IdentityKey
			a.Secrets = u.Secrets
		}
	}
	return core.ProviderAccount{}, false, nil
}

func (s *fakeAccountStore) set(id string, f func(a *core.AccountWithSecrets)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.accounts[id])
}

func claudeAcct(id, access, refresh string, expires time.Time) *core.AccountWithSecrets {
	return &core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: id, UserID: "u1", Provider: "claude", AuthKind: core.AuthKindOAuth,
			IdentityKey: core.ClaudeIdentityKey("acct-"+id, "org"), ExpiresAt: expires},
		Secrets: core.AccountSecrets{Secret: access, Refresh: refresh},
	}
}

func expired() time.Time { return time.Now().Add(-time.Hour) }
func live() time.Time    { return time.Now().Add(8 * time.Hour) }

func held(a *core.AccountWithSecrets) llm.OAuthTokens {
	return llm.OAuthTokens{AccessToken: a.Secrets.Secret, RefreshToken: a.Secrets.Refresh, ExpiresAt: a.ExpiresAt}
}

func rotateTo(access, refresh string) ProviderRefresh {
	return func(string) (core.AccountTokens, error) {
		return core.AccountTokens{Secret: access, Refresh: refresh, ExpiresAt: live()}, nil
	}
}

// N concurrent callers of ONE account hold the same expired access token and the same
// single-use refresh token. Exactly one exchange happens and everyone shares its result.
func TestAccountRefresh_SingleFlightCollapsesConcurrentCallersOfOneAccount(t *testing.T) {
	acct := claudeAcct("a1", "old", "rt-1", expired())
	store := newFakeStore(acct)
	var calls atomic.Int32
	release := make(chan struct{})
	refresh := func(rt string) (core.AccountTokens, error) {
		calls.Add(1)
		<-release
		if rt != "rt-1" {
			return core.AccountTokens{}, errors.New("claude session expired: please reconnect Claude")
		}
		return core.AccountTokens{Secret: "new", Refresh: "rt-2", ExpiresAt: live()}, nil
	}

	const n = 8
	var wg sync.WaitGroup
	results := make([]llm.OAuthTokens, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), refresh)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, calls.Load(), "one account, one upstream exchange")
	for i := range n {
		require.NoError(t, errs[i])
		assert.Equal(t, "new", results[i].AccessToken)
		assert.Equal(t, "rt-2", results[i].RefreshToken)
	}
	assert.Equal(t, "rt-2", store.accounts["a1"].Secrets.Refresh, "the rotation is persisted")
}

// Two accounts of the same provider refresh at the same time without waiting on each other.
func TestAccountRefresh_DifferentAccountsRefreshConcurrently(t *testing.T) {
	a1 := claudeAcct("a1", "old1", "rt-a1", expired())
	a2 := claudeAcct("a2", "old2", "rt-a2", expired())
	store := newFakeStore(a1, a2)

	var inFlight, peak atomic.Int32
	gate := make(chan struct{})
	refresh := func(rt string) (core.AccountTokens, error) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case <-gate:
		case <-time.After(5 * time.Second):
		}
		inFlight.Add(-1)
		return core.AccountTokens{Secret: "new-" + rt, Refresh: rt + "-next", ExpiresAt: live()}, nil
	}

	var wg sync.WaitGroup
	got := make([]llm.OAuthTokens, 2)
	for i, a := range []*core.AccountWithSecrets{a1, a2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = CoordinatedAccountRefresh(context.Background(), store, "u1", a.ID, held(a), refresh)
		}()
	}
	require.Eventually(t, func() bool { return peak.Load() == 2 }, 3*time.Second, 5*time.Millisecond,
		"both accounts must be inside their exchange at the same time")
	close(gate)
	wg.Wait()

	assert.Equal(t, "new-rt-a1", got[0].AccessToken)
	assert.Equal(t, "new-rt-a2", got[1].AccessToken)
	assert.Equal(t, "rt-a1-next", store.accounts["a1"].Secrets.Refresh)
	assert.Equal(t, "rt-a2-next", store.accounts["a2"].Secrets.Refresh)
}

// Another process already rotated the tokens: adopt them, spend no refresh token.
func TestAccountRefresh_AdoptsTokensRotatedByAnotherProcess(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	store.set("a1", func(a *core.AccountWithSecrets) {
		a.Secrets = core.AccountSecrets{Secret: "fresh", Refresh: "rt-2"}
		a.ExpiresAt = live()
	})

	var calls atomic.Int32
	got, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(string) (core.AccountTokens, error) {
		calls.Add(1)
		return core.AccountTokens{}, errors.New("must not be called")
	})
	require.NoError(t, err)
	assert.Equal(t, "fresh", got.AccessToken)
	assert.Zero(t, calls.Load())
}

// The store owns the lineage: if the persisted refresh token differs (rotated but already near
// expiry again), that one is consumed, not our stale copy.
func TestAccountRefresh_ConsumesPersistedRefreshTokenLineage(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-OLD", expired())
	store := newFakeStore(acct)
	store.set("a1", func(a *core.AccountWithSecrets) { a.Secrets.Refresh = "rt-NEW" })

	var used string
	_, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(rt string) (core.AccountTokens, error) {
		used = rt
		return core.AccountTokens{Secret: "n", Refresh: "rt-3", ExpiresAt: live()}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "rt-NEW", used)
}

// Our exchange fails with invalid_grant because another process consumed the token first and
// persisted a rotation: adopt it rather than report a dead session.
func TestAccountRefresh_RefreshFailureAdoptsConcurrentRotation(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	reads := 0
	store.onGet = func(n int, s *fakeAccountStore) {
		reads = n
		if n == 2 { // the re-read after our exchange failed
			a := s.accounts["a1"]
			a.Secrets = core.AccountSecrets{Secret: "winner", Refresh: "rt-2"}
			a.ExpiresAt = live()
		}
	}
	got, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(string) (core.AccountTokens, error) {
		return core.AccountTokens{}, &llm.RefreshRejectedError{Message: "claude session expired: please reconnect Claude"}
	})
	require.NoError(t, err)
	assert.Equal(t, "winner", got.AccessToken)
	assert.Empty(t, store.patched, "a recovered session is not marked needing sign-in")
	assert.GreaterOrEqual(t, reads, 2)
}

// Our exchange wins but another writer persisted first: the CAS loses and the persisted live
// lineage wins.
func TestAccountRefresh_CASLoserAdoptsPersistedTokens(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	refresh := func(string) (core.AccountTokens, error) {
		// Another process persists between our exchange and our CAS.
		store.set("a1", func(a *core.AccountWithSecrets) {
			a.Secrets = core.AccountSecrets{Secret: "theirs", Refresh: "rt-X"}
			a.ExpiresAt = live()
		})
		return core.AccountTokens{Secret: "ours", Refresh: "rt-2", ExpiresAt: live()}, nil
	}
	got, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), refresh)
	require.NoError(t, err)
	assert.Equal(t, "theirs", got.AccessToken)
	assert.Equal(t, "rt-X", store.accounts["a1"].Secrets.Refresh, "their lineage is not clobbered")
}

// invalid_grant with no newer lineage anywhere: the account needs sign-in, which takes it out of
// resolution so a chat moves to another account instead of failing forever.
func TestAccountRefresh_RejectedGrantWithNoNewerLineageMarksNeedsSignIn(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	_, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(string) (core.AccountTokens, error) {
		return core.AccountTokens{}, &llm.RefreshRejectedError{Message: "claude session expired: please reconnect Claude"}
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reconnect Claude", "the reconnect message still reaches the caller")
	assert.ErrorIs(t, err, llm.ErrRefreshRejected)
	assert.Contains(t, store.patched["a1"], "rejected", "the account is marked needing sign-in")
}

// A refresh that merely FAILS (network, 5xx) says nothing about the credential and must not
// sign the user out.
func TestAccountRefresh_TransientFailureDoesNotMarkNeedsSignIn(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	_, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(string) (core.AccountTokens, error) {
		return core.AccountTokens{}, errors.New("dial tcp: connection reset")
	})
	require.Error(t, err)
	assert.Empty(t, store.patched)
}

func TestAccountRefresh_NoRefreshTokenIsATerminalError(t *testing.T) {
	acct := claudeAcct("a1", "stale", "", expired())
	store := newFakeStore(acct)
	_, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), rotateTo("x", "y"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token")
}

// A concurrent disconnect deleted the account: the refresh must not resurrect it.
func TestAccountRefresh_DoesNotResurrectARemovedAccount(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	refresh := func(string) (core.AccountTokens, error) {
		store.mu.Lock()
		delete(store.accounts, "a1")
		store.mu.Unlock()
		return core.AccountTokens{Secret: "ours", Refresh: "rt-2", ExpiresAt: live()}, nil
	}
	got, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), refresh)
	require.NoError(t, err, "the request that triggered the refresh still gets its token")
	assert.Equal(t, "ours", got.AccessToken)
	_, exists := store.accounts["a1"]
	assert.False(t, exists)
}

// The first refresh response states the account's real identity: a placeholder is
// rewritten in place (design §3.3).
func TestAccountRefresh_RewritesPlaceholderIdentity(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	acct.IdentityKey = core.PlaceholderIdentityKey("row-1")
	store := newFakeStore(acct)
	_, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(string) (core.AccountTokens, error) {
		return core.AccountTokens{Secret: "n", Refresh: "rt-2", ExpiresAt: live(),
			IdentityKey: core.ClaudeIdentityKey("real", "org"), ExternalAccountID: "real", OrganizationID: "org", Email: "a@x.com"}, nil
	})
	require.NoError(t, err)
	require.Len(t, store.upserts, 1)
	assert.Equal(t, core.PlaceholderIdentityKey("row-1"), store.upserts[0].ReplaceIdentityOf)
	assert.Equal(t, core.ClaudeIdentityKey("real", "org"), store.upserts[0].IdentityKey)
	assert.Equal(t, core.ClaudeIdentityKey("real", "org"), store.accounts["a1"].IdentityKey)
}

// An account whose identity is already real is never rewritten.
func TestAccountRefresh_RealIdentityIsLeftAlone(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	_, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(string) (core.AccountTokens, error) {
		return core.AccountTokens{Secret: "n", Refresh: "rt-2", ExpiresAt: live(), IdentityKey: core.ClaudeIdentityKey("other", "org")}, nil
	})
	require.NoError(t, err)
	assert.Empty(t, store.upserts)
}

// Google does not rotate the refresh token: a response without one keeps the grant we consumed.
func TestAccountRefresh_KeepsRefreshTokenWhenProviderDoesNotRotate(t *testing.T) {
	acct := claudeAcct("a1", "stale", "rt-1", expired())
	store := newFakeStore(acct)
	got, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "a1", held(acct), func(string) (core.AccountTokens, error) {
		return core.AccountTokens{Secret: "n", ExpiresAt: live()}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "rt-1", got.RefreshToken)
	assert.Equal(t, "rt-1", store.accounts["a1"].Secrets.Refresh)
}

// Codex has no stored expiry: it is the JWT's exp claim, for the refresh decision and the result.
func TestAccountRefresh_CodexExpiryIsDerivedFromTheJWT(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	fresh := mintCodexJWT(t, "chatgpt-1", exp)
	acct := &core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: "c1", UserID: "u1", Provider: "codex", AuthKind: core.AuthKindOAuth},
		Secrets:         core.AccountSecrets{Secret: mintCodexJWT(t, "chatgpt-1", expired()), Refresh: "rt-1"},
	}
	store := newFakeStore(acct)
	require.True(t, tokenExpired("codex", acct.Secrets.Secret, acct.ExpiresAt))

	got, err := CoordinatedAccountRefresh(context.Background(), store, "u1", "c1", oauthTokensOf(acct), func(string) (core.AccountTokens, error) {
		return core.AccountTokens{Secret: fresh, Refresh: "rt-2", ExpiresAt: codexTokenExpiry(fresh)}, nil
	})
	require.NoError(t, err)
	assert.True(t, exp.Equal(got.ExpiresAt))
	assert.False(t, tokenExpired("codex", fresh, time.Time{}))
}

func TestProviderRefreshersHaveNoDefault(t *testing.T) {
	for _, id := range []string{"claude", "codex", "antigravity"} {
		assert.Contains(t, providerRefreshers, models.DriverID(id))
	}
	assert.NotContains(t, providerRefreshers, models.DriverID("anthropic"), "an API key has no OAuth refresher")
	assert.NotContains(t, providerRefreshers, models.DriverID("copilot"))
	assert.Len(t, providerRefreshers, 3, fmt.Sprint(providerRefreshers))
}

// RefreshingTokens is what the usage poller (accountlimits) runs on.
func TestRefreshingTokens_ExpiredAccountGetsARefreshedToken(t *testing.T) {
	acct := claudeAcct("a1", "expired-access", "rt-1", expired())
	store := newFakeStore(acct)
	orig := providerRefreshers["claude"]
	providerRefreshers["claude"] = rotateTo("refreshed-access", "rt-2")
	t.Cleanup(func() { providerRefreshers["claude"] = orig })

	tok, err := RefreshingTokens{Store: store}.AccessToken(context.Background(), "u1", "a1")
	require.NoError(t, err)
	assert.Equal(t, "refreshed-access", tok)
	assert.Equal(t, "rt-2", store.accounts["a1"].Secrets.Refresh, "persisted through the coordinator")
}

func TestRefreshingTokens_LiveTokenIsReturnedUntouched(t *testing.T) {
	acct := claudeAcct("a1", "live-access", "rt-1", live())
	store := newFakeStore(acct)
	orig := providerRefreshers["claude"]
	providerRefreshers["claude"] = func(string) (core.AccountTokens, error) {
		return core.AccountTokens{}, errors.New("must not refresh a live token")
	}
	t.Cleanup(func() { providerRefreshers["claude"] = orig })

	tok, err := RefreshingTokens{Store: store}.AccessToken(context.Background(), "u1", "a1")
	require.NoError(t, err)
	assert.Equal(t, "live-access", tok)
}

func TestRefreshingTokens_APIKeyAccountNeedsNoRefresh(t *testing.T) {
	store := newFakeStore(&core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: "k1", UserID: "u1", Provider: "anthropic", AuthKind: core.AuthKindAPIKey},
		Secrets:         core.AccountSecrets{Secret: "sk-ant-key"},
	})
	tok, err := RefreshingTokens{Store: store}.AccessToken(context.Background(), "u1", "k1")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-key", tok)
}
