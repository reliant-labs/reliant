// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claudeUpsert(user, acct, org, secret, refresh string) core.AccountUpsert {
	return core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey(acct, org), Email: acct + "@example.com",
		ExternalAccountID: acct, OrganizationID: org, OrganizationName: "Org " + org,
		Secrets:   core.AccountSecrets{Secret: secret, Refresh: refresh},
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
}

func mustUpsert(t *testing.T, repo *Repo, u core.AccountUpsert) core.ProviderAccount {
	t.Helper()
	a, _, err := repo.UpsertProviderAccount(context.Background(), u)
	require.NoError(t, err)
	return a
}

func priorities(t *testing.T, repo *Repo, user, provider string) []string {
	t.Helper()
	all, err := repo.ListProviderAccounts(context.Background(), user)
	require.NoError(t, err)
	var ids []string
	for _, a := range all {
		if a.Provider == provider {
			require.Equal(t, len(ids), a.Priority, "priorities must be dense and ordered")
			ids = append(ids, a.ID)
		}
	}
	return ids
}

func TestUpsertProviderAccountDedupesByIdentityAndAppendsPriority(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	a1, created, err := repo.UpsertProviderAccount(ctx, claudeUpsert("u1", "alice", "org1", "tok-1", "ref-1"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 0, a1.Priority)

	a2, created, err := repo.UpsertProviderAccount(ctx, claudeUpsert("u1", "bob", "org1", "tok-2", "ref-2"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 1, a2.Priority, "a new account takes priority max+1")

	// Same login in another org is another account (limits and cache are per org).
	a3, created, err := repo.UpsertProviderAccount(ctx, claudeUpsert("u1", "alice", "org2", "tok-3", "ref-3"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 2, a3.Priority)

	// Another user's identical identity is independent, at priority 0.
	other := mustUpsert(t, repo, claudeUpsert("u2", "alice", "org1", "tok-x", "ref-x"))
	assert.Equal(t, 0, other.Priority)

	// Pause + label, then sign in again: tokens refresh, the user's settings survive, needs-sign-in clears.
	label, paused, reason := "work", true, "invalid_grant"
	_, err = repo.PatchProviderAccount(ctx, "u1", a1.ID, core.AccountPatch{Label: &label, Paused: &paused, NeedsReauth: &reason})
	require.NoError(t, err)

	again, created, err := repo.UpsertProviderAccount(ctx, claudeUpsert("u1", "alice", "org1", "tok-1b", "ref-1b"))
	require.NoError(t, err)
	assert.False(t, created, "same identity updates, never duplicates")
	assert.Equal(t, a1.ID, again.ID)
	assert.Equal(t, 0, again.Priority)
	assert.Equal(t, "work", again.Label)
	assert.NotNil(t, again.PausedAt, "re-login must not silently resume a paused account")
	assert.Nil(t, again.NeedsReauthAt, "re-login clears needs-sign-in")
	assert.Empty(t, again.NeedsReauthReason)

	got, err := repo.GetProviderAccountWithSecrets(ctx, "u1", a1.ID)
	require.NoError(t, err)
	assert.Equal(t, core.AccountSecrets{Secret: "tok-1b", Refresh: "ref-1b"}, got.Secrets)

	all, err := repo.ListProviderAccounts(ctx, "u1")
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestUpsertProviderAccountKeepsRefreshTokenWhenReconsentOmitsIt(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "tok-1", "ref-1"))

	u := claudeUpsert("u1", "alice", "o", "tok-2", "")
	u.Email = ""
	_, _, err := repo.UpsertProviderAccount(ctx, u)
	require.NoError(t, err)

	got, err := repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
	require.NoError(t, err)
	assert.Equal(t, "tok-2", got.Secrets.Secret)
	assert.Equal(t, "ref-1", got.Secrets.Refresh)
	assert.Equal(t, "alice@example.com", got.Email)
}

func TestUpsertProviderAccountValidation(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	base := claudeUpsert("u1", "a", "o", "t", "r")

	for name, mutate := range map[string]func(*core.AccountUpsert){
		"no user":        func(u *core.AccountUpsert) { u.UserID = "" },
		"no provider":    func(u *core.AccountUpsert) { u.Provider = "" },
		"no identity":    func(u *core.AccountUpsert) { u.IdentityKey = "" },
		"bad kind":       func(u *core.AccountUpsert) { u.AuthKind = "sso" },
		"no secret":      func(u *core.AccountUpsert) { u.Secrets.Secret = "" },
		"managed id":     func(u *core.AccountUpsert) { u.AuthKind = core.AuthKindManaged },
		"managed second": nil,
	} {
		if mutate == nil {
			continue
		}
		u := base
		mutate(&u)
		_, _, err := repo.UpsertProviderAccount(ctx, u)
		assert.Error(t, err, name)
	}
}

func TestAccountSecretsAreSealedAndBoundToTheRow(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "PLAINTEXT-ACCESS", "PLAINTEXT-REFRESH"))
	b := mustUpsert(t, repo, claudeUpsert("u1", "bob", "o", "other-access", "other-refresh"))

	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT secret_sealed FROM provider_accounts WHERE id=$1`, a.ID).Scan(&sealed))
	assert.NotContains(t, string(sealed), "PLAINTEXT-ACCESS")

	// Ciphertext copied onto another account row of the SAME user and provider must not open:
	// the AAD binds the row id.
	_, err := raw.ExecContext(ctx, `UPDATE provider_accounts SET secret_sealed=$1 WHERE id=$2`, sealed, b.ID)
	require.NoError(t, err)
	_, err = repo.GetProviderAccountWithSecrets(ctx, "u1", b.ID)
	require.Error(t, err, "ciphertext moved between account rows must not open")

	// A column swap within one row must not open either.
	var refresh []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT refresh_sealed FROM provider_accounts WHERE id=$1`, a.ID).Scan(&refresh))
	_, err = raw.ExecContext(ctx, `UPDATE provider_accounts SET secret_sealed=$1 WHERE id=$2`, refresh, a.ID)
	require.NoError(t, err)
	_, err = repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
	require.Error(t, err, "ciphertext moved between columns must not open")
}

