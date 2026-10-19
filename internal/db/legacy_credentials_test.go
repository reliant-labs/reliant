// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	require.NoError(t, err)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

func codexIDToken(t *testing.T, acct, user, email, plan string) string {
	return fakeJWT(t, map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": acct, "chatgpt_account_user_id": user, "chatgpt_plan_type": plan,
		},
	})
}

func accountsOf(t *testing.T, repo *Repo, user string) map[string]core.AccountWithSecrets {
	t.Helper()
	all, err := repo.ListProviderAccountsWithSecrets(context.Background(), user)
	require.NoError(t, err)
	out := map[string]core.AccountWithSecrets{}
	for _, a := range all {
		out[a.Provider] = a
	}
	return out
}

func legacyUpdatedAt(t *testing.T, raw *sql.DB, table string, at time.Time) {
	t.Helper()
	//nolint:gosec // test-only table name constant
	_, err := raw.Exec(fmt.Sprintf(`UPDATE %s SET updated_at = $1`, table), at.UTC())
	require.NoError(t, err)
}

func TestCopyLegacyCredentialsMirrorsEveryTable(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "legacy-user"
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	idToken := codexIDToken(t, "cgpt-acct", "cgpt-user", "codex@example.com", "plus")

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "cl-access", RefreshToken: "cl-refresh", ExpiresAt: exp,
		AccountUUID: "cl-acct", AccountEmail: "claude@example.com", OrganizationUUID: "cl-org", OrganizationName: "Acme", Scope: "user:inference"}))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, user, CodexAuthTokens{AccessToken: "cx-access", RefreshToken: "cx-refresh", IDToken: idToken, AccountID: "cgpt-acct"}))
	require.NoError(t, repo.SetCopilotAuthTokens(ctx, user, CopilotAuthTokens{GitHubAccessToken: "gh-access", GitHubRefreshToken: "gh-refresh", Tier: "individual"}))
	require.NoError(t, repo.SetAntigravityAuthTokens(ctx, user, AntigravityAuthTokens{AccessToken: "ag-access", RefreshToken: "ag-refresh", ExpiresAt: exp,
		IDToken: fakeJWT(t, map[string]any{"sub": "google-sub-1", "email": "ag@example.com"}), Scope: "openid"}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "anthropic", "sk-ant-api03-ZZZZ1234"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-openai-AAAA5678"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "reliant", "rk-managed"))
	// Markers, automation credentials and cleared keys are not accounts.
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "claude", ProviderOAuthMarker))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "codex", ProviderOAuthMarker))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, core.AutomationProviderPrefix+"daemon-1", "automation-secret"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "gemini", ""))

	res, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 7, res.Created)
	assert.Zero(t, res.Failed)

	got := accountsOf(t, repo, user)
	require.Len(t, got, 7)

	cl := got["claude"]
	assert.Equal(t, core.AuthKindOAuth, cl.AuthKind)
	assert.Equal(t, core.ClaudeIdentityKey("cl-acct", "cl-org"), cl.IdentityKey)
	assert.Equal(t, core.AccountSecrets{Secret: "cl-access", Refresh: "cl-refresh"}, cl.Secrets)
	assert.Equal(t, "claude@example.com", cl.Email)
	assert.Equal(t, "cl-org", cl.OrganizationID)
	assert.Equal(t, "Acme", cl.OrganizationName)
	assert.Equal(t, "user:inference", cl.Scope)
	assert.Equal(t, 0, cl.Priority)
	assert.True(t, cl.ExpiresAt.Equal(exp))

	cx := got["codex"]
	assert.Equal(t, core.CodexIdentityKey("cgpt-acct", "cgpt-user"), cx.IdentityKey)
	assert.Equal(t, "codex@example.com", cx.Email)
	assert.Equal(t, "plus", cx.Plan)
	assert.Equal(t, "plus", cx.PlanRaw)
	assert.Equal(t, "cgpt-acct", cx.ExternalAccountID)
	assert.Equal(t, core.AccountSecrets{Secret: "cx-access", Refresh: "cx-refresh", IDToken: idToken}, cx.Secrets)

	gh := got["copilot"]
	assert.Equal(t, core.AuthKindDevice, gh.AuthKind)
	assert.True(t, core.IsLegacyIdentityKey(gh.IdentityKey), "copilot's GitHub id is learned at the next connect; until then a placeholder")
	assert.Equal(t, "individual", gh.PlanRaw)
	assert.Equal(t, core.AccountSecrets{Secret: "gh-access", Refresh: "gh-refresh"}, gh.Secrets)

	ag := got["antigravity"]
	assert.Equal(t, core.GoogleIdentityKey("google-sub-1"), ag.IdentityKey)
	assert.Equal(t, "ag@example.com", ag.Email)
	assert.Equal(t, "ag-refresh", ag.Secrets.Refresh)

	an := got["anthropic"]
	assert.Equal(t, core.AuthKindAPIKey, an.AuthKind)
	assert.Equal(t, core.APIKeyIdentityKey(user, "anthropic", "sk-ant-api03-ZZZZ1234"), an.IdentityKey)
	assert.Equal(t, "1234", an.KeyHint)
	assert.Equal(t, "sk-ant-api03-ZZZZ1234", an.Secrets.Secret)

	rl := got["reliant"]
	assert.Equal(t, core.AuthKindManaged, rl.AuthKind)
	assert.Equal(t, core.ManagedIdentityKey, rl.IdentityKey)

	assert.NotContains(t, got, "gemini")

	// The legacy tables are untouched: main keeps reading them.
	tok, err := repo.GetClaudeAuthTokens(ctx, user)
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.Equal(t, "cl-refresh", tok.RefreshToken)
	key, err := repo.GetProviderAPIKey(ctx, user, "anthropic")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-api03-ZZZZ1234", key)
	var legacyRows int
	require.NoError(t, raw.QueryRow(`SELECT (SELECT count(*) FROM claude_auth_tokens) + (SELECT count(*) FROM codex_auth_tokens)
		+ (SELECT count(*) FROM copilot_auth_tokens) + (SELECT count(*) FROM antigravity_auth_tokens) + (SELECT count(*) FROM api_keys)`).Scan(&legacyRows))
	assert.Equal(t, 4+7, legacyRows+0, "no legacy row is removed or added")
}

