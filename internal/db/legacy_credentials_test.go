// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	"github.com/reliant-labs/reliant/internal/vault"
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

// legacyFixture is a migrated database whose legacy credential tables are seeded the way a
// database that ran the previous release holds them: every secret sealed under the LEGACY AAD
// (it binds the table name), which is what the one-time copy has to open and re-seal.
type legacyFixture struct {
	t      *testing.T
	repo   *Repo
	raw    *sql.DB
	sealer vault.Service
}

func newLegacyFixture(t *testing.T) *legacyFixture {
	t.Helper()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(k))
	require.NoError(t, err)
	sealer := vault.New(vault.Deps{DB: raw, Keys: vault.NewEnvKeyWrapper(ring)})
	require.NoError(t, repo.EnableCredentialSealing(sealer))
	return &legacyFixture{t: t, repo: repo, raw: raw, sealer: sealer}
}

func (f *legacyFixture) seal(user string, aad []byte, plaintext string) []byte {
	f.t.Helper()
	sealed, err := f.sealer.Seal(context.Background(), vault.UserTenant(user), []byte(plaintext), aad)
	require.NoError(f.t, err)
	return sealed
}

func (f *legacyFixture) oauth(table, user, column, plaintext string) []byte {
	return f.seal(user, postgresstore.LegacyOAuthTokenAAD(table, user, column), plaintext)
}

func (f *legacyFixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.raw.ExecContext(context.Background(), query, args...)
	require.NoError(f.t, err, query)
}

func (f *legacyFixture) claude(user, access, refresh, accountUUID, orgUUID string, exp time.Time) {
	t := postgresstore.LegacySourceClaude
	f.exec(`INSERT INTO claude_auth_tokens (id, user_id, expires_at, account_uuid, account_email, organization_uuid, organization_name, scope,
			access_token_sealed, refresh_token_sealed) VALUES ($1,$2,$3,$4,$5,$6,'Acme','user:inference',$7,$8)`,
		uuid.NewString(), user, exp.UTC(), nullIfEmpty(accountUUID), nullIfEmpty(accountUUID+"@example.com"), nullIfEmpty(orgUUID),
		f.oauth(t, user, "access_token", access), f.oauth(t, user, "refresh_token", refresh))
}

func (f *legacyFixture) codex(user, access, refresh, idToken, accountID string) {
	t := postgresstore.LegacySourceCodex
	f.exec(`INSERT INTO codex_auth_tokens (id, user_id, account_id, access_token_sealed, refresh_token_sealed, id_token_sealed)
		VALUES ($1,$2,$3,$4,$5,$6)`, uuid.NewString(), user, accountID,
		f.oauth(t, user, "access_token", access), f.oauth(t, user, "refresh_token", refresh), f.oauth(t, user, "id_token", idToken))
}

func (f *legacyFixture) copilot(user, access, refresh string) {
	t := postgresstore.LegacySourceCopilot
	f.exec(`INSERT INTO copilot_auth_tokens (id, user_id, tier, github_access_token_sealed, github_refresh_token_sealed)
		VALUES ($1,$2,'individual',$3,$4)`, uuid.NewString(), user,
		f.oauth(t, user, "github_access_token", access), f.oauth(t, user, "github_refresh_token", refresh))
}

func (f *legacyFixture) antigravity(user, access, refresh, idToken string, exp time.Time) {
	t := postgresstore.LegacySourceAntigravity
	f.exec(`INSERT INTO antigravity_auth_tokens (id, user_id, expires_at, scope, access_token_sealed, refresh_token_sealed, id_token_sealed)
		VALUES ($1,$2,$3,'openid',$4,$5,$6)`, uuid.NewString(), user, exp.UTC(),
		f.oauth(t, user, "access_token", access), f.oauth(t, user, "refresh_token", refresh), f.oauth(t, user, "id_token", idToken))
}

func (f *legacyFixture) apiKey(user, provider, key string) {
	f.exec(`INSERT INTO api_keys (id, user_id, provider, api_key, api_key_sealed) VALUES ($1,$2,$3,'',$4)`,
		uuid.NewString(), user, provider, f.seal(user, postgresstore.LegacyAPIKeyAAD(user, provider), key))
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (f *legacyFixture) count(table string) int {
	f.t.Helper()
	var n int
	//nolint:gosec // table is a literal from the caller.
	require.NoError(f.t, f.raw.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n))
	return n
}