func TestListProviderAccountsWithSecretsSkipsUnopenableRow(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "t1", "r1"))
	b := mustUpsert(t, repo, claudeUpsert("u1", "bob", "o", "t2", "r2"))
	_, err := raw.ExecContext(ctx, `UPDATE provider_accounts SET secret_sealed='\x00112233'::bytea WHERE id=$1`, a.ID)
	require.NoError(t, err)

	got, err := repo.ListProviderAccountsWithSecrets(ctx, "u1")
	require.NoError(t, err)
	require.Len(t, got, 1, "one broken credential must not hide the others")
	assert.Equal(t, b.ID, got[0].ID)
	assert.Equal(t, "t2", got[0].Secrets.Secret)

	meta, err := repo.ListProviderAccounts(ctx, "u1")
	require.NoError(t, err)
	assert.Len(t, meta, 2, "metadata listing never opens secrets")
}

func TestAccountsAreScopedToTheirUser(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "t", "r"))

	_, err := repo.GetProviderAccountWithSecrets(ctx, "u2", a.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = repo.PatchProviderAccount(ctx, "u2", a.ID, core.AccountPatch{})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	assert.ErrorIs(t, repo.DeleteProviderAccount(ctx, "u2", a.ID), core.ErrProviderAccountNotFound)
	ok, err := repo.CompareAndSwapAccountTokens(ctx, "u2", a.ID, "r", core.AccountTokens{Secret: "x", Refresh: "y"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.ErrorIs(t, repo.BenchAccount(ctx, "u2", a.ID, time.Now().Add(time.Hour), "", false), core.ErrProviderAccountNotFound)

	still, err := repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
	require.NoError(t, err)
	assert.Equal(t, "t", still.Secrets.Secret)
}

func TestReorderProviderAccounts(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "a", "o", "t", "r"))
	b := mustUpsert(t, repo, claudeUpsert("u1", "b", "o", "t", "r"))
	c := mustUpsert(t, repo, claudeUpsert("u1", "c", "o", "t", "r"))
	codex := mustUpsert(t, repo, core.AccountUpsert{UserID: "u1", Provider: "codex", AuthKind: core.AuthKindOAuth,
		IdentityKey: "acct:x/user:y", Secrets: core.AccountSecrets{Secret: "t"}})
	other := mustUpsert(t, repo, claudeUpsert("u2", "z", "o", "t", "r"))

	require.NoError(t, repo.ReorderProviderAccounts(ctx, "u1", "claude", []string{c.ID, a.ID, b.ID}))
	assert.Equal(t, []string{c.ID, a.ID, b.ID}, priorities(t, repo, "u1", "claude"), "swap through the deferred unique constraint")

	before := priorities(t, repo, "u1", "claude")
	for name, ids := range map[string][]string{
		"missing":   {a.ID, b.ID},
		"extra":     {a.ID, b.ID, c.ID, codex.ID},
		"foreign":   {a.ID, b.ID, other.ID},
		"duplicate": {a.ID, a.ID, b.ID},
		"unknown":   {a.ID, b.ID, "nope"},
		"empty":     {},
	} {
		err := repo.ReorderProviderAccounts(ctx, "u1", "claude", ids)
		assert.ErrorIs(t, err, core.ErrInvalidAccountOrder, name)
	}
	assert.Equal(t, before, priorities(t, repo, "u1", "claude"), "a rejected reorder changes nothing (atomic)")

	assert.Equal(t, []string{codex.ID}, priorities(t, repo, "u1", "codex"))
	assert.Equal(t, []string{other.ID}, priorities(t, repo, "u2", "claude"))
}

