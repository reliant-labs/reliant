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

// During the expand release the connect and disconnect RPCs still write only the legacy tables.
// The driver layer calls SyncLegacyCredentials before it reads accounts, so none of that waits
// for the next boot copy.

func claudeTokens(access, refresh string) ClaudeAuthTokens {
	return ClaudeAuthTokens{AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "acct-1", OrganizationUUID: "org-1"}
}

// A connect that lands only in the legacy tables, after the boot copy, becomes an account.
func TestSyncLegacyCredentialsAdoptsAConnectMadeAfterBoot(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "sync-connect"

	_, err := repo.CopyLegacyCredentials(ctx) // boot: this user has nothing yet
	require.NoError(t, err)
	assert.Empty(t, accountsOf(t, repo, user))

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("a1", "r1")))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "claude", ProviderOAuthMarker))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-new"))

	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))
	got := accountsOf(t, repo, user)
	require.Contains(t, got, "claude")
	assert.Equal(t, "a1", got["claude"].Secrets.Secret)
	require.Contains(t, got, "openai")
	assert.Equal(t, "sk-new", got["openai"].Secrets.Secret)
	assert.NotContains(t, got, "oauth", "the OAuth marker is never an account")
}

// A rotation the previous release wrote to the legacy table replaces the mirror's spent token.
func TestSyncLegacyCredentialsAdoptsALegacyRotation(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "sync-rotation"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("a1", "r1")))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	acct := accountsOf(t, repo, user)["claude"]
	require.Equal(t, "r1", acct.Secrets.Refresh)

	// The old release rotates the legacy row.
	legacyUpdatedAt(t, raw, "claude_auth_tokens", acct.UpdatedAt.Add(-time.Hour)) // make the rotation unambiguous below
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("a2", "r2")))

	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))
	got := accountsOf(t, repo, user)["claude"]
	assert.Equal(t, acct.ID, got.ID, "the same account, rotated in place")
	assert.Equal(t, "a2", got.Secrets.Secret)
	assert.Equal(t, "r2", got.Secrets.Refresh)
}

// A disconnect made in the legacy tables removes the account between boots.
func TestSyncLegacyCredentialsRemovesAnAccountDisconnectedInTheLegacyTables(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "sync-disconnect"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("a1", "r1")))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "anthropic", "sk-2"))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	require.Len(t, accountsOf(t, repo, user), 3)

	require.NoError(t, repo.DeleteClaudeAuthTokens(ctx, user))
	require.NoError(t, repo.DeleteProviderAPIKey(ctx, user, "openai"))
	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))

	got := accountsOf(t, repo, user)
	assert.NotContains(t, got, "claude")
	assert.NotContains(t, got, "openai")
	assert.Contains(t, got, "anthropic", "an untouched connection survives")
}

// The sync is scoped to the user it is asked about: it never prunes or imports for anyone else.
func TestSyncLegacyCredentialsTouchesOnlyThatUser(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.SetProviderAPIKey(ctx, "mine", "openai", "sk-mine"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, "theirs", "openai", "sk-theirs"))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)

	require.NoError(t, repo.DeleteProviderAPIKey(ctx, "mine", "openai"))
	require.NoError(t, repo.DeleteProviderAPIKey(ctx, "theirs", "openai"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, "theirs", "gemini", "g"))

	require.NoError(t, repo.SyncLegacyCredentials(ctx, "mine"))
	assert.NotContains(t, accountsOf(t, repo, "mine"), "openai")
	theirs := accountsOf(t, repo, "theirs")
	assert.Contains(t, theirs, "openai", "someone else's stale mirror is not this call's business")
	assert.NotContains(t, theirs, "gemini")
}

// A native account (no legacy row ever) is never touched by the sync.
func TestSyncLegacyCredentialsLeavesNativeAccountsAlone(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "sync-native"

	native := mustUpsert(t, repo, claudeUpsert(user, "native", "o", "tok", "rt"))
	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1"))
	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))
	require.NoError(t, repo.DeleteProviderAPIKey(ctx, user, "openai"))
	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))

	got := accountsOf(t, repo, user)
	assert.Equal(t, native.ID, got["claude"].ID)
	assert.Equal(t, "tok", got["claude"].Secrets.Secret)
}

