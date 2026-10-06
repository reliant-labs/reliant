// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sealProviderTokensVersion is 20261006124843_seal_provider_oauth_tokens.sql.
const sealProviderTokensVersion int64 = 20261006124843

// Plaintext values distinctive enough that finding one in a column is proof it
// was stored in the clear.
const (
	plainClaudeAccess  = "sk-ant-oat01-claude-access-PLAINTEXT"
	plainClaudeRefresh = "sk-ant-ort01-claude-refresh-PLAINTEXT"
	plainCodexAccess   = "codex-access-PLAINTEXT"
	plainCodexRefresh  = "codex-refresh-PLAINTEXT"
	plainCodexID       = "codex-idtoken-PLAINTEXT"
	plainCopilotAccess = "gho_copilot-access-PLAINTEXT"
	plainCopilotRefres = "ghr_copilot-refresh-PLAINTEXT"
	plainAgyAccess     = "ya29.antigravity-access-PLAINTEXT"
	plainAgyRefresh    = "1//antigravity-refresh-PLAINTEXT"
	plainAgyID         = "antigravity-idtoken-PLAINTEXT"
)

func seedAllProviderTokens(t *testing.T, repo *Repo, userID string) {
	t.Helper()
	ctx := context.Background()
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, userID, ClaudeAuthTokens{
		AccessToken: plainClaudeAccess, RefreshToken: plainClaudeRefresh, ExpiresAt: expiry,
		AccountUUID: "acct-1", AccountEmail: "dev@example.com", OrganizationUUID: "org-1", OrganizationName: "Acme", Scope: "user:inference",
	}))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, CodexAuthTokens{
		AccessToken: plainCodexAccess, RefreshToken: plainCodexRefresh, IDToken: plainCodexID, AccountID: "codex-acct",
	}))
	require.NoError(t, repo.SetCopilotAuthTokens(ctx, userID, CopilotAuthTokens{
		GitHubAccessToken: plainCopilotAccess, GitHubRefreshToken: plainCopilotRefres, Tier: "individual",
	}))
	require.NoError(t, repo.SetAntigravityAuthTokens(ctx, userID, AntigravityAuthTokens{
		AccessToken: plainAgyAccess, RefreshToken: plainAgyRefresh, ExpiresAt: expiry, IDToken: plainAgyID, Scope: "openid",
	}))
}

func TestProviderTokensRoundTrip(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const userID = "round-trip-user"
	seedAllProviderTokens(t, repo, userID)

	claude, err := repo.GetClaudeAuthTokens(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, claude)
	assert.Equal(t, plainClaudeAccess, claude.AccessToken)
	assert.Equal(t, plainClaudeRefresh, claude.RefreshToken)
	assert.Equal(t, "dev@example.com", claude.AccountEmail)
	assert.Equal(t, "Acme", claude.OrganizationName)

	codex, err := repo.GetCodexAuthTokens(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, codex)
	assert.Equal(t, CodexAuthTokens{AccessToken: plainCodexAccess, RefreshToken: plainCodexRefresh, IDToken: plainCodexID, AccountID: "codex-acct"}, *codex)

	copilot, err := repo.GetCopilotAuthTokens(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, copilot)
	assert.Equal(t, plainCopilotAccess, copilot.GitHubAccessToken)
	assert.Equal(t, plainCopilotRefres, copilot.GitHubRefreshToken)
	assert.Equal(t, "individual", copilot.Tier)

	agy, err := repo.GetAntigravityAuthTokens(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, agy)
	assert.Equal(t, plainAgyAccess, agy.AccessToken)
	assert.Equal(t, plainAgyRefresh, agy.RefreshToken)
	assert.Equal(t, plainAgyID, agy.IDToken)
	assert.Equal(t, "openid", agy.Scope)

	// Absent optional tokens round-trip as empty, not as an error.
	require.NoError(t, repo.SetCodexAuthTokens(ctx, "no-refresh-user", CodexAuthTokens{AccessToken: "only-access"}))
	sparse, err := repo.GetCodexAuthTokens(ctx, "no-refresh-user")
	require.NoError(t, err)
	assert.Equal(t, CodexAuthTokens{AccessToken: "only-access"}, *sparse)
}