// seedEverything is a user connected to every kind of legacy credential, plus the rows that are
// NOT credentials (OAuth markers, an automation token, a cleared key, a "dummy" key).
func (f *legacyFixture) seedEverything(user string) (exp time.Time, idToken string) {
	exp = time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	idToken = codexIDToken(f.t, "cgpt-acct", "cgpt-user", "codex@example.com", "plus")
	f.claude(user, "cl-access", "cl-refresh", "cl-acct", "cl-org", exp)
	f.codex(user, "cx-access", "cx-refresh", idToken, "cgpt-acct")
	f.copilot(user, "gh-access", "gh-refresh")
	f.antigravity(user, "ag-access", "ag-refresh", fakeJWT(f.t, map[string]any{"sub": "google-sub-1", "email": "ag@example.com"}), exp)
	f.apiKey(user, "anthropic", "sk-ant-api03-ZZZZ1234")
	f.apiKey(user, "openai", "sk-openai-AAAA5678")
	f.apiKey(user, "reliant", "rlat_managed")
	f.apiKey(user, "claude", core.ProviderOAuthMarker)
	f.apiKey(user, "codex", core.ProviderOAuthMarker)
	f.apiKey(user, "gemini", "")
	f.apiKey(user, "xai", "dummy")
	f.apiKey(user, core.AutomationProviderPrefix+"daemon-1", "automation-secret")
	return exp, idToken
}

func TestMigrateLegacyCredentialsCopiesEveryTable(t *testing.T) {
	f := newLegacyFixture(t)
	ctx := context.Background()
	const user = "legacy-user"
	exp, idToken := f.seedEverything(user)

	res, ran, err := f.repo.MigrateLegacyCredentials(ctx)
	require.NoError(t, err)
	require.True(t, ran)
	assert.Equal(t, 7, res.Created)
	assert.Zero(t, res.Failed)

	got := accountsOf(t, f.repo, user)
	require.Len(t, got, 7, "markers, the automation token and cleared or dummy keys are not accounts")

	cl := got["claude"]
	assert.Equal(t, core.AuthKindOAuth, cl.AuthKind)
	assert.Equal(t, core.ClaudeIdentityKey("cl-acct", "cl-org"), cl.IdentityKey)
	assert.Equal(t, core.AccountSecrets{Secret: "cl-access", Refresh: "cl-refresh"}, cl.Secrets)
	assert.Equal(t, "cl-acct@example.com", cl.Email)
	assert.Equal(t, "cl-org", cl.OrganizationID)
	assert.Equal(t, "Acme", cl.OrganizationName)
	assert.Equal(t, "user:inference", cl.Scope)
	assert.Equal(t, 0, cl.Priority)
	assert.True(t, cl.ExpiresAt.Equal(exp))

	cx := got["codex"]
	assert.Equal(t, core.CodexIdentityKey("cgpt-acct", "cgpt-user"), cx.IdentityKey)
	assert.Equal(t, "codex@example.com", cx.Email)
	assert.Equal(t, "plus", cx.Plan)
	assert.Equal(t, "cgpt-acct", cx.ExternalAccountID)
	assert.Equal(t, core.AccountSecrets{Secret: "cx-access", Refresh: "cx-refresh", IDToken: idToken}, cx.Secrets)

	gh := got["copilot"]
	assert.Equal(t, core.AuthKindDevice, gh.AuthKind)
	assert.True(t, core.IsPlaceholderIdentityKey(gh.IdentityKey))
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
	assert.Equal(t, "rlat_managed", rl.Secrets.Secret)

	for _, p := range []string{"gemini", "xai"} {
		assert.NotContains(t, got, p)
	}

	// The copy only reads: the legacy tables are exactly as they were, for the later DROP.
	assert.Equal(t, 1, f.count("claude_auth_tokens"))
	assert.Equal(t, 8, f.count("api_keys"))
}

