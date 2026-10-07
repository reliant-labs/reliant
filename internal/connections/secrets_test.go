// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/stretchr/testify/require"
)

// A SELECT * on connections cannot return ciphertext: it lives in its own table.
func TestConnectionsTableHasNoCiphertextColumn(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "work")

	rows, err := e.raw.Query(`SELECT * FROM connections WHERE id = $1`, conn.ID)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	for _, c := range cols {
		require.NotContains(t, strings.ToLower(c), "cipher")
		require.NotContains(t, strings.ToLower(c), "token_enc")
	}
	require.True(t, rows.Next())
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	require.NoError(t, rows.Scan(ptrs...))
	for i, v := range vals {
		if b, ok := v.([]byte); ok {
			require.NotContains(t, string(b), "ghu_access", "column %s leaked a token", cols[i])
			require.NotContains(t, string(b), "ghr_refresh", "column %s leaked a token", cols[i])
		}
	}
}

func TestCiphertextOpensOnlyWithRightAAD(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.connect("alice", "a")
	// second connect with the same GitHub account reconnects in place; use a
	// different account for a distinct connection.
	e.gh.accountID, e.gh.login = 999, "other"
	b := e.connect("alice", "b")
	require.NotEqual(t, a.ID, b.ID)

	secrets, err := e.store.GetSecrets(ctx, "alice", a.ID)
	require.NoError(t, err)
	access := secrets[core.SecretFieldAccessToken]
	tenant := vault.UserTenant("alice")

	pt, err := e.vault.Open(ctx, tenant, access.Ciphertext, connections.SecretAAD(a.ID, core.SecretFieldAccessToken))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(pt), "ghu_access_"))

	_, err = e.vault.Open(ctx, tenant, access.Ciphertext, connections.SecretAAD(b.ID, core.SecretFieldAccessToken))
	require.Error(t, err, "ciphertext moved to another connection's AAD must not open")
	_, err = e.vault.Open(ctx, tenant, access.Ciphertext, connections.SecretAAD(a.ID, core.SecretFieldRefreshToken))
	require.Error(t, err, "ciphertext moved to another field's AAD must not open")
	_, err = e.vault.Open(ctx, vault.UserTenant("bob"), access.Ciphertext, connections.SecretAAD(a.ID, core.SecretFieldAccessToken))
	require.Error(t, err, "another tenant must not open it")
}

// A row transplant (attacker with DB write copies one connection's ciphertext
// onto another's) is caught by the AAD at use time.
func TestTransplantedCiphertextFailsAtUse(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.connect("alice", "a")
	e.gh.accountID, e.gh.login = 999, "other"
	b := e.connect("alice", "b")

	_, err := e.raw.Exec(`UPDATE connection_secrets SET ciphertext = (SELECT ciphertext FROM connection_secrets WHERE connection_id=$1 AND field='access_token')
		WHERE connection_id=$2 AND field='access_token'`, a.ID, b.ID)
	require.NoError(t, err)

	_, err = e.tokens.Token(ctx, "alice", b.ID)
	require.Error(t, err)
	_, err = e.tokens.Token(ctx, "alice", a.ID)
	require.NoError(t, err)
}

func TestPKCEVerifierIsSealedAtRest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	authURL, err := e.svc.StartOAuth(ctx, connections.StartParams{UserID: "alice", IntegrationID: "github", ClientOrigin: testAppOrigin})
	require.NoError(t, err)
	require.NotEmpty(t, mustQuery(t, authURL, "code_challenge"))
	require.Equal(t, "S256", mustQuery(t, authURL, "code_challenge_method"))

	var sealed []byte
	var plainCol sql.NullString
	require.NoError(t, e.raw.QueryRow(`SELECT pkce_verifier_sealed, redirect_after FROM oauth_flows`).Scan(&sealed, &plainCol))
	require.Greater(t, len(sealed), 43)
	state := mustQuery(t, authURL, "state")
	// The raw state is never stored, only its hash.
	require.Zero(t, e.count(`SELECT count(*) FROM oauth_flows WHERE encode(state_hash,'escape') = $1`, state))
	// Opening it with the wrong AAD fails.
	_, err = e.vault.Open(ctx, vault.UserTenant("alice"), sealed, []byte("oauth_flows\x00wrong"))
	require.Error(t, err)
}

func TestNoSecretInEventsOrMetadata(t *testing.T) {
	e := newEnv(t)
	conn := e.connect("alice", "w")
	evs, err := e.store.ListConnectionEvents(context.Background(), "alice", conn.ID, 50, 0)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	for _, ev := range evs {
		require.NotContains(t, ev.Actor, "ghu_")
	}
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connections WHERE status_reason LIKE '%ghu_%' OR name LIKE '%ghu_%'`))
}