func TestCopyLegacyCredentialsIsIdempotent(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "idem-user"
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour),
		AccountUUID: "acct", OrganizationUUID: "org"}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1111"))

	first, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, first.Created)

	var before []byte
	require.NoError(t, raw.QueryRow(`SELECT secret_sealed FROM provider_accounts WHERE user_id=$1 AND provider='claude'`, user).Scan(&before))
	snapshot := func() string {
		rows, err := raw.Query(`SELECT id, identity_key, priority, updated_at::text, md5(secret_sealed) FROM provider_accounts ORDER BY id`)
		require.NoError(t, err)
		defer rows.Close()
		var s string
		for rows.Next() {
			var id, ik, up, h string
			var p int
			require.NoError(t, rows.Scan(&id, &ik, &p, &up, &h))
			s += fmt.Sprint(id, ik, p, up, h, ";")
		}
		return s
	}
	want := snapshot()

	for range 3 {
		res, err := repo.CopyLegacyCredentials(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, res.Unchanged)
		assert.Zero(t, res.Created+res.Updated+res.Pruned+res.Failed)
	}
	assert.Equal(t, want, snapshot(), "a re-run changes nothing: not even a re-seal or an updated_at bump")
}

func TestCopyLegacyCredentialsConcurrentBootsDoNotDuplicate(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	for i := range 5 {
		user := fmt.Sprintf("boot-user-%d", i)
		require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "acct", OrganizationUUID: "org"}))
		require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-"+user))
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	var mu sync.Mutex
	created := 0
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := repo.CopyLegacyCredentials(ctx)
			errs[i] = err
			mu.Lock()
			created += res.Created
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 10, created, "API server and worker booting together create each account once")
	for i := range 5 {
		all, err := repo.ListProviderAccounts(ctx, fmt.Sprintf("boot-user-%d", i))
		require.NoError(t, err)
		assert.Len(t, all, 2)
	}
}