// The copy runs once. Anything the user does afterwards stands: a deleted account is not brought
// back from its legacy row, and a legacy row written later is not imported.
func TestMigrateLegacyCredentialsRunsExactlyOnce(t *testing.T) {
	f := newLegacyFixture(t)
	ctx := context.Background()
	const user = "once-user"
	f.seedEverything(user)

	_, ran, err := f.repo.MigrateLegacyCredentials(ctx)
	require.NoError(t, err)
	require.True(t, ran)

	require.NoError(t, f.repo.DeleteProviderAccount(ctx, user, accountsOf(t, f.repo, user)["claude"].ID))
	f.apiKey("late-user", "openai", "sk-late")

	res, ran, err := f.repo.MigrateLegacyCredentials(ctx)
	require.NoError(t, err)
	assert.False(t, ran, "the marker says the copy is done")
	assert.Zero(t, res.Created)
	assert.NotContains(t, accountsOf(t, f.repo, user), "claude", "a deleted account is not resurrected")
	assert.Empty(t, accountsOf(t, f.repo, "late-user"))
	assert.Len(t, accountsOf(t, f.repo, user), 6)

	var created, skipped, failed int
	require.NoError(t, f.raw.QueryRow(`SELECT created, skipped, failed FROM legacy_credential_copy WHERE id = 1`).Scan(&created, &skipped, &failed))
	assert.Equal(t, 7, created)
}

func TestMigrateLegacyCredentialsConcurrentBootsImportOnce(t *testing.T) {
	f := newLegacyFixture(t)
	ctx := context.Background()
	const user = "race-user"
	f.seedEverything(user)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ranCount := 0
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ran, err := f.repo.MigrateLegacyCredentials(ctx)
			assert.NoError(t, err)
			if ran {
				mu.Lock()
				ranCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, ranCount, "exactly one boot performs the copy")
	assert.Len(t, accountsOf(t, f.repo, user), 7)
	assert.Equal(t, 7, f.count("provider_accounts"), "no duplicates")
}

// A credential that does not open (a rotated vault key, corrupt ciphertext) is skipped and
// counted; it never blocks the others, and the copy still completes.
func TestMigrateLegacyCredentialsSkipsUnopenableRowWithoutBlockingOthers(t *testing.T) {
	f := newLegacyFixture(t)
	ctx := context.Background()
	f.exec(`INSERT INTO api_keys (id, user_id, provider, api_key, api_key_sealed) VALUES ('broken','u1','openai','','\x0102'::bytea)`)
	f.apiKey("u1", "anthropic", "sk-good")

	res, ran, err := f.repo.MigrateLegacyCredentials(ctx)
	require.NoError(t, err)
	require.True(t, ran)
	assert.Equal(t, 1, res.Created)
	assert.Equal(t, 1, res.Failed)

	got := accountsOf(t, f.repo, "u1")
	assert.Contains(t, got, "anthropic")
	assert.NotContains(t, got, "openai")

	var failed int
	require.NoError(t, f.raw.QueryRow(`SELECT failed FROM legacy_credential_copy`).Scan(&failed))
	assert.Equal(t, 1, failed, "the marker records what was left behind")
}

// A Claude row with no account uuid cannot be identified yet: it is copied under a placeholder
// the first refresh rewrites, and an API-key row for an OAuth provider (a marker) never is.
func TestMigrateLegacyCredentialsClaudeWithoutAccountUUIDGetsAPlaceholder(t *testing.T) {
	f := newLegacyFixture(t)
	f.claude("p-user", "a", "r", "", "", time.Now().Add(time.Hour))

	_, _, err := f.repo.MigrateLegacyCredentials(context.Background())
	require.NoError(t, err)
	acct := accountsOf(t, f.repo, "p-user")["claude"]
	assert.True(t, core.IsPlaceholderIdentityKey(acct.IdentityKey), acct.IdentityKey)
	assert.Equal(t, "r", acct.Secrets.Refresh)
}

