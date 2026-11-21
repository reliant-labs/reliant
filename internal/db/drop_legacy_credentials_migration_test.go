// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// 20261121034320_drop_legacy_credentials and 20261121034321_drop_plaintext_api_key drop data
// that only an older release's boot moved into place (the legacy credential copy, the api_keys
// sealing backfill). Each refuses to run on a database that never booted that release and still
// holds the data. These tests build the schema just before them, plant rows, and boot the
// current binary's Migrate over it.

const (
	beforeLegacyCredentialDrop = 20261120223939 // the newest migration before drop_legacy_credentials
	beforePlaintextAPIKeyDrop  = 20261121034320 // drop_legacy_credentials
)

// schemaAt returns a database migrated through version, and its DSN.
func schemaAt(t *testing.T, version uint) (string, *sql.DB) {
	t.Helper()
	dsn := newEmptyTestDB(t)
	raw := openTestDSN(t, dsn)
	_, err := migratekit.Apply(context.Background(), raw, embeddedUpTo(t, version, nil), forgedb.SourceDir)
	require.NoError(t, err)
	require.Equal(t, version, migrationState(t, raw).Version)
	return dsn, raw
}

func tableExists(t *testing.T, raw *sql.DB, table string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, raw.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&ok))
	return ok
}

func columnExists(t *testing.T, raw *sql.DB, table, column string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, raw.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`, table, column).Scan(&ok))
	return ok
}

func TestDropLegacyCredentialsRefusesWhenTheCopyNeverRan(t *testing.T) {
	testkit.Slow(t, "builds a database through every migration, then migrates it")
	t.Parallel()
	for name, plant := range map[string]string{
		"claude_auth_tokens": `INSERT INTO claude_auth_tokens (id, user_id, access_token_sealed, refresh_token_sealed)
			VALUES ('c1', 'u1', '\x00'::bytea, '\x00'::bytea)`,
		"an LLM row of api_keys": `INSERT INTO api_keys (id, user_id, provider, api_key, api_key_sealed)
			VALUES ('k1', 'u1', 'openai', '', '\x00'::bytea)`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dsn, raw := schemaAt(t, beforeLegacyCredentialDrop)
			_, err := raw.Exec(plant)
			require.NoError(t, err)

			_, err = migrate(dsn)
			require.Error(t, err)
			require.Contains(t, err.Error(), "legacy_credential_copy has no completion marker")
			require.True(t, tableExists(t, raw, "claude_auth_tokens"), "the refused migration dropped nothing")
			require.True(t, columnExists(t, raw, "provider_accounts", "legacy_source"))
		})
	}
}

func TestDropLegacyCredentialsAfterTheCopy(t *testing.T) {
	testkit.Slow(t, "builds a database through every migration, then migrates it")
	t.Parallel()
	dsn, raw := schemaAt(t, beforeLegacyCredentialDrop)
	for _, stmt := range []string{
		`INSERT INTO claude_auth_tokens (id, user_id, access_token_sealed, refresh_token_sealed)
			VALUES ('c1', 'u1', '\x00'::bytea, '\x00'::bytea)`,
		`INSERT INTO api_keys (id, user_id, provider, api_key, api_key_sealed) VALUES
			('k-llm', 'u1', 'openai', '', '\x00'::bytea),
			('k-marker', 'u1', 'claude', '', '\x00'::bytea),
			('k-auto', 'u1', 'reliant-automation:d1', '', '\x01'::bytea)`,
		`INSERT INTO legacy_credential_copy (id, completed_at, created, failed) VALUES (1, '2026-10-20T00:00:00Z', 2, 0)`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	_, err := migrate(dsn)
	require.NoError(t, err)
	require.Equal(t, latestEmbedded(t), migrationState(t, raw).Version)
	for _, table := range []string{"claude_auth_tokens", "codex_auth_tokens", "copilot_auth_tokens", "antigravity_auth_tokens", "legacy_credential_copy"} {
		require.False(t, tableExists(t, raw, table), "%s is dropped", table)
	}
	require.False(t, columnExists(t, raw, "provider_accounts", "legacy_source"))

	var ids []string
	rows, err := raw.Query(`SELECT id FROM api_keys ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"k-auto"}, ids, "only the automation token survives")
}