// Steady state is cheap: with the accounts level, the sync opens no secret and writes nothing.
func TestSyncLegacyCredentialsIsANoOpWhenLevel(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "sync-level"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("a1", "r1")))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)

	drift, err := repo.legacyDrift(ctx, user)
	require.NoError(t, err)
	assert.False(t, drift, "a freshly copied user has no drift")

	var before time.Time
	require.NoError(t, raw.QueryRow(`SELECT updated_at FROM provider_accounts WHERE user_id = $1`, user).Scan(&before))
	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))
	var after time.Time
	require.NoError(t, raw.QueryRow(`SELECT updated_at FROM provider_accounts WHERE user_id = $1`, user).Scan(&after))
	assert.True(t, before.Equal(after))

	// And drift is detected for each kind of divergence.
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk"))
	drift, err = repo.legacyDrift(ctx, user)
	require.NoError(t, err)
	assert.True(t, drift, "connected in the legacy tables only")
}

// An unreadable legacy credential must not be re-opened on every request.
func TestSyncLegacyCredentialsBacksOffOnAnUnreadableRow(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "sync-broken"

	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1"))
	_, err := raw.Exec(`UPDATE api_keys SET api_key_sealed = '\x00' WHERE user_id = $1`, user)
	require.NoError(t, err)

	require.NoError(t, repo.SyncLegacyCredentials(ctx, user), "an unreadable row never fails the caller")
	until, ok := repo.legacyBackoff.Load(user)
	require.True(t, ok, "the user is backed off")
	assert.True(t, until.(time.Time).After(time.Now()))
	assert.Empty(t, accountsOf(t, repo, user))
}

// Refresh tokens are single-use. A rotation done through the account must reach the legacy row
// the previous release reads, or that release would be left holding a spent token.
func TestAccountRotationIsWrittenThroughToTheLegacyRow(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "writethrough"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("a1", "r1")))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	acct := accountsOf(t, repo, user)["claude"]

	swapped, err := repo.CompareAndSwapAccountTokens(ctx, user, acct.ID, "r1", core.AccountTokens{
		Secret: "a2", Refresh: "r2", ExpiresAt: time.Now().Add(2 * time.Hour)})
	require.NoError(t, err)
	require.True(t, swapped)

	legacy, err := repo.GetClaudeAuthTokens(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, "a2", legacy.AccessToken)
	assert.Equal(t, "r2", legacy.RefreshToken, "the previous release reads the live lineage")
	assert.Equal(t, "acct-1", legacy.AccountUUID, "identity metadata is preserved")

	// Our own write-through is not mistaken for a newer legacy credential.
	drift, err := repo.legacyDrift(ctx, user)
	require.NoError(t, err)
	assert.False(t, drift)
	require.NoError(t, repo.SyncLegacyCredentials(ctx, user))
	assert.Equal(t, "r2", accountsOf(t, repo, user)["claude"].Secrets.Refresh)
}

// If the previous release already rotated the legacy row, our write-through must not clobber it.
func TestAccountRotationDoesNotClobberAnotherLegacyRotation(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "writethrough-race"

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("a1", "r1")))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	acct := accountsOf(t, repo, user)["claude"]

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, claudeTokens("legacy-a", "legacy-r")))
	_, err = repo.CompareAndSwapAccountTokens(ctx, user, acct.ID, "r1", core.AccountTokens{Secret: "a2", Refresh: "r2", ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)

	legacy, err := repo.GetClaudeAuthTokens(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, "legacy-r", legacy.RefreshToken, "the legacy lineage is left exactly as the other release wrote it")
}

// Native accounts have nothing to write through to; their rotation is the account's alone.
func TestNativeAccountRotationWritesNoLegacyRow(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "native-rotation"

	native := mustUpsert(t, repo, claudeUpsert(user, "native", "o", "tok", "rt"))
	swapped, err := repo.CompareAndSwapAccountTokens(ctx, user, native.ID, "rt", core.AccountTokens{Secret: "tok2", Refresh: "rt2", ExpiresAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	require.True(t, swapped)
	legacy, err := repo.GetClaudeAuthTokens(ctx, user)
	require.NoError(t, err)
	assert.Nil(t, legacy)
}