// The upgrade a deployed database goes through, end to end: the previous release's rows sealed
// under the legacy AADs, then BootVault (the same entry point both servers call) with the real
// vault key. Every credential must exist as an account AND open, and a second boot must change
// nothing. No user loses a working connection.
func TestUpgradeBootCopiesLegacyCredentialsAndTheyOpen(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	envKey := "v1:" + base64.StdEncoding.EncodeToString(k)
	t.Setenv(vault.EnvKey, envKey)
	ring, err := crypto.ParseKeyring(envKey)
	require.NoError(t, err)
	// The previous release's vault: same key, same tenant DEKs (they live in vault_keys).
	prev := &legacyFixture{t: t, repo: repo, raw: raw, sealer: vault.New(vault.Deps{DB: raw, Keys: vault.NewEnvKeyWrapper(ring)})}

	const user = "upgrade-user"
	prev.seedEverything(user)
	prev.claude("other-user", "o-access", "o-refresh", "o-acct", "o-org", time.Now().Add(time.Hour))

	_, err = BootVault(ctx, repo, true, t.TempDir())
	require.NoError(t, err)

	got := accountsOf(t, repo, user)
	require.Len(t, got, 7)
	assert.Equal(t, "cl-access", got["claude"].Secrets.Secret)
	assert.Equal(t, "cx-refresh", got["codex"].Secrets.Refresh)
	assert.Equal(t, "gh-access", got["copilot"].Secrets.Secret)
	assert.Equal(t, "ag-refresh", got["antigravity"].Secrets.Refresh)
	assert.Equal(t, "sk-openai-AAAA5678", got["openai"].Secrets.Secret)
	assert.Equal(t, "rlat_managed", got["reliant"].Secrets.Secret)
	assert.Equal(t, "o-access", accountsOf(t, repo, "other-user")["claude"].Secrets.Secret)

	// The automation token is the one thing api_keys still serves.
	tok, err := repo.GetAutomationToken(ctx, user, "daemon-1")
	require.NoError(t, err)
	assert.Equal(t, "automation-secret", tok)

	before := prev.count("provider_accounts")
	_, err = BootVault(ctx, repo, true, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, before, prev.count("provider_accounts"), "a second boot copies nothing")
}

// An account that connected through the new code BEFORE the copy ran (a rolling deploy: the new
// release served traffic while the old rows were still unread) is never overwritten by, or
// duplicated beside, the legacy row for the same upstream account.
func TestMigrateLegacyCredentialsNeverOverwritesAnAccountTheNewCodeWrote(t *testing.T) {
	f := newLegacyFixture(t)
	ctx := context.Background()
	const user = "rolling-user"
	f.claude(user, "legacy-access", "legacy-refresh", "acct", "org", time.Now().Add(time.Hour))
	_, _, err := f.repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth, IdentityKey: core.ClaudeIdentityKey("acct", "org"),
		Secrets: core.AccountSecrets{Secret: "native-access", Refresh: "native-refresh"},
	})
	require.NoError(t, err)

	res, _, err := f.repo.MigrateLegacyCredentials(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Created)
	assert.Equal(t, 1, res.Unchanged)

	got := accountsOf(t, f.repo, user)
	assert.Equal(t, "native-refresh", got["claude"].Secrets.Refresh, "the newer, native lineage wins")
	assert.Equal(t, 1, f.count("provider_accounts"))
}

func TestCodexIdentityFromJWT(t *testing.T) {
	id, ok := core.CodexIdentityFromJWT(codexIDToken(t, "A", "U", "me@x.io", "pro"), "")
	require.True(t, ok)
	assert.Equal(t, core.CodexIdentityKey("A", "U"), id.IdentityKey)
	assert.Equal(t, "me@x.io", id.Email)
	assert.Equal(t, "pro", id.Plan)

	tok := fakeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "B", "chatgpt_user_id": "V"}})
	id, ok = core.CodexIdentityFromJWT("", tok)
	require.True(t, ok, "falls back to the access token")
	assert.Equal(t, core.CodexIdentityKey("B", "V"), id.IdentityKey)

	_, ok = core.CodexIdentityFromJWT("not-a-jwt", "")
	assert.False(t, ok)
	_, ok = core.CodexIdentityFromJWT(fakeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "A"}}), "")
	assert.False(t, ok, "an account without a user part is not an identity")
}

func TestNormalizePlan(t *testing.T) {
	for _, c := range []struct{ provider, raw, want string }{
		{"claude", "default_claude_max_20x", "max_20x"},
		{"claude", "default_claude_max_5x", "max_5x"},
		{"claude", "claude_pro", "pro"},
		{"claude", "", ""},
		{"codex", "plus", "plus"},
		{"codex", "prolite", "pro_lite"},
		{"codex", "promax", "pro_max"},
		{"codex", "self_serve_business_usage_based", "business"},
		{"codex", "FutureTier", "futuretier"},
		{"codex", "", ""},
	} {
		assert.Equal(t, c.want, core.NormalizePlan(c.provider, c.raw), "%s %q", c.provider, c.raw)
	}
}