// TestProviderTokensPersistOnlyCiphertext reads the rows back with raw SQL: no
// column of any provider token table may hold a token in the clear.
func TestProviderTokensPersistOnlyCiphertext(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	const userID = "ciphertext-user"
	seedAllProviderTokens(t, repo, userID)

	sealedColumns := map[string]map[string]string{
		"claude_auth_tokens":      {"access_token_sealed": plainClaudeAccess, "refresh_token_sealed": plainClaudeRefresh},
		"codex_auth_tokens":       {"access_token_sealed": plainCodexAccess, "refresh_token_sealed": plainCodexRefresh, "id_token_sealed": plainCodexID},
		"copilot_auth_tokens":     {"github_access_token_sealed": plainCopilotAccess, "github_refresh_token_sealed": plainCopilotRefres},
		"antigravity_auth_tokens": {"access_token_sealed": plainAgyAccess, "refresh_token_sealed": plainAgyRefresh, "id_token_sealed": plainAgyID},
	}
	allPlaintexts := []string{
		plainClaudeAccess, plainClaudeRefresh, plainCodexAccess, plainCodexRefresh, plainCodexID,
		plainCopilotAccess, plainCopilotRefres, plainAgyAccess, plainAgyRefresh, plainAgyID,
	}

	for table, columns := range sealedColumns {
		for column, plaintext := range columns {
			var stored []byte
			//nolint:gosec // table and column come from the literal map above.
			require.NoError(t, raw.QueryRow(fmt.Sprintf(`SELECT %s FROM %s WHERE user_id = $1`, column, table), userID).Scan(&stored))
			assert.NotEmpty(t, stored, "%s.%s must hold a sealed value", table, column)
			assert.NotEqual(t, []byte(plaintext), stored, "%s.%s is the plaintext token", table, column)
			assert.NotContains(t, string(stored), plaintext, "%s.%s contains the plaintext token", table, column)
		}

		// No column of the row — whatever its name — may contain any token.
		//nolint:gosec // table comes from the literal map above.
		var rowText string
		require.NoError(t, raw.QueryRow(fmt.Sprintf(`SELECT t::text FROM %s t WHERE user_id = $1`, table), userID).Scan(&rowText))
		for _, plaintext := range allPlaintexts {
			assert.NotContains(t, rowText, plaintext, "%s row contains a plaintext token", table)
		}
	}

	// And the plaintext columns no longer exist at all.
	var plaintextColumns []string
	rows, err := raw.Query(`SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_name IN ('claude_auth_tokens','codex_auth_tokens','copilot_auth_tokens','antigravity_auth_tokens')
		  AND column_name IN ('access_token','refresh_token','id_token','github_access_token','github_refresh_token')`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		plaintextColumns = append(plaintextColumns, name)
	}
	require.NoError(t, rows.Err())
	assert.Empty(t, plaintextColumns, "plaintext token columns must be gone")
}

func TestProviderTokenCiphertextDoesNotOpenElsewhere(t *testing.T) {
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAllProviderTokens(t, repo, "owner")
	seedAllProviderTokens(t, repo, "victim")

	// Another user's ciphertext, copied into this user's row.
	_, err := raw.Exec(`UPDATE claude_auth_tokens SET access_token_sealed =
		(SELECT access_token_sealed FROM claude_auth_tokens WHERE user_id = 'owner') WHERE user_id = 'victim'`)
	require.NoError(t, err)
	_, err = repo.GetClaudeAuthTokens(ctx, "victim")
	require.Error(t, err, "a ciphertext transplanted from another user must not open")

	// The same user's refresh token, moved into the access-token column.
	_, err = raw.Exec(`UPDATE codex_auth_tokens SET access_token_sealed = refresh_token_sealed WHERE user_id = 'owner'`)
	require.NoError(t, err)
	_, err = repo.GetCodexAuthTokens(ctx, "owner")
	require.Error(t, err, "a ciphertext moved to another column must not open")

	// The same user's Codex token, moved into their Antigravity row.
	_, err = raw.Exec(`UPDATE antigravity_auth_tokens SET access_token_sealed =
		(SELECT access_token_sealed FROM claude_auth_tokens WHERE user_id = 'owner') WHERE user_id = 'owner'`)
	require.NoError(t, err)
	_, err = repo.GetAntigravityAuthTokens(ctx, "owner")
	require.Error(t, err, "a ciphertext moved to another table must not open")

	// Reconnecting overwrites the unreadable row and the credential works again.
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, "victim", ClaudeAuthTokens{AccessToken: "fresh", RefreshToken: "fresh-rt"}))
	got, err := repo.GetClaudeAuthTokens(ctx, "victim")
	require.NoError(t, err)
	assert.Equal(t, "fresh", got.AccessToken)
}