func TestDeleteProviderAccountDensifiesAndCascades(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "a", "o", "t", "r"))
	b := mustUpsert(t, repo, claudeUpsert("u1", "b", "o", "t", "r"))
	c := mustUpsert(t, repo, claudeUpsert("u1", "c", "o", "t", "r"))

	chat := createTestChat(t, repo)
	insertTestThreadForWorkflow(t, repo, "th-del", chat, "wf-del", ThreadStatusRunning)
	ok, err := repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-del", Provider: "claude", AccountID: b.ID, Reason: "initial"}, "")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.BenchAccount(ctx, "u1", b.ID, time.Now().Add(time.Hour), "five_hour", false))

	require.NoError(t, repo.DeleteProviderAccount(ctx, "u1", b.ID))
	assert.Equal(t, []string{a.ID, c.ID}, priorities(t, repo, "u1", "claude"), "priorities re-densify")

	asg, err := repo.GetThreadAssignment(ctx, "th-del", "claude")
	require.NoError(t, err)
	assert.Nil(t, asg, "thread assignment cascades; the router reassigns with reason removed")
	limits, err := repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	assert.NotContains(t, limits, b.ID)

	assert.ErrorIs(t, repo.DeleteProviderAccount(ctx, "u1", b.ID), core.ErrProviderAccountNotFound)
}

func TestCompareAndSwapAccountTokensExactlyOneWinner(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "access-0", "refresh-0"))
	exp := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	t.Run("matching swaps, stale does not", func(t *testing.T) {
		ok, err := repo.CompareAndSwapAccountTokens(ctx, "u1", a.ID, "refresh-0",
			core.AccountTokens{Secret: "access-1", Refresh: "refresh-1", ExpiresAt: exp, Plan: "max_5x"})
		require.NoError(t, err)
		assert.True(t, ok)

		ok, err = repo.CompareAndSwapAccountTokens(ctx, "u1", a.ID, "refresh-0",
			core.AccountTokens{Secret: "access-x", Refresh: "refresh-x"})
		require.NoError(t, err)
		assert.False(t, ok, "refresh-0 was consumed; the row holds refresh-1")

		got, err := repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
		require.NoError(t, err)
		assert.Equal(t, "access-1", got.Secrets.Secret)
		assert.Equal(t, "refresh-1", got.Secrets.Refresh)
		assert.Equal(t, "max_5x", got.Plan)
		assert.Equal(t, exp, got.ExpiresAt.UTC())
	})

	t.Run("omitted refresh keeps the stored one", func(t *testing.T) {
		ok, err := repo.CompareAndSwapAccountTokens(ctx, "u1", a.ID, "refresh-1", core.AccountTokens{Secret: "access-2"})
		require.NoError(t, err)
		require.True(t, ok)
		got, err := repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
		require.NoError(t, err)
		assert.Equal(t, "refresh-1", got.Secrets.Refresh)
	})

	t.Run("missing account or empty secret", func(t *testing.T) {
		ok, err := repo.CompareAndSwapAccountTokens(ctx, "u1", "no-such", "r", core.AccountTokens{Secret: "s"})
		require.NoError(t, err)
		assert.False(t, ok)
		_, err = repo.CompareAndSwapAccountTokens(ctx, "u1", a.ID, "refresh-1", core.AccountTokens{})
		assert.Error(t, err)
	})

	t.Run("racing goroutines: one winner, losers see the new lineage", func(t *testing.T) {
		const racers = 8
		var wg sync.WaitGroup
		results := make([]bool, racers)
		errs := make([]error, racers)
		start := make(chan struct{})
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results[i], errs[i] = repo.CompareAndSwapAccountTokens(ctx, "u1", a.ID, "refresh-1",
					core.AccountTokens{Secret: fmt.Sprintf("access-r%d", i), Refresh: fmt.Sprintf("refresh-r%d", i)})
			}()
		}
		close(start)
		wg.Wait()

		winners := 0
		for i := range racers {
			require.NoError(t, errs[i])
			if results[i] {
				winners++
			}
		}
		assert.Equal(t, 1, winners, "single-use refresh tokens: exactly one writer may win")

		got, err := repo.GetProviderAccountWithSecrets(ctx, "u1", a.ID)
		require.NoError(t, err)
		assert.NotEqual(t, "refresh-1", got.Secrets.Refresh)
	})
}

