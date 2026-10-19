// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

func addClaude(t *testing.T, repo *db.Repo, user, name string) core.ProviderAccount {
	t.Helper()
	a, _, err := repo.UpsertProviderAccount(context.Background(), core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey(name, "org-"+name), Email: name + "@example.com",
		ExternalAccountID: name, OrganizationID: "org-" + name,
		Secrets:   core.AccountSecrets{Secret: "access-" + name, Refresh: "refresh-" + name},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	return a
}

func availableFor(t *testing.T, repo *db.Repo, user string) models.AvailableDrivers {
	t.Helper()
	available, err := buildAvailableDrivers(context.Background(), repo, user, noReportClients)
	require.NoError(t, err)
	return available
}

// Two Claude accounts and an Anthropic key: Accounts[claude] has both in priority order,
// Drivers[claude] is the priority-0 account, and the key is its own provider.
func TestAvailableDrivers_TwoClaudeAccountsAndAnAnthropicKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "multi"

	first := addClaude(t, repo, user, "alice")
	second := addClaude(t, repo, user, "bob")
	db.SeedProviderAccount(t, repo, user, "anthropic", "sk-ant-api03-k")

	available := availableFor(t, repo, user)

	claude := available.Accounts["claude"]
	require.Len(t, claude, 2)
	assert.Equal(t, first.ID, claude[0].AccountID)
	assert.Equal(t, second.ID, claude[1].AccountID)
	assert.Equal(t, 0, claude[0].Priority)
	assert.Equal(t, 1, claude[1].Priority)
	assert.Equal(t, "alice@example.com", claude[0].AccountLabel)
	assert.Equal(t, "access-bob", claude[1].APIKey)
	assert.Equal(t, "refresh-bob", claude[1].RefreshToken)
	assert.Equal(t, "bob", claude[1].AccountUUID)

	assert.Equal(t, claude[0].DriverConfig, available.Drivers["claude"], "Drivers keeps meaning the default account")
	assert.Equal(t, first.ID, available.Drivers["claude"].AccountID)

	require.Len(t, available.Accounts["anthropic"], 1)
	assert.Equal(t, "sk-ant-api03-k", available.Drivers["anthropic"].APIKey)
}

// Reordering changes which account is the default.
func TestAvailableDrivers_DefaultFollowsPriority(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "reorder"
	a := addClaude(t, repo, user, "alice")
	b := addClaude(t, repo, user, "bob")
	require.NoError(t, repo.ReorderProviderAccounts(context.Background(), user, "claude", []string{b.ID, a.ID}))

	available := availableFor(t, repo, user)
	assert.Equal(t, b.ID, available.Drivers["claude"].AccountID)
	assert.Equal(t, b.ID, available.Accounts["claude"][0].AccountID)
}