// A user with no row — every user right after the sealing migration — reads
// as not connected rather than as an error.
func TestProviderTokensMissingRowReadsAsNotConnected(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	claude, err := repo.GetClaudeAuthTokens(ctx, "never-connected")
	require.NoError(t, err)
	assert.Nil(t, claude)
	codex, err := repo.GetCodexAuthTokens(ctx, "never-connected")
	require.NoError(t, err)
	assert.Nil(t, codex)
	copilot, err := repo.GetCopilotAuthTokens(ctx, "never-connected")
	require.NoError(t, err)
	assert.Nil(t, copilot)
	agy, err := repo.GetAntigravityAuthTokens(ctx, "never-connected")
	require.NoError(t, err)
	assert.Nil(t, agy)
}

func TestCompareAndSwapAntigravityAuthTokens(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const userID = "agy-cas-user"
	require.NoError(t, repo.SetAntigravityAuthTokens(ctx, userID, AntigravityAuthTokens{AccessToken: "a1", RefreshToken: "r1"}))

	swapped, err := repo.CompareAndSwapAntigravityAuthTokens(ctx, userID, "r1", AntigravityAuthTokens{AccessToken: "a2", RefreshToken: "r2", IDToken: "i2"})
	require.NoError(t, err)
	assert.True(t, swapped)

	swapped, err = repo.CompareAndSwapAntigravityAuthTokens(ctx, userID, "r1", AntigravityAuthTokens{AccessToken: "a3", RefreshToken: "r3"})
	require.NoError(t, err)
	assert.False(t, swapped, "r1 was consumed")

	stored, err := repo.GetAntigravityAuthTokens(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "a2", stored.AccessToken)
	assert.Equal(t, "i2", stored.IDToken)
}

// Sealing is randomized, so the comparison must be on the opened plaintext: a
// row re-saved with the same refresh token (new ciphertext) still matches.
func TestCompareAndSwapComparesPlaintextNotCiphertext(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const userID = "reseal-user"
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, userID, ClaudeAuthTokens{AccessToken: "a1", RefreshToken: "r1"}))
	require.NoError(t, repo.SetClaudeAuthTokens(ctx, userID, ClaudeAuthTokens{AccessToken: "a1b", RefreshToken: "r1"}))

	swapped, err := repo.CompareAndSwapClaudeAuthTokens(ctx, userID, "r1", ClaudeAuthTokens{AccessToken: "a2", RefreshToken: "r2"})
	require.NoError(t, err)
	assert.True(t, swapped)
}

// Many processes holding the same refresh token race to persist a rotation;
// exactly one may win, and the stored row is the winner's.
func TestCompareAndSwapConcurrentRotationHasOneWinner(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	const userID = "race-user"
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, CodexAuthTokens{AccessToken: "a0", RefreshToken: "r0"}))

	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []int
	)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			swapped, err := repo.CompareAndSwapCodexAuthTokens(ctx, userID, "r0",
				CodexAuthTokens{AccessToken: fmt.Sprintf("a-%d", i), RefreshToken: fmt.Sprintf("r-%d", i)})
			assert.NoError(t, err)
			if swapped {
				mu.Lock()
				winners = append(winners, i)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.Len(t, winners, 1, "exactly one rotation may be persisted")
	stored, err := repo.GetCodexAuthTokens(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("r-%d", winners[0]), stored.RefreshToken)
}