func TestPatchProviderAccount(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "t", "r"))

	label := "personal"
	got, err := repo.PatchProviderAccount(ctx, "u1", a.ID, core.AccountPatch{Label: &label})
	require.NoError(t, err)
	assert.Equal(t, "personal", got.DisplayLabel())

	yes, no := true, false
	got, err = repo.PatchProviderAccount(ctx, "u1", a.ID, core.AccountPatch{Paused: &yes})
	require.NoError(t, err)
	require.NotNil(t, got.PausedAt)
	first := *got.PausedAt
	got, err = repo.PatchProviderAccount(ctx, "u1", a.ID, core.AccountPatch{Paused: &yes})
	require.NoError(t, err)
	assert.True(t, got.PausedAt.Equal(first), "pausing twice keeps the original time")
	assert.Equal(t, "personal", got.Label, "an unset field is left alone")

	got, err = repo.PatchProviderAccount(ctx, "u1", a.ID, core.AccountPatch{Paused: &no})
	require.NoError(t, err)
	assert.Nil(t, got.PausedAt)

	reason := "invalid_grant"
	got, err = repo.PatchProviderAccount(ctx, "u1", a.ID, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	require.NotNil(t, got.NeedsReauthAt)
	assert.Equal(t, "invalid_grant", got.NeedsReauthReason)

	empty := ""
	got, err = repo.PatchProviderAccount(ctx, "u1", a.ID, core.AccountPatch{NeedsReauth: &empty})
	require.NoError(t, err)
	assert.Nil(t, got.NeedsReauthAt)
	assert.Empty(t, got.NeedsReauthReason)

	_, err = repo.PatchProviderAccount(ctx, "u1", "missing", core.AccountPatch{})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
}

func TestManagedAccountIdentityAndAPIKeyAccount(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	m, created, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{UserID: "u1", Provider: "reliant",
		AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey, Secrets: core.AccountSecrets{Secret: "rk-1"}})
	require.NoError(t, err)
	assert.True(t, created)
	m2, created, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{UserID: "u1", Provider: "reliant",
		AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey, Secrets: core.AccountSecrets{Secret: "rk-2"}})
	require.NoError(t, err)
	assert.False(t, created, "at most one managed account per user")
	assert.Equal(t, m.ID, m2.ID)

	key := "sk-ant-api03-abcdWXYZ"
	k := mustUpsert(t, repo, core.AccountUpsert{UserID: "u1", Provider: "anthropic", AuthKind: core.AuthKindAPIKey,
		IdentityKey: core.APIKeyIdentityKey("u1", "anthropic", key), KeyHint: core.KeyHint(key), Secrets: core.AccountSecrets{Secret: key}})
	assert.Equal(t, "Anthropic key …WXYZ", k.DisplayLabel())
	assert.NotEqual(t, core.APIKeyIdentityKey("u1", "anthropic", key), core.APIKeyIdentityKey("u2", "anthropic", key), "salted per user")
}