func TestCopyLegacyCredentialsNewerLegacyRowWins(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "rolling-user"
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a1", RefreshToken: "r1", ExpiresAt: exp, AccountUUID: "acct", OrganizationUUID: "org"}))
	legacyUpdatedAt(t, raw, "claude_auth_tokens", base)
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)

	t.Run("previous release rotates the legacy row after the copy", func(t *testing.T) {
		require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a2", RefreshToken: "r2", ExpiresAt: exp, AccountUUID: "acct", OrganizationUUID: "org"}))
		res, err := repo.CopyLegacyCredentials(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, res.Updated)
		got := accountsOf(t, repo, user)["claude"]
		assert.Equal(t, core.AccountSecrets{Secret: "a2", Refresh: "r2"}, got.Secrets, "adopts the rotation the old release wrote")
		all, _ := repo.ListProviderAccounts(ctx, user)
		assert.Len(t, all, 1)
	})

	t.Run("an account written by new code is never clobbered by an older legacy row", func(t *testing.T) {
		acct := accountsOf(t, repo, user)["claude"]
		ok, err := repo.CompareAndSwapAccountTokens(ctx, user, acct.ID, "r2", core.AccountTokens{Secret: "a3", Refresh: "r3"})
		require.NoError(t, err)
		require.True(t, ok)
		legacyUpdatedAt(t, raw, "claude_auth_tokens", time.Now().UTC().Add(-time.Minute)) // older than the CAS

		res, err := repo.CopyLegacyCredentials(ctx)
		require.NoError(t, err)
		assert.Zero(t, res.Updated)
		assert.Equal(t, "r3", accountsOf(t, repo, user)["claude"].Secrets.Refresh)
	})

	t.Run("a legacy row newer than the account's last write wins again", func(t *testing.T) {
		require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a4", RefreshToken: "r4", ExpiresAt: exp, AccountUUID: "acct", OrganizationUUID: "org"}))
		_, err := repo.CopyLegacyCredentials(ctx)
		require.NoError(t, err)
		assert.Equal(t, "r4", accountsOf(t, repo, user)["claude"].Secrets.Refresh)
	})

	t.Run("a user label and priority survive a legacy refresh", func(t *testing.T) {
		acct := accountsOf(t, repo, user)["claude"]
		label := "kept"
		_, err := repo.PatchProviderAccount(ctx, user, acct.ID, core.AccountPatch{Label: &label})
		require.NoError(t, err)
		require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a5", RefreshToken: "r5", ExpiresAt: exp, AccountUUID: "acct", OrganizationUUID: "org"}))
		_, err = repo.CopyLegacyCredentials(ctx)
		require.NoError(t, err)
		got := accountsOf(t, repo, user)["claude"]
		assert.Equal(t, "r5", got.Secrets.Refresh)
		assert.Equal(t, "kept", got.Label)
	})
}

func TestCopyLegacyCredentialsPlaceholderIdentityIsRewrittenNotDuplicated(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "placeholder-user"
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	// A legacy Claude row with no account uuid (signed in before the identity was captured).
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a1", RefreshToken: "r1", ExpiresAt: exp}))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	acct := accountsOf(t, repo, user)["claude"]
	require.True(t, core.IsLegacyIdentityKey(acct.IdentityKey))

	// The first refresh through new code learns the identity and rewrites it in place.
	u := claudeUpsert(user, "learned", "org", "a2", "r2")
	u.ReplaceIdentityOf = acct.IdentityKey
	rewritten, created, err := repo.UpsertProviderAccount(ctx, u)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, acct.ID, rewritten.ID)

	// The previous release then rotates the (still identity-less) legacy row, and the next boot copies it.
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a3", RefreshToken: "r3", ExpiresAt: exp}))
	res, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Created, "the legacy row is the account already under its learned identity, not a new one")

	all, err := repo.ListProviderAccounts(ctx, user)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, core.ClaudeIdentityKey("learned", "org"), all[0].IdentityKey, "the learned identity is not regressed to a placeholder")
	_ = raw
}