// A paused account is absent; a limited one is present (limits are the router's business); an
// account needing sign-in is absent; when it was the provider's only account the provider is
// reported unavailable, with a reason.
func TestAvailableDrivers_PausedAbsentLimitedPresentNeedsSignInAbsent(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "states"

	live := addClaude(t, repo, user, "live")
	paused := addClaude(t, repo, user, "paused")
	limited := addClaude(t, repo, user, "limited")
	broken := addClaude(t, repo, user, "broken")

	yes, reason := true, "the provider rejected the saved sign-in"
	_, err := repo.PatchProviderAccount(ctx, user, paused.ID, core.AccountPatch{Paused: &yes})
	require.NoError(t, err)
	_, err = repo.PatchProviderAccount(ctx, user, broken.ID, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.BenchAccount(ctx, user, limited.ID, until, "5h", false))

	available := availableFor(t, repo, user)
	ids := []string{}
	for _, a := range available.Accounts["claude"] {
		ids = append(ids, a.AccountID)
	}
	assert.Equal(t, []string{live.ID, limited.ID}, ids)
	assert.Equal(t, models.AccountAvailable, available.Accounts["claude"][0].Status)
	assert.Equal(t, models.AccountLimited, available.Accounts["claude"][1].Status)
	assert.True(t, until.Equal(available.Accounts["claude"][1].LimitedUntil))

	// Pausing/limiting every account must not take the provider out of resolution (a limited
	// account stays); an only-account needing sign-in does.
	_, err = repo.PatchProviderAccount(ctx, user, live.ID, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	_, err = repo.PatchProviderAccount(ctx, user, limited.ID, core.AccountPatch{NeedsReauth: &reason})
	require.NoError(t, err)
	available = availableFor(t, repo, user)
	assert.NotContains(t, available.Drivers, models.DriverID("claude"))
	assert.Contains(t, available.Unavailable["claude"], "sign in again")
}

// A user whose only Claude account is limited keeps the provider: resolution must not silently
// move an unpinned chat to Codex.
func TestAvailableDrivers_AFullyLimitedProviderStaysResolvable(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "all-limited"
	a := addClaude(t, repo, user, "alice")
	require.NoError(t, repo.BenchAccount(context.Background(), user, a.ID, time.Now().Add(time.Hour), "5h", false))

	available := availableFor(t, repo, user)
	require.Contains(t, available.Drivers, models.DriverID("claude"))
	assert.Equal(t, models.AccountLimited, available.Accounts["claude"][0].Status)
}

// Each Codex account derives its own installation id: Codex enforces one account per device.
func TestAvailableDrivers_CodexAccountsCarryDistinctInstallationDiscriminators(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "codex-two"
	for _, name := range []string{"alice", "bob"} {
		_, _, err := repo.UpsertProviderAccount(context.Background(), core.AccountUpsert{
			UserID: user, Provider: "codex", AuthKind: core.AuthKindOAuth,
			IdentityKey: core.CodexIdentityKey("chatgpt-"+name, "u"), ExternalAccountID: "chatgpt-" + name,
			Secrets: core.AccountSecrets{Secret: mintCodexJWT(t, "chatgpt-"+name, time.Now().Add(time.Hour)), Refresh: "rt-" + name},
		})
		require.NoError(t, err)
	}
	codex := availableFor(t, repo, user).Accounts["codex"]
	require.Len(t, codex, 2)
	assert.Equal(t, "chatgpt-alice", codex[0].AccountUUID)
	assert.Equal(t, "chatgpt-bob", codex[1].AccountUUID)
	assert.NotEqual(t, codex[0].AccountUUID, codex[1].AccountUUID,
		"the discriminator codex.deriveInstallationID keys on differs per account")
	assert.False(t, codex[0].TokenExpiresAt.IsZero(), "Codex expiry is derived from the JWT")
}

// One-shot callers (image/video generation, key validation) have no thread: they use the default
// account.
func TestImageGenerationUsesTheDefaultAccount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	InitializeAPIKeyProvider(repo)
	previous := AccountAvailabilityClient
	AccountAvailabilityClient = noReportClients
	t.Cleanup(func() { AccountAvailabilityClient = previous })

	const user = "image-default"
	ctx := context.Background()
	for _, key := range []string{"g-first", "g-second"} {
		_, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
			UserID: user, Provider: "gemini", AuthKind: core.AuthKindAPIKey,
			IdentityKey: core.APIKeyIdentityKey(user, "gemini", key), Secrets: core.AccountSecrets{Secret: key},
		})
		require.NoError(t, err)
	}

	available := GetAvailableDrivers(ctx, user)
	require.Len(t, available.Accounts["gemini"], 2)
	assert.Equal(t, "g-first", available.Drivers["gemini"].APIKey)

	resolved, avail, err := resolveMediaModel(ctx, user, models.ModelSelector{Tags: []string{"image-gen"}}, models.ModalityImage)
	require.NoError(t, err)
	if resolved.Provider.Driver == "gemini" {
		assert.Equal(t, "g-first", avail.Drivers["gemini"].APIKey)
	}
}

// GetDriverForAccount builds the driver from the account it is handed, not from Drivers[driver].
func TestGetDriverForAccount_UsesTheGivenAccountsCredential(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	const user = "for-account"
	addClaude(t, repo, user, "alice")
	addClaude(t, repo, user, "bob")
	available := availableFor(t, repo, user)

	def, ok := models.MustGetRegistry().GetDefinition("claude-5.5-opus")
	require.True(t, ok)
	model := def.ToModel()

	var seenKey string
	spy := func(o *llm.DriverOptions) { seenKey = o.ApiKey }
	for _, want := range []string{"access-alice", "access-bob"} {
		var cfg models.DriverConfig
		for _, a := range available.Accounts["claude"] {
			if a.APIKey == want {
				cfg = a.DriverConfig
			}
		}
		drv, err := GetDriverForAccount(context.Background(), user, cfg, model, spy)
		require.NoError(t, err)
		assert.Equal(t, "claude", drv.Name())
		assert.Equal(t, want, seenKey)
	}
}