func TestLimitSnapshotIsMonotonicAndBenchUsesGreatest(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "t", "r"))
	t0 := time.Now().UTC().Truncate(time.Second)
	f := func(v float64) *float64 { return &v }

	snap := func(at time.Time, used float64, status string) core.LimitState {
		return core.LimitState{AccountID: a.ID, UserID: "u1", Status: status, Source: "headers", ObservedAt: at,
			RepresentativeWindow: "five_hour",
			Windows:              []core.LimitWindow{{Name: "five_hour", UsedFraction: f(used), ResetsAt: at.Add(3 * time.Hour), LengthS: 18000}}}
	}
	read := func() core.LimitState {
		m, err := repo.ListAccountLimits(ctx, "u1")
		require.NoError(t, err)
		require.Contains(t, m, a.ID)
		return m[a.ID]
	}

	require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, snap(t0, 0.40, "allowed")))
	require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, snap(t0.Add(-time.Minute), 0.10, "allowed")))
	got := read()
	assert.InDelta(t, 0.40, *got.Windows[0].UsedFraction, 1e-9, "an older snapshot never regresses newer state")
	assert.Equal(t, "five_hour", got.RepresentativeWindow)
	assert.Equal(t, int64(18000), got.Windows[0].LengthS)
	assert.True(t, got.ObservedAt.Equal(t0))

	require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, snap(t0.Add(time.Minute), 0.55, "warning")))
	assert.InDelta(t, 0.55, *read().Windows[0].UsedFraction, 1e-9)
	assert.Equal(t, "warning", read().Status)

	t.Run("bench keeps the later reset", func(t *testing.T) {
		later := t0.Add(5 * time.Hour)
		sooner := t0.Add(2 * time.Hour)
		require.NoError(t, repo.BenchAccount(ctx, "u1", a.ID, later, "seven_day", false))
		require.NoError(t, repo.BenchAccount(ctx, "u1", a.ID, sooner, "five_hour", false))
		got := read()
		require.NotNil(t, got.LimitedUntil)
		assert.True(t, got.LimitedUntil.Equal(later), "GREATEST(existing, until)")
		assert.Equal(t, "seven_day", got.LimitedWindow, "the window that set the bench is kept")
		assert.Equal(t, "rejected", got.Status)
		assert.Equal(t, 0, got.LimitedProbes)
		assert.Len(t, got.Windows, 1, "benching does not erase the observed windows")
	})

	t.Run("probe benches count up, a known reset resets the count", func(t *testing.T) {
		for want := 1; want <= 3; want++ {
			require.NoError(t, repo.BenchAccount(ctx, "u1", a.ID, t0.Add(time.Duration(want)*time.Minute), "", true))
			assert.Equal(t, want, read().LimitedProbes)
		}
		require.NoError(t, repo.BenchAccount(ctx, "u1", a.ID, t0.Add(6*time.Hour), "five_hour", false))
		assert.Equal(t, 0, read().LimitedProbes)
	})

	t.Run("a newer allowed snapshot lifts the bench early", func(t *testing.T) {
		require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, snap(time.Now().UTC().Add(time.Hour), 0.01, "allowed")))
		got := read()
		assert.Nil(t, got.LimitedUntil)
		assert.Equal(t, 0, got.LimitedProbes)
	})

	t.Run("snapshot for an unknown account stores nothing", func(t *testing.T) {
		require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, core.LimitState{AccountID: "ghost", UserID: "u1", ObservedAt: t0}))
		m, err := repo.ListAccountLimits(ctx, "u1")
		require.NoError(t, err)
		assert.NotContains(t, m, "ghost")
	})

	t.Run("bench rejects a zero time", func(t *testing.T) {
		assert.Error(t, repo.BenchAccount(ctx, "u1", a.ID, time.Time{}, "", false))
	})
}

func TestBenchBeforeAnySnapshotCreatesLimitState(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "t", "r"))
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.BenchAccount(ctx, "u1", a.ID, until, "five_hour", false))

	m, err := repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	l := m[a.ID]
	require.NotNil(t, l.LimitedUntil)
	assert.True(t, l.LimitedUntil.Equal(until))
	assert.Empty(t, l.Windows)
	assert.Equal(t, "usage_limit_error", l.Source)

	acct := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "t", "r"))
	assert.Equal(t, core.AccountLimited, acct.Status(&l, time.Now()))
	assert.Equal(t, core.AccountAvailable, acct.Status(&l, until.Add(time.Second)), "benching is lazy: past limited_until is available")
}

func TestAccountStatusPrecedence(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	limited := &core.LimitState{LimitedUntil: &future}
	a := core.ProviderAccount{}
	assert.Equal(t, core.AccountAvailable, a.Status(nil, now))
	assert.Equal(t, core.AccountLimited, a.Status(limited, now))
	a.PausedAt = &now
	assert.Equal(t, core.AccountPaused, a.Status(limited, now))
	a.NeedsReauthAt = &now
	assert.Equal(t, core.AccountNeedsSignIn, a.Status(limited, now))
}