// TestSealProviderTokensMigrationDeletesPlaintextRows rewinds a migrated
// database to the pre-sealing shape, plants plaintext rows the way the old code
// wrote them, and runs the real migration: the rows must be gone and the
// plaintext columns with them.
func TestSealProviderTokensMigrationDeletesPlaintextRows(t *testing.T) {
	_, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	unsealProviderTokenTables(t, raw)
	rewind := []string{
		fmt.Sprintf(`DELETE FROM %s WHERE version_id = %d`, goose.TableName(), sealProviderTokensVersion),
		`INSERT INTO claude_auth_tokens (id, user_id, access_token, refresh_token) VALUES ('c1', 'u1', 'plain-at', 'plain-rt')`,
		`INSERT INTO codex_auth_tokens (id, user_id, access_token, refresh_token, id_token) VALUES ('x1', 'u1', 'plain-at', 'plain-rt', 'plain-id')`,
		`INSERT INTO copilot_auth_tokens (id, user_id, github_access_token) VALUES ('g1', 'u1', 'gho_plain')`,
		`INSERT INTO antigravity_auth_tokens (id, user_id, access_token, refresh_token) VALUES ('a1', 'u1', 'ya29.plain', 'plain-rt')`,
	}
	for _, stmt := range rewind {
		_, err := raw.Exec(stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, initGoose())
	require.NoError(t, goose.UpTo(raw, migrationsDir, sealProviderTokensVersion, goose.WithAllowMissing()))

	for _, table := range []string{"claude_auth_tokens", "codex_auth_tokens", "copilot_auth_tokens", "antigravity_auth_tokens"} {
		assert.Equal(t, 0, countRows(t, raw, table), "%s must be emptied: plaintext rows cannot be sealed in SQL", table)
	}
	var plaintextColumns int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_name IN ('claude_auth_tokens','codex_auth_tokens','copilot_auth_tokens','antigravity_auth_tokens')
		  AND column_name IN ('access_token','refresh_token','id_token','github_access_token','github_refresh_token')`).Scan(&plaintextColumns))
	assert.Zero(t, plaintextColumns)
}

// unsealProviderTokenTables undoes the OBJECTS of the sealing migration on an
// empty, migrated database: the sealed columns go and the plaintext columns
// come back, which is the shape every database had before it. Any test that
// rewinds past sealProviderTokensVersion and replays it needs this, because
// the migration's DROP COLUMN fails against a table that no longer has the
// column.
func unsealProviderTokenTables(t *testing.T, raw *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`ALTER TABLE claude_auth_tokens DROP COLUMN access_token_sealed, DROP COLUMN refresh_token_sealed,
			ADD COLUMN access_token TEXT NOT NULL, ADD COLUMN refresh_token TEXT`,
		`ALTER TABLE codex_auth_tokens DROP COLUMN access_token_sealed, DROP COLUMN refresh_token_sealed, DROP COLUMN id_token_sealed,
			ADD COLUMN access_token TEXT NOT NULL, ADD COLUMN refresh_token TEXT, ADD COLUMN id_token TEXT`,
		`ALTER TABLE copilot_auth_tokens DROP COLUMN github_access_token_sealed, DROP COLUMN github_refresh_token_sealed,
			ADD COLUMN github_access_token TEXT NOT NULL, ADD COLUMN github_refresh_token TEXT`,
		`ALTER TABLE antigravity_auth_tokens DROP COLUMN access_token_sealed, DROP COLUMN refresh_token_sealed, DROP COLUMN id_token_sealed,
			ADD COLUMN access_token TEXT NOT NULL, ADD COLUMN refresh_token TEXT, ADD COLUMN id_token TEXT`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err, stmt)
	}
}

func countRows(t *testing.T, raw *sql.DB, table string) int {
	t.Helper()
	var n int
	//nolint:gosec // table is a literal from the caller.
	require.NoError(t, raw.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&n))
	return n
}