// A refresh the provider rejects marks the account needs-sign-in and removes it from Accounts,
// against the real stores; the user's other providers are untouched.
func TestDeadLineageMarksNeedsSignInAndLeavesAccounts(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "dead-lineage"

	a, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey("acct", "org"), Secrets: core.AccountSecrets{Secret: "a1", Refresh: "r1"},
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)
	db.SeedProviderAccount(t, repo, user, "anthropic", "sk-1")
	cfg := availableFor(t, repo, user).Drivers["claude"]
	require.Equal(t, a.ID, cfg.AccountID)

	_, err = CoordinatedAccountRefresh(ctx, repo, user, cfg.AccountID,
		llm.OAuthTokens{AccessToken: cfg.APIKey, RefreshToken: cfg.RefreshToken, ExpiresAt: cfg.TokenExpiresAt},
		func(string) (core.AccountTokens, error) {
			return core.AccountTokens{}, &llm.RefreshRejectedError{Message: "claude session expired: please reconnect Claude"}
		})
	require.Error(t, err)

	available := availableFor(t, repo, user)
	assert.NotContains(t, available.Accounts, models.DriverID("claude"))
	assert.Contains(t, available.Unavailable["claude"], "sign in again")
	assert.Contains(t, available.Drivers, models.DriverID("anthropic"))
}

// The reliant key heal still sees the key: HasStaleReliantKey reads accounts now.
func TestHasStaleReliantKey_ReadsTheAccount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	db.SeedProviderAccount(t, repo, "stale-user", "reliant", "rlnt_abcdef0123456789")
	stale, err := HasStaleReliantKey(ctx, repo, "stale-user")
	require.NoError(t, err)
	assert.True(t, stale)
	stale, err = HasStaleReliantKey(ctx, repo, "nobody")
	require.NoError(t, err)
	assert.False(t, stale)
}

// S9 (the integration review's scenario): a copied Claude row with no account uuid carries a
// placeholder identity. The user then adds the SAME upstream account through "Add another
// account", which stores it under its real identity and gives it a pause, a bench and a poll
// schedule. When the placeholder's next refresh learns the real identity, the two merge, and the
// account the user configured must survive, with every one of those.
func TestRefreshMergeKeepsTheAccountWithTheRealIdentity(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "s9-user"

	real := addClaude(t, repo, user, "alice")
	label := "work"
	_, err := repo.PatchProviderAccount(ctx, user, real.ID, core.AccountPatch{Label: &label})
	require.NoError(t, err)
	require.NoError(t, repo.BenchAccount(ctx, user, real.ID, time.Now().Add(time.Hour), "five_hour", false))
	require.NoError(t, repo.ScheduleUsagePoll(ctx, real.ID, time.Now().Add(time.Minute), 0, ""))

	time.Sleep(10 * time.Millisecond)
	placeholder, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.PlaceholderIdentityKey("copied-row"), Secrets: core.AccountSecrets{Secret: "old-access", Refresh: "old-refresh"},
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)

	_, err = CoordinatedAccountRefresh(ctx, repo, user, placeholder.ID,
		llm.OAuthTokens{AccessToken: "old-access", RefreshToken: "old-refresh"},
		func(string) (core.AccountTokens, error) {
			return core.AccountTokens{
				Secret: "new-access", Refresh: "new-refresh", ExpiresAt: time.Now().Add(time.Hour),
				IdentityKey: core.ClaudeIdentityKey("alice", "org-alice"), ExternalAccountID: "alice", OrganizationID: "org-alice",
			}, nil
		})
	require.NoError(t, err)

	all, err := repo.ListProviderAccounts(ctx, user)
	require.NoError(t, err)
	require.Len(t, all, 1, "merged into one account")
	assert.Equal(t, real.ID, all[0].ID, "the account the user configured survives, not the placeholder")
	assert.Equal(t, "work", all[0].Label)
	limits, err := repo.GetAccountLimits(ctx, real.ID)
	require.NoError(t, err)
	require.NotNil(t, limits)
	assert.NotNil(t, limits.LimitedUntil, "its bench survives")
	assert.NotNil(t, limits.UsageAPINextAt, "its poll schedule survives")
	got, err := repo.GetProviderAccountWithSecrets(ctx, user, real.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-refresh", got.Secrets.Refresh, "and it carries the fresh lineage")
}