func TestLeaseUsagePollsConcurrentLeasersGetDisjointSets(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	var want []string
	for i := range 40 {
		a := mustUpsert(t, repo, claudeUpsert("u1", fmt.Sprintf("acct%02d", i), "o", "t", "r"))
		next := now.Add(-time.Duration(i+1) * time.Second)
		if i >= 30 {
			next = now.Add(time.Hour) // not due
		} else {
			want = append(want, a.ID)
		}
		require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, next, 0, ""))
	}

	const leasers = 4
	got := make([][]string, leasers)
	errs := make([]error, leasers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range leasers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got[i], errs[i] = repo.LeaseUsagePolls(ctx, now, 12, 2*time.Minute)
		}()
	}
	close(start)
	wg.Wait()

	seen := map[string]int{}
	for i := range leasers {
		require.NoError(t, errs[i])
		for _, id := range got[i] {
			seen[id]++
		}
	}
	var all []string
	for id, n := range seen {
		assert.Equal(t, 1, n, "account %s leased by more than one poller", id)
		all = append(all, id)
	}
	sort.Strings(all)
	sort.Strings(want)
	assert.Equal(t, want, all, "every due account is leased exactly once; the not-yet-due ones are not")

	again, err := repo.LeaseUsagePolls(ctx, now, 50, 2*time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again, "a lease pushes the schedule forward")

	later, err := repo.LeaseUsagePolls(ctx, now.Add(3*time.Minute), 50, 2*time.Minute)
	require.NoError(t, err)
	assert.Len(t, later, 30, "an expired lease is re-leasable (a crashed poller does not lose accounts)")

	none, err := repo.LeaseUsagePolls(ctx, now, 0, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestScheduleUsagePoll(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "alice", "o", "t", "r"))
	next := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)

	require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, next, 2, "429"))
	m, err := repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	l := m[a.ID]
	require.NotNil(t, l.UsageAPINextAt)
	assert.True(t, l.UsageAPINextAt.Equal(next))
	assert.Equal(t, 2, l.UsageAPIFailures)
	assert.Equal(t, "429", l.UsageAPIError)
	assert.True(t, l.ObservedAt.IsZero(), "scheduling is not an observation")

	// A later header snapshot must not disturb the schedule.
	require.NoError(t, repo.UpsertAccountLimitSnapshot(ctx, core.LimitState{AccountID: a.ID, UserID: "u1", Status: "allowed", ObservedAt: time.Now()}))
	m, err = repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	require.NotNil(t, m[a.ID].UsageAPINextAt)
	assert.True(t, m[a.ID].UsageAPINextAt.Equal(next))

	require.NoError(t, repo.ScheduleUsagePoll(ctx, a.ID, time.Time{}, 0, ""))
	m, err = repo.ListAccountLimits(ctx, "u1")
	require.NoError(t, err)
	assert.Nil(t, m[a.ID].UsageAPINextAt, "zero unschedules")

	assert.ErrorIs(t, repo.ScheduleUsagePoll(ctx, "ghost", next, 0, ""), core.ErrProviderAccountNotFound)
}

