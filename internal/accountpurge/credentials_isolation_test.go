// Copyright (c) 2025 Reliant Labs
package accountpurge_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/reliant-labs/reliant/internal/accountpurge/purgetest"
	"github.com/reliant-labs/reliant/internal/db"
)

// seedCredentialKinds gives userID one row of every credential kind the purge reaches, plus the
// org-owned rows an admin's id is attached to for attribution.
func seedCredentialKinds(t *testing.T, rawDB *sql.DB, userID string) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := rawDB.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO api_keys (id, user_id, provider, api_key_sealed) VALUES ('ak-'||$1, $1, 'reliant-automation:d1', '\x00')`, userID)
	exec(`INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, priority, secret_sealed)
		VALUES ('pa-'||$1, $1, 'claude', 'oauth', 'acct:'||$1, 0, '\x00')`, userID)
	exec(`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
		VALUES ('vk-'||$1, 'user', $1, 1, 'kek', '\x00', 'primary')`, userID)
	exec(`INSERT INTO connections (id, owner_kind, user_id, integration_id, auth_kind, name, status)
		VALUES ('conn-'||$1, 'user', $1, 'github', 'oauth2', 'work', 'active')`, userID)
	exec(`INSERT INTO connection_secrets (connection_id, field, vault_key_id, ciphertext) VALUES ('conn-'||$1, 'access_token', 'vk-'||$1, '\x01')`, userID)
	exec(`INSERT INTO connection_events (connection_id, user_id, kind, actor) VALUES ('conn-'||$1, $1, 'created', 'user')`, userID)
	exec(`INSERT INTO oauth_flows (state_hash, user_id, session_id_hash, integration_id, pkce_verifier_sealed, expires_at)
		VALUES (decode(md5($1), 'hex'), $1, '\x00', 'github', '\x00', now() + interval '1 hour')`, userID)
}

var credentialCounts = map[string]string{
	"api_keys":           `SELECT count(*) FROM api_keys WHERE user_id = $1`,
	"provider_accounts":  `SELECT count(*) FROM provider_accounts WHERE user_id = $1`,
	"vault_keys":         `SELECT count(*) FROM vault_keys WHERE tenant_kind = 'user' AND tenant_id = $1`,
	"connections":        `SELECT count(*) FROM connections WHERE user_id = $1 AND owner_kind = 'user'`,
	"connection_secrets": `SELECT count(*) FROM connection_secrets WHERE connection_id = 'conn-'||$1`,
	"connection_events":  `SELECT count(*) FROM connection_events WHERE user_id = $1`,
	"oauth_flows":        `SELECT count(*) FROM oauth_flows WHERE user_id = $1`,
}

func credentialRows(t *testing.T, rawDB *sql.DB, userID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for table, q := range credentialCounts {
		var n int
		if err := rawDB.QueryRow(q, userID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// Purging user A removes every credential row of A and none of B's, and keeps every org-owned
// account and connection, the grants an admin made to others, and the org's vault key.
func TestPurge_CredentialsAreScopedToThePurgedPerson(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	purger := purgetest.New(t, repo)

	const a, b = "user-a", "user-b"
	seedCredentialKinds(t, rawDB, a)
	seedCredentialKinds(t, rawDB, b)
	for table, n := range credentialRows(t, rawDB, a) {
		if n != 1 {
			t.Fatalf("seed: %s has %d rows for A, want 1", table, n)
		}
	}

	mustExec(t, rawDB, `INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state) VALUES ('vk-org', 'org', 'org-1', 1, 'kek', '\x00', 'primary')`)
	mustExec(t, rawDB, `INSERT INTO provider_accounts (id, user_id, owner_kind, org_id, org_access, created_by_user_id, provider, auth_kind, identity_key, priority, secret_sealed)
		VALUES ('acct-org', 'org:org-1', 'org', 'org-1', 'selected', $1, 'anthropic', 'api_key', 'key:org', 0, '\x01')`, a)
	mustExec(t, rawDB, `INSERT INTO provider_account_grants (account_id, org_id, user_id, granted_by_user_id)
		VALUES ('acct-org', 'org-1', $1, $1), ('acct-org', 'org-1', $2, $1)`, a, b)
	mustExec(t, rawDB, `INSERT INTO connections (id, owner_kind, user_id, org_id, integration_id, auth_kind, name, status)
		VALUES ('conn-org', 'org', $1, 'org-1', 'slack', 'api_key', 'org bot', 'active')`, a)
	mustExec(t, rawDB, `INSERT INTO connection_secrets (connection_id, field, vault_key_id, ciphertext) VALUES ('conn-org', 'api_key', 'vk-org', '\x02')`)

	if _, err := purger.Purge(context.Background(), a); err != nil {
		t.Fatalf("Purge(A): %v", err)
	}

	for table, n := range credentialRows(t, rawDB, a) {
		if n != 0 {
			t.Errorf("%s: %d rows of the purged user survived", table, n)
		}
	}
	for table, n := range credentialRows(t, rawDB, b) {
		if n != 1 {
			t.Errorf("%s: user B has %d rows after A was purged, want 1", table, n)
		}
	}
	var org, orgGrantForB, adminGrant, orgConn, orgSecret, orgKey int
	if err := rawDB.QueryRow(`SELECT
		(SELECT count(*) FROM provider_accounts WHERE id = 'acct-org'),
		(SELECT count(*) FROM provider_account_grants WHERE account_id = 'acct-org' AND user_id = $2),
		(SELECT count(*) FROM provider_account_grants WHERE account_id = 'acct-org' AND user_id = $1),
		(SELECT count(*) FROM connections WHERE id = 'conn-org'),
		(SELECT count(*) FROM connection_secrets WHERE connection_id = 'conn-org'),
		(SELECT count(*) FROM vault_keys WHERE id = 'vk-org')`, a, b).
		Scan(&org, &orgGrantForB, &adminGrant, &orgConn, &orgSecret, &orgKey); err != nil {
		t.Fatalf("count org rows: %v", err)
	}
	if org != 1 || orgGrantForB != 1 || orgConn != 1 || orgSecret != 1 || orgKey != 1 {
		t.Errorf("org rows after A's purge: account=%d grantToB=%d connection=%d secret=%d key=%d, want all 1", org, orgGrantForB, orgConn, orgSecret, orgKey)
	}
	if adminGrant != 0 {
		t.Errorf("the grant made TO the purged person survived: %d", adminGrant)
	}
}

// A soft-deleted connection is still the person's data: the purge hard-deletes it.
func TestPurge_RemovesTheSoftDeletedConnectionsOfThePerson(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	purger := purgetest.New(t, repo)

	const a = "user-soft"
	seedCredentialKinds(t, rawDB, a)
	mustExec(t, rawDB, `UPDATE connections SET deleted_at = now(), status = 'revoked' WHERE user_id = $1`, a)

	if _, err := purger.Purge(context.Background(), a); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n := credentialRows(t, rawDB, a)["connections"]; n != 0 {
		t.Errorf("%d soft-deleted connections survived the purge", n)
	}
}

// A statement that fails after the credential packages ran rolls the credential rows back with it.
func TestPurge_CredentialsRollBackWithTheTransaction(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	purger := purgetest.New(t, repo)

	const a = "user-rollback"
	seedCredentialKinds(t, rawDB, a)
	// The vault key is the purge's last statement; a trigger that refuses its delete fails the
	// purge after every credential package has already deleted its rows.
	mustExec(t, rawDB, `CREATE FUNCTION refuse_vault_key_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`)
	mustExec(t, rawDB, `CREATE TRIGGER refuse_vault_key_delete BEFORE DELETE ON vault_keys FOR EACH ROW EXECUTE FUNCTION refuse_vault_key_delete()`)

	if _, err := purger.Purge(context.Background(), a); err == nil {
		t.Fatal("Purge succeeded though its last statement was refused")
	}
	for table, n := range credentialRows(t, rawDB, a) {
		if n != 1 {
			t.Errorf("%s: %d rows after a failed purge, want 1 (the purge must be all-or-nothing)", table, n)
		}
	}
}