func TestCopyLegacyCredentialsSkipsUnopenableRowWithoutBlockingOthers(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, "broken-user", ClaudeAuthTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "x", OrganizationUUID: "y"}))
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, "fine-user", ClaudeAuthTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "x", OrganizationUUID: "y"}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, "broken-user", "openai", "sk-ok"))
	_, err := raw.Exec(`UPDATE claude_auth_tokens SET refresh_token_sealed = '\xdeadbeef'::bytea WHERE user_id = 'broken-user'`)
	require.NoError(t, err)

	res, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err, "a credential that does not open must never fail boot")
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, 2, res.Created)

	broken := accountsOf(t, repo, "broken-user")
	assert.NotContains(t, broken, "claude", "the user sees needs-sign-in for that provider")
	assert.Contains(t, broken, "openai", "the user's other credentials still copy")
	assert.Contains(t, accountsOf(t, repo, "fine-user"), "claude")
}

func TestCopyLegacyCredentialsPrunesMirrorOfDisconnectedLegacyRow(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "prune-user"
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "anthropic", "sk-2"))
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "x", OrganizationUUID: "y"}))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)

	// A native account that never mirrored anything must survive pruning.
	native := mustUpsert(t, repo, core.AccountUpsert{UserID: user, Provider: "gemini", AuthKind: core.AuthKindAPIKey,
		IdentityKey: core.APIKeyIdentityKey(user, "gemini", "g"), Secrets: core.AccountSecrets{Secret: "g"}})

	require.NoError(t, repo.DeleteProviderAPIKey(ctx, user, "openai"))
	require.NoError(t, repo.DeleteClaudeAuthTokens(ctx, user))
	res, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Pruned, "disconnecting in the previous release is not undone")

	got := accountsOf(t, repo, user)
	assert.NotContains(t, got, "openai")
	assert.NotContains(t, got, "claude")
	assert.Contains(t, got, "anthropic")
	assert.Equal(t, native.ID, got["gemini"].ID)
}

func TestDeletingAMirroredAccountDeletesItsLegacyRow(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "delete-user"
	require.NoError(t, repo.SetProviderAPIKey(ctx, user, "openai", "sk-1"))
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), AccountUUID: "x", OrganizationUUID: "y"}))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)

	acct := accountsOf(t, repo, user)
	require.NoError(t, repo.DeleteProviderAccount(ctx, user, acct["openai"].ID))
	require.NoError(t, repo.DeleteProviderAccount(ctx, user, acct["claude"].ID))

	_, err = repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	assert.Empty(t, accountsOf(t, repo, user), "deleting an account in the new model must not be resurrected by the next boot")
	tok, err := repo.GetClaudeAuthTokens(ctx, user)
	require.NoError(t, err)
	assert.Nil(t, tok, "main, still reading the legacy tables, sees the disconnect")
}

