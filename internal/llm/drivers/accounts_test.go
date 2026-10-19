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
	ctx := context.Background()
	const user = "multi"

	first := addClaude(t, repo, user, "alice")
	second := addClaude(t, repo, user, "bob")
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "anthropic", "sk-ant-api03-k"))

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

// ---- The legacy expand period: the connect/disconnect RPCs still write only the legacy tables. ----

// A connect that lands only in the legacy tables takes effect immediately, without a restart.
func TestExpand_ALegacyOnlyConnectTakesEffect(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "expand-connect"

	_, err := repo.CopyLegacyCredentials(ctx) // boot copy ran before the user connected
	require.NoError(t, err)

	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "claude", db.ProviderOAuthMarker))
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "legacy-access", RefreshToken: "legacy-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-legacy"))

	available := availableFor(t, repo, user)
	require.Contains(t, available.Drivers, models.DriverID("claude"))
	assert.Equal(t, "legacy-access", available.Drivers["claude"].APIKey)
	assert.Equal(t, "sk-legacy", available.Drivers["openai"].APIKey)
}

// A token rotation the previous release wrote to the legacy table takes effect, so a user never
// runs on a spent single-use refresh token.
func TestExpand_ALegacyOnlyRotationTakesEffect(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "expand-rotate"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "a1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	before := availableFor(t, repo, user)
	require.Equal(t, "r1", before.Drivers["claude"].RefreshToken)

	time.Sleep(10 * time.Millisecond)
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "a2", RefreshToken: "r2", ExpiresAt: time.Now().Add(2 * time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))

	after := availableFor(t, repo, user)
	assert.Equal(t, "a2", after.Drivers["claude"].APIKey)
	assert.Equal(t, "r2", after.Drivers["claude"].RefreshToken)
	assert.Equal(t, before.Drivers["claude"].AccountID, after.Drivers["claude"].AccountID, "the same account, rotated in place")
}

// A disconnect still removes the account: between boots, the stale mirror must not keep serving.
func TestExpand_ALegacyOnlyDisconnectRemovesTheAccount(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "expand-disconnect"

	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "claude", db.ProviderOAuthMarker))
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "a1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "anthropic", "sk-2"))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	require.Contains(t, availableFor(t, repo, user).Drivers, models.DriverID("claude"))

	// The disconnect RPC, exactly as it runs today.
	require.NoError(t, repo.DeleteClaudeAuthTokens(ctx, user))
	require.NoError(t, repo.DeleteProviderAPIKey(ctx, user, "claude"))
	require.NoError(t, repo.DeleteProviderAPIKey(ctx, user, "openai"))

	available := availableFor(t, repo, user)
	assert.NotContains(t, available.Drivers, models.DriverID("claude"))
	assert.NotContains(t, available.Accounts, models.DriverID("claude"))
	assert.NotContains(t, available.Drivers, models.DriverID("openai"))
	assert.Contains(t, available.Drivers, models.DriverID("anthropic"), "no unrelated connection is lost")
}

// No user loses a working connection: an OAuth marker alone (no tokens) is not a credential and
// does not create one, while a real credential survives an arbitrary number of syncs.
func TestExpand_WorkingConnectionsSurviveRepeatedReads(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "expand-stable"

	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "claude", db.ProviderOAuthMarker))
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "a1", RefreshToken: "r1", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "copilot", db.ProviderOAuthMarker)) // marker only: not connected
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1"))

	var firstID string
	for i := range 5 {
		available := availableFor(t, repo, user)
		require.Contains(t, available.Drivers, models.DriverID("claude"), "read %d", i)
		require.Contains(t, available.Drivers, models.DriverID("openai"), "read %d", i)
		assert.NotContains(t, available.Drivers, models.DriverID("copilot"), "a marker is not a connection")
		if i == 0 {
			firstID = available.Drivers["claude"].AccountID
		}
		assert.Equal(t, firstID, available.Drivers["claude"].AccountID, "no churn across reads")
	}
}

// Rotating through the coordinator keeps the previous release's row in step, so a rolling deploy
// never leaves it holding a spent token, and the next read does not mistake our write-through for
// a newer legacy credential.
func TestExpand_CoordinatorRotationReachesTheLegacyRow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "expand-coordinator"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "a1", RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	cfg := availableFor(t, repo, user).Drivers["claude"]

	got, err := CoordinatedAccountRefresh(ctx, repo, user, cfg.AccountID,
		llm.OAuthTokens{AccessToken: cfg.APIKey, RefreshToken: cfg.RefreshToken, ExpiresAt: cfg.TokenExpiresAt}, rotateTo("a2", "r2"))
	require.NoError(t, err)
	assert.Equal(t, "a2", got.AccessToken)

	legacy, err := repo.GetClaudeAuthTokens(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, "r2", legacy.RefreshToken)

	again := availableFor(t, repo, user).Drivers["claude"]
	assert.Equal(t, "a2", again.APIKey)
	assert.Equal(t, cfg.AccountID, again.AccountID)
}

// invalid_grant while the previous release holds a newer lineage in the legacy table: the
// coordinator adopts it from the real stores instead of signing the user out.
func TestExpand_RefreshFailureAdoptsALegacyRotationFromTheRealStore(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "expand-adopt"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "a1", RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	cfg := availableFor(t, repo, user).Drivers["claude"]
	held := llm.OAuthTokens{AccessToken: cfg.APIKey, RefreshToken: cfg.RefreshToken, ExpiresAt: cfg.TokenExpiresAt}

	// The old release rotates the legacy row while our exchange is in flight, and our exchange
	// loses the single-use race.
	time.Sleep(10 * time.Millisecond)
	refresh := func(string) (core.AccountTokens, error) {
		require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
			AccessToken: "legacy-a", RefreshToken: "legacy-r", ExpiresAt: time.Now().Add(2 * time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
		return core.AccountTokens{}, &llm.RefreshRejectedError{Message: "claude session expired: please reconnect Claude"}
	}
	got, err := CoordinatedAccountRefresh(ctx, repo, user, cfg.AccountID, held, refresh)
	require.NoError(t, err)
	assert.Equal(t, "legacy-a", got.AccessToken)

	acct, err := repo.GetProviderAccountWithSecrets(ctx, user, cfg.AccountID)
	require.NoError(t, err)
	assert.Nil(t, acct.NeedsReauthAt, "a working account is not signed out")
	assert.Equal(t, "legacy-r", acct.Secrets.Refresh)
}

// invalid_grant with the legacy row on the same spent lineage: the account needs sign-in and
// leaves Accounts, against the real stores.
func TestExpand_DeadLineageMarksNeedsSignInAndLeavesAccounts(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "expand-dead"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, db.ClaudeAuthTokens{
		AccessToken: "a1", RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "anthropic", "sk-1"))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	cfg := availableFor(t, repo, user).Drivers["claude"]

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
	require.NoError(t, repo.SetProviderAPIKey(ctx, "stale-user", "reliant", "rlnt_abcdef0123456789"))
	stale, err := HasStaleReliantKey(ctx, repo, "stale-user")
	require.NoError(t, err)
	assert.True(t, stale)
	stale, err = HasStaleReliantKey(ctx, repo, "nobody")
	require.NoError(t, err)
	assert.False(t, stale)
}