func TestThreadAssignmentCompareAndSet(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "a", "o", "t", "r"))
	b := mustUpsert(t, repo, claudeUpsert("u1", "b", "o", "t", "r"))
	c := mustUpsert(t, repo, claudeUpsert("u1", "c", "o", "t", "r"))
	chat := createTestChat(t, repo)
	insertTestThreadForWorkflow(t, repo, "th-1", chat, "wf-1", ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-2", chat, "wf-2", ThreadStatusRunning)

	got, err := repo.GetThreadAssignment(ctx, "th-1", "claude")
	require.NoError(t, err)
	assert.Nil(t, got)

	ok, err := repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-1", Provider: "claude", AccountID: a.ID, Reason: "initial"}, "")
	require.NoError(t, err)
	assert.True(t, ok)

	// A second initial pick on the same thread loses; the caller adopts the winner.
	ok, err = repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-1", Provider: "claude", AccountID: b.ID, Reason: "initial"}, "")
	require.NoError(t, err)
	assert.False(t, ok)
	got, err = repo.GetThreadAssignment(ctx, "th-1", "claude")
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.AccountID)
	assert.Equal(t, "initial", got.Reason)
	assert.False(t, got.AssignedAt.IsZero())

	// Moving is conditional on the current account.
	ok, err = repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-1", Provider: "claude", AccountID: c.ID, Reason: "limited"}, b.ID)
	require.NoError(t, err)
	assert.False(t, ok, "expected account is not the current one")
	ok, err = repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-1", Provider: "claude", AccountID: b.ID, Reason: "limited"}, a.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	got, err = repo.GetThreadAssignment(ctx, "th-1", "claude")
	require.NoError(t, err)
	assert.Equal(t, b.ID, got.AccountID)
	assert.Equal(t, "limited", got.Reason)

	// Per (thread, provider): another provider and another thread are independent.
	cx := mustUpsert(t, repo, core.AccountUpsert{UserID: "u1", Provider: "codex", AuthKind: core.AuthKindOAuth,
		IdentityKey: "acct:1/user:1", Secrets: core.AccountSecrets{Secret: "t"}})
	ok, err = repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-1", Provider: "codex", AccountID: cx.ID, Reason: "initial"}, "")
	require.NoError(t, err)
	assert.True(t, ok)
	got, err = repo.GetThreadAssignment(ctx, "th-1", "claude")
	require.NoError(t, err)
	assert.Equal(t, b.ID, got.AccountID)

	t.Run("racing first assignments: one winner", func(t *testing.T) {
		var wg sync.WaitGroup
		wins := make([]bool, 6)
		start := make(chan struct{})
		for i := range wins {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				acct := []string{a.ID, b.ID, c.ID}[i%3]
				wins[i], _ = repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-2", Provider: "claude", AccountID: acct, Reason: "initial"}, "")
			}()
		}
		close(start)
		wg.Wait()
		n := 0
		for _, w := range wins {
			if w {
				n++
			}
		}
		assert.Equal(t, 1, n)
	})

	t.Run("validation and FK", func(t *testing.T) {
		_, err := repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "th-1", Provider: "claude"}, "")
		assert.Error(t, err)
		_, err = repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: "no-thread", Provider: "claude", AccountID: a.ID, Reason: "initial"}, "")
		assert.Error(t, err, "a thread that does not exist cannot be assigned")
	})
}