func TestLegacyCredentialSourceReadsTheMirroredRow(t *testing.T) {
	repo, _, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	const user = "source-user"
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a1", RefreshToken: "r1", ExpiresAt: exp, AccountUUID: "x", OrganizationUUID: "y"}))
	_, err := repo.CopyLegacyCredentials(ctx)
	require.NoError(t, err)
	acct := accountsOf(t, repo, user)["claude"]

	var src core.LegacyCredentialSource = repo
	cred, err := src.ReadLegacyCredential(ctx, user, acct.ID)
	require.NoError(t, err)
	require.NotNil(t, cred)
	assert.Equal(t, postgresstore.LegacySourceClaude, cred.Source)
	assert.Equal(t, "r1", cred.Secrets.Refresh)

	// The previous release rotates; a failed refresh in new code adopts the newer lineage.
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, user, ClaudeAuthTokens{AccessToken: "a2", RefreshToken: "r2", ExpiresAt: exp, AccountUUID: "x", OrganizationUUID: "y"}))
	cred, err = src.ReadLegacyCredential(ctx, user, acct.ID)
	require.NoError(t, err)
	assert.Equal(t, "r2", cred.Secrets.Refresh)
	assert.True(t, cred.UpdatedAt.After(acct.UpdatedAt) || cred.UpdatedAt.Equal(acct.UpdatedAt))

	// Native account, missing account, and a row gone from the legacy table.
	native := mustUpsert(t, repo, claudeUpsert(user, "native", "o", "t", "r"))
	cred, err = src.ReadLegacyCredential(ctx, user, native.ID)
	require.NoError(t, err)
	assert.Nil(t, cred)
	_, err = src.ReadLegacyCredential(ctx, user, "ghost")
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = src.ReadLegacyCredential(ctx, "someone-else", acct.ID)
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	require.NoError(t, repo.DeleteClaudeAuthTokens(ctx, user))
	cred, err = src.ReadLegacyCredential(ctx, user, acct.ID)
	require.NoError(t, err)
	assert.Nil(t, cred)
}

func TestLegacySealingAADsDoNotOpenAsAccountColumns(t *testing.T) {
	// The boot copy exists because the old AAD binds the table; prove the new AAD really differs.
	assert.NotEqual(t, postgresstore.LegacyOAuthTokenAAD("claude_auth_tokens", "u", "access_token"), postgresstore.ProviderAccountAAD("acct", "secret"))
	assert.NotEqual(t, postgresstore.ProviderAccountAAD("a", "secret"), postgresstore.ProviderAccountAAD("b", "secret"))
	assert.NotEqual(t, postgresstore.ProviderAccountAAD("a", "secret"), postgresstore.ProviderAccountAAD("a", "refresh"))
}

func TestCodexIdentityFromJWT(t *testing.T) {
	id, ok := core.CodexIdentityFromJWT(codexIDToken(t, "A", "U", "me@x.io", "pro"), "")
	require.True(t, ok)
	assert.Equal(t, "acct:A/user:U", id.IdentityKey)
	assert.Equal(t, "me@x.io", id.Email)
	assert.Equal(t, "pro", id.Plan)

	// chatgpt_user_id is the fallback when the account-user id is absent; email comes from the profile claim.
	tok := fakeJWT(t, map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "A", "chatgpt_user_id": "user-9"},
		"https://api.openai.com/profile": map[string]any{"email": "profile@x.io"},
	})
	id, ok = core.CodexIdentityFromJWT("", tok)
	require.True(t, ok, "falls back to the access token")
	assert.Equal(t, "acct:A/user:user-9", id.IdentityKey)
	assert.Equal(t, "profile@x.io", id.Email)

	_, ok = core.CodexIdentityFromJWT("not-a-jwt", "")
	assert.False(t, ok)
	_, ok = core.CodexIdentityFromJWT(fakeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "A"}}), "")
	assert.False(t, ok, "an account with no user part is not an identity")
}

func TestNormalizePlan(t *testing.T) {
	for _, c := range []struct{ provider, raw, want string }{
		{"claude", "default_claude_max_20x", "max_20x"},
		{"claude", "default_claude_max_5x", "max_5x"},
		{"claude", "claude_pro", "pro"},
		{"claude", "default_raven", ""},
		{"codex", "Plus", "plus"},
		{"codex", "business", "business"},
		{"codex", "alien", ""},
		{"openai", "plus", ""},
		{"claude", "", ""},
	} {
		assert.Equal(t, c.want, core.NormalizePlan(c.provider, c.raw), "%s %q", c.provider, c.raw)
	}
}