// The copy writes its marker even when rows failed to copy (counted in failed); those rows
// exist only in the legacy tables, so the drop refuses until an operator reconciles them.
func TestDropLegacyCredentialsRefusesWhenTheCopyFailedRows(t *testing.T) {
	testkit.Slow(t, "builds a database through every migration, then migrates it")
	t.Parallel()
	dsn, raw := schemaAt(t, beforeLegacyCredentialDrop)
	for _, stmt := range []string{
		`INSERT INTO codex_auth_tokens (id, user_id, access_token_sealed, refresh_token_sealed, id_token_sealed)
			VALUES ('x1', 'u1', '\x00'::bytea, '\x00'::bytea, '\x00'::bytea)`,
		`INSERT INTO legacy_credential_copy (id, completed_at, created, failed) VALUES (1, '2026-10-20T00:00:00Z', 3, 2)`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}

	_, err := migrate(dsn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recorded 2 row(s) it failed to copy")
	require.True(t, tableExists(t, raw, "codex_auth_tokens"), "the refused migration dropped nothing")
	require.True(t, columnExists(t, raw, "provider_accounts", "legacy_source"))
}

func TestDropLegacyCredentialsOnAnEmptyDatabaseNeedsNoMarker(t *testing.T) {
	testkit.Slow(t, "builds a database through every migration, then migrates it")
	t.Parallel()
	dsn, raw := schemaAt(t, beforeLegacyCredentialDrop)
	_, err := migrate(dsn)
	require.NoError(t, err, "a database with no legacy rows has nothing to copy")
	require.Equal(t, latestEmbedded(t), migrationState(t, raw).Version)
}

// plantUnsealedAPIKey writes a row the way a database from before the sealing contract holds
// it: the check is NOT VALID, so it is dropped to plant the row and re-added as it was.
func plantUnsealedAPIKey(t *testing.T, raw *sql.DB, id, plaintext string) {
	t.Helper()
	for _, stmt := range []string{
		`ALTER TABLE api_keys DROP CONSTRAINT api_keys_sealed_not_null`,
		`INSERT INTO api_keys (id, user_id, provider, api_key) VALUES ('` + id + `', 'u1', 'reliant-automation:` + id + `', '` + plaintext + `')`,
		`ALTER TABLE api_keys ADD CONSTRAINT api_keys_sealed_not_null CHECK (api_key_sealed IS NOT NULL) NOT VALID`,
	} {
		_, err := raw.Exec(stmt)
		require.NoError(t, err)
	}
}

func TestDropPlaintextAPIKeyRefusesAnUnsealedKey(t *testing.T) {
	testkit.Slow(t, "builds a database through every migration, then migrates it")
	t.Parallel()
	dsn, raw := schemaAt(t, beforePlaintextAPIKeyDrop)
	plantUnsealedAPIKey(t, raw, "plain", "rlat_never_sealed")

	_, err := migrate(dsn)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plaintext api_key and no api_key_sealed")
	require.True(t, columnExists(t, raw, "api_keys", "api_key"), "the refused migration dropped nothing")
}

func TestDropPlaintextAPIKeyAfterSealing(t *testing.T) {
	testkit.Slow(t, "builds a database through every migration, then migrates it")
	t.Parallel()
	dsn, raw := schemaAt(t, beforePlaintextAPIKeyDrop)
	plantUnsealedAPIKey(t, raw, "empty", "")
	_, err := raw.Exec(`INSERT INTO api_keys (id, user_id, provider, api_key, api_key_sealed)
		VALUES ('sealed', 'u1', 'reliant-automation:sealed', '', '\x01'::bytea)`)
	require.NoError(t, err)

	_, err = migrate(dsn)
	require.NoError(t, err)
	require.False(t, columnExists(t, raw, "api_keys", "api_key"))
	var validated bool
	require.NoError(t, raw.QueryRow(`SELECT convalidated FROM pg_constraint WHERE conname = 'api_keys_sealed_not_null'`).Scan(&validated))
	require.True(t, validated, "api_keys_sealed_not_null is validated")
	var ids []string
	rows, err := raw.Query(`SELECT id FROM api_keys ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"sealed"}, ids, "the row holding no credential is deleted; the sealed one is kept")
}