func TestRecentAssignmentCounts(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	a := mustUpsert(t, repo, claudeUpsert("u1", "a", "o", "t", "r"))
	b := mustUpsert(t, repo, claudeUpsert("u1", "b", "o", "t", "r"))
	idle := mustUpsert(t, repo, claudeUpsert("u1", "idle", "o", "t", "r"))
	chat := createTestChat(t, repo)
	now := time.Now().UTC()

	for i, spec := range []struct {
		acct string
		age  time.Duration
	}{{a.ID, time.Minute}, {a.ID, 2 * time.Minute}, {a.ID, time.Hour}, {b.ID, 30 * time.Second}} {
		th := fmt.Sprintf("th-rc-%d", i)
		insertTestThreadForWorkflow(t, repo, th, chat, "wf-"+th, ThreadStatusRunning)
		ok, err := repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: th, Provider: "claude", AccountID: spec.acct,
			AssignedAt: now.Add(-spec.age), Reason: "initial"}, "")
		require.NoError(t, err)
		require.True(t, ok)
	}

	got, err := repo.RecentAssignmentCounts(ctx, []string{a.ID, b.ID, idle.ID}, now.Add(-5*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{a.ID: 2, b.ID: 1}, got)

	empty, err := repo.RecentAssignmentCounts(ctx, nil, now)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestMessagesProviderAccountIDColumn(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	var typ, nullable string
	require.NoError(t, raw.QueryRow(`SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_name='messages' AND column_name='provider_account_id'`).Scan(&typ, &nullable))
	assert.Equal(t, "text", typ)
	assert.Equal(t, "YES", nullable)
	var fks int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage k USING (constraint_name, table_schema)
		WHERE tc.constraint_type='FOREIGN KEY' AND tc.table_name='messages' AND k.column_name='provider_account_id'`).Scan(&fks))
	assert.Zero(t, fks, "history outlives a removed account")
}

func TestMergeOnIdentityRewrite(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	legacy := claudeUpsert("u1", "x", "o", "old-tok", "old-ref")
	legacy.IdentityKey = core.LegacyIdentityKey("row-1")
	placeholder := mustUpsert(t, repo, legacy)
	other := mustUpsert(t, repo, claudeUpsert("u1", "bob", "o", "t", "r"))

	t.Run("rewrite in place", func(t *testing.T) {
		u := claudeUpsert("u1", "alice", "org1", "new-tok", "new-ref")
		u.ReplaceIdentityOf = core.LegacyIdentityKey("row-1")
		got, created, err := repo.UpsertProviderAccount(ctx, u)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, placeholder.ID, got.ID, "same row, new identity")
		assert.Equal(t, core.ClaudeIdentityKey("alice", "org1"), got.IdentityKey)
		assert.Equal(t, 0, got.Priority)
		s, err := repo.GetProviderAccountWithSecrets(ctx, "u1", got.ID)
		require.NoError(t, err)
		assert.Equal(t, "new-tok", s.Secrets.Secret)
		assert.Equal(t, []string{placeholder.ID, other.ID}, priorities(t, repo, "u1", "claude"))
	})

	t.Run("replacing an identity nobody holds is a plain upsert", func(t *testing.T) {
		u := claudeUpsert("u1", "carol", "org1", "t", "r")
		u.ReplaceIdentityOf = core.LegacyIdentityKey("gone")
		_, created, err := repo.UpsertProviderAccount(ctx, u)
		require.NoError(t, err)
		assert.True(t, created)
	})

	t.Run("merge when the real identity already exists", func(t *testing.T) {
		repo2, cleanup2 := SetupTestDB(t)
		defer cleanup2()
		ph := claudeUpsert("u1", "x", "o", "ph-tok", "ph-ref")
		ph.IdentityKey = core.LegacyIdentityKey("row-2")
		phAcct := mustUpsert(t, repo2, ph) // priority 0
		real := mustUpsert(t, repo2, claudeUpsert("u1", "dave", "o", "real-tok", "real-ref"))
		label := "mine"
		_, err := repo2.PatchProviderAccount(ctx, "u1", phAcct.ID, core.AccountPatch{Label: &label})
		require.NoError(t, err)
		chat := createTestChat(t, repo2)
		insertTestThreadForWorkflow(t, repo2, "th-m1", chat, "wf-m1", ThreadStatusRunning)
		insertTestThreadForWorkflow(t, repo2, "th-m2", chat, "wf-m2", ThreadStatusRunning)
		for _, asg := range []core.Assignment{
			{ThreadID: "th-m1", Provider: "claude", AccountID: phAcct.ID, Reason: "initial"},
			{ThreadID: "th-m2", Provider: "claude", AccountID: real.ID, Reason: "initial"},
		} {
			ok, err := repo2.SetThreadAssignment(ctx, asg, "")
			require.NoError(t, err)
			require.True(t, ok)
		}

		// The placeholder was written after the real row: it is the newer lineage.
		time.Sleep(10 * time.Millisecond)
		u := claudeUpsert("u1", "dave", "o", "fresh-tok", "fresh-ref")
		u.ReplaceIdentityOf = core.LegacyIdentityKey("row-2")
		_, err = repo2.PatchProviderAccount(ctx, "u1", phAcct.ID, core.AccountPatch{})
		require.NoError(t, err)
		merged, created, err := repo2.UpsertProviderAccount(ctx, u)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, core.ClaudeIdentityKey("dave", "o"), merged.IdentityKey)

		all, err := repo2.ListProviderAccounts(ctx, "u1")
		require.NoError(t, err)
		require.Len(t, all, 1, "two identical upstream accounts never coexist")
		assert.Equal(t, 0, all[0].Priority)
		assert.Equal(t, "mine", all[0].Label, "the user's label survives the merge")
		s, err := repo2.GetProviderAccountWithSecrets(ctx, "u1", all[0].ID)
		require.NoError(t, err)
		assert.Equal(t, "fresh-tok", s.Secrets.Secret)

		for _, th := range []string{"th-m1", "th-m2"} {
			asg, err := repo2.GetThreadAssignment(ctx, th, "claude")
			require.NoError(t, err)
			require.NotNil(t, asg, th)
			assert.Equal(t, all[0].ID, asg.AccountID, "assignments are repointed to the survivor")
		}
	})
}

func TestDisplayLabel(t *testing.T) {
	assert.Equal(t, "work", core.ProviderAccount{Label: "work", Email: "a@b.c"}.DisplayLabel())
	assert.Equal(t, "a@b.c", core.ProviderAccount{Email: "a@b.c"}.DisplayLabel())
	assert.Equal(t, "Openai key …1234", core.ProviderAccount{Provider: "openai", KeyHint: "1234"}.DisplayLabel())
	assert.Equal(t, "Claude account", core.ProviderAccount{Provider: "claude"}.DisplayLabel())
}
