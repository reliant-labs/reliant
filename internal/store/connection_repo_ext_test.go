// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The connection vault ports (P5b lane L5): connections, connection_secrets,
// connection_events and oauth_flows. Each query is written through one layer
// and read back through the other — env.Repo.Connections(), the store the
// ports replaced and now the adapter over them.

// vaultKey gives the test's sealed fields the vault key they reference
// (connection_secrets.vault_key_id is a foreign key). The ciphertext itself
// is opaque here: internal/connections seals and unseals it.
func vaultKey(t *testing.T, env *storetest.Env) {
	t.Helper()
	_, err := env.SQL.Exec(`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
		VALUES ('vk-1', 'user', 'alice', 1, 'kek', '\x00', 'primary')`)
	require.NoError(t, err)
}

func newConnection(userID, integration, name string) *core.Connection {
	return &core.Connection{
		ID: uuid.NewString(), OwnerKind: core.ConnectionOwnerUser, UserID: userID, IntegrationID: integration,
		AuthKind: core.ConnectionAuthOAuth2, Name: name, Scopes: []string{"repo"},
		Status: core.ConnectionStatusActive, Params: map[string]string{"site": "acme"},
	}
}

func secret(field, ciphertext string) core.ConnectionSecret {
	return core.ConnectionSecret{Field: field, VaultKeyID: "vk-1", Ciphertext: []byte(ciphertext)}
}

func event(kind, userID string) core.ConnectionEvent {
	return core.ConnectionEvent{UserID: userID, Kind: kind, Actor: "user"}
}

func TestConnectionCreateAndReads(t *testing.T) {
	env := storetest.New(t)
	vaultKey(t, env)
	ctx := context.Background()
	twin := env.Repo.Connections()

	first := newConnection("alice", "github", "work")
	require.NoError(t, store.InsertUserConnection(ctx, env.ORM, first,
		[]core.ConnectionSecret{secret(core.SecretFieldAccessToken, "sealed-a"), secret(core.SecretFieldRefreshToken, "sealed-r")},
		event(core.ConnectionEventCreated, "alice")))
	assert.True(t, first.IsDefault, "the first connection of an integration is its default")
	second := newConnection("alice", "github", "personal")
	ext := "gh-42"
	second.ExternalAccountID = &ext
	require.NoError(t, twin.CreateConnection(ctx, second, nil, event(core.ConnectionEventCreated, "alice")))
	assert.False(t, second.IsDefault)
	require.ErrorIs(t, store.InsertUserConnection(ctx, env.ORM, newConnection("alice", "github", "work"), nil,
		event(core.ConnectionEventCreated, "alice")), core.ErrConnectionNameTaken)
	slack := newConnection("alice", "slack", "work")
	slack.Scopes, slack.Params = nil, nil
	require.NoError(t, store.InsertUserConnection(ctx, env.ORM, slack, nil, event(core.ConnectionEventCreated, "alice")))

	got, err := twin.GetConnection(ctx, "alice", first.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"repo"}, got.Scopes)
	assert.Equal(t, map[string]string{"site": "acme"}, got.Params)
	mine, err := store.GetUserConnection(ctx, env.ORM, "alice", first.ID)
	require.NoError(t, err)
	assert.Equal(t, got, mine)
	_, err = store.GetUserConnection(ctx, env.ORM, "bob", first.ID)
	assert.ErrorIs(t, err, core.ErrConnectionNotFound, "someone else's connection is not found")
	empty, err := store.GetUserConnection(ctx, env.ORM, "alice", slack.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{}, empty.Scopes, "nil scopes are stored as {}")
	assert.Equal(t, map[string]string{}, empty.Params, "nil params are the empty object")

	all, err := store.ListUserConnections(ctx, env.ORM, "alice", "")
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, []string{first.ID, second.ID, slack.ID}, connIDs(all), "by integration, then age")
	twinAll, err := twin.ListConnections(ctx, "alice", "")
	require.NoError(t, err)
	assert.Equal(t, twinAll, all)
	gh, err := store.ListUserConnections(ctx, env.ORM, "alice", "github")
	require.NoError(t, err)
	assert.Len(t, gh, 2)
	none, err := store.ListUserConnections(ctx, env.ORM, "bob", "")
	require.NoError(t, err)
	assert.NotNil(t, none)
	assert.Empty(t, none)

	def, err := store.DefaultUserConnection(ctx, env.ORM, "alice", "github")
	require.NoError(t, err)
	assert.Equal(t, first.ID, def.ID)
	found, err := store.FindConnectionByExternalAccount(ctx, env.ORM, "alice", "github", "gh-42")
	require.NoError(t, err)
	assert.Equal(t, second.ID, found.ID)
	_, err = twin.FindByExternalAccount(ctx, "alice", "github", "gh-0")
	assert.ErrorIs(t, err, core.ErrConnectionNotFound)

	secrets, err := twin.GetSecrets(ctx, "alice", first.ID)
	require.NoError(t, err)
	require.Len(t, secrets, 2)
	assert.Equal(t, []byte("sealed-a"), secrets[core.SecretFieldAccessToken].Ciphertext)
	assert.EqualValues(t, 1, secrets[core.SecretFieldAccessToken].Generation)
	mineSecrets, err := store.GetConnectionSecrets(ctx, env.ORM, "alice", first.ID)
	require.NoError(t, err)
	assert.Equal(t, secrets, mineSecrets)
	_, err = store.GetConnectionSecrets(ctx, env.ORM, "bob", first.ID)
	assert.ErrorIs(t, err, core.ErrConnectionNotFound, "ownership is checked by the statement that reads ciphertext")
	_, err = store.GetConnectionSecrets(ctx, env.ORM, "alice", slack.ID)
	assert.ErrorIs(t, err, core.ErrConnectionNotFound, "a connection with no secrets")
}

func TestConnectionWrites(t *testing.T) {
	env := storetest.New(t)
	vaultKey(t, env)
	ctx := context.Background()
	twin := env.Repo.Connections()
	a, b := newConnection("alice", "github", "a"), newConnection("alice", "github", "b")
	require.NoError(t, store.InsertUserConnection(ctx, env.ORM, a, []core.ConnectionSecret{secret(core.SecretFieldAccessToken, "t1")}, event(core.ConnectionEventCreated, "alice")))
	require.NoError(t, store.InsertUserConnection(ctx, env.ORM, b, nil, event(core.ConnectionEventCreated, "alice")))

	require.ErrorIs(t, store.RenameConnection(ctx, env.ORM, "alice", b.ID, "a", event(core.ConnectionEventRenamed, "alice")), core.ErrConnectionNameTaken)
	require.NoError(t, store.RenameConnection(ctx, env.ORM, "alice", b.ID, "b2", event(core.ConnectionEventRenamed, "alice")))
	require.ErrorIs(t, store.RenameConnection(ctx, env.ORM, "bob", b.ID, "x", event(core.ConnectionEventRenamed, "bob")), core.ErrConnectionNotFound)

	require.NoError(t, store.SetDefaultConnection(ctx, env.ORM, "alice", b.ID, event(core.ConnectionEventDefaultChanged, "alice")))
	def, err := twin.DefaultConnection(ctx, "alice", "github")
	require.NoError(t, err)
	assert.Equal(t, b.ID, def.ID)
	assert.Equal(t, "b2", def.Name)
	require.ErrorIs(t, store.SetDefaultConnection(ctx, env.ORM, "bob", a.ID, event(core.ConnectionEventDefaultChanged, "bob")), core.ErrConnectionNotFound)

	exp := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	require.NoError(t, twin.RecordTestResult(ctx, "alice", a.ID, "", ""))
	re, err := store.ReauthorizeConnection(ctx, env.ORM, "alice", a.ID, core.ConnectionUpdate{
		AccountLabel: "octocat", ExternalAccountID: "gh-1", SenderID: "u-1", OAuthClient: "client-1", AccessExpiresAt: &exp,
	}, []core.ConnectionSecret{secret(core.SecretFieldAccessToken, "t2")}, event(core.ConnectionEventReconnected, "alice"))
	require.NoError(t, err)
	assert.Equal(t, core.ConnectionStatusActive, re.Status)
	require.NotNil(t, re.AccountLabel)
	assert.Equal(t, "octocat", *re.AccountLabel)
	assert.Equal(t, []string{}, re.Scopes, "the new authorization's scopes replace the old")
	require.NotNil(t, re.AccessExpiresAt)
	assert.True(t, re.AccessExpiresAt.Equal(exp))
	secrets, err := twin.GetSecrets(ctx, "alice", a.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, secrets[core.SecretFieldAccessToken].Generation, "a rewrite bumps the generation")
	assert.Equal(t, []byte("t2"), secrets[core.SecretFieldAccessToken].Ciphertext)
	_, err = store.ReauthorizeConnection(ctx, env.ORM, "bob", a.ID, core.ConnectionUpdate{}, nil, event(core.ConnectionEventReconnected, "bob"))
	assert.ErrorIs(t, err, core.ErrConnectionNotFound)

	// An empty test result keeps what is stored; a value replaces it.
	require.NoError(t, store.RecordConnectionTestResult(ctx, env.ORM, "alice", a.ID, "", "u-2"))
	got, err := twin.GetConnection(ctx, "alice", a.ID)
	require.NoError(t, err)
	assert.Equal(t, "octocat", *got.AccountLabel)
	assert.Equal(t, "u-2", *got.SenderID)
	require.ErrorIs(t, store.RecordConnectionTestResult(ctx, env.ORM, "bob", a.ID, "x", ""), core.ErrConnectionNotFound)

	require.NoError(t, store.TouchConnectionUsed(ctx, env.ORM, "alice", a.ID))
	touched, err := store.GetUserConnection(ctx, env.ORM, "alice", a.ID)
	require.NoError(t, err)
	require.NotNil(t, touched.LastUsedAt)
	assert.True(t, touched.UpdatedAt.Equal(got.UpdatedAt), "a touch is telemetry: updated_at is left alone")

	// Delete keeps the row, revoked, for the audit trail; the secrets go.
	require.NoError(t, store.SoftDeleteConnection(ctx, env.ORM, "alice", a.ID, event(core.ConnectionEventDeleted, "alice")))
	_, err = twin.GetConnection(ctx, "alice", a.ID)
	assert.ErrorIs(t, err, core.ErrConnectionNotFound)
	require.ErrorIs(t, store.SoftDeleteConnection(ctx, env.ORM, "alice", a.ID, event(core.ConnectionEventDeleted, "alice")), core.ErrConnectionNotFound)
	require.ErrorIs(t, store.RenameConnection(ctx, env.ORM, "alice", a.ID, "zzz", event(core.ConnectionEventRenamed, "alice")), core.ErrConnectionNotFound,
		"a deleted connection takes no writes")
	rows, err := store.ListAllConnection(ctx, env.ORM)
	require.NoError(t, err)
	var kept *store.Connection
	for _, r := range rows {
		if r.ID == a.ID {
			kept = r
		}
	}
	require.NotNil(t, kept)
	assert.NotNil(t, kept.DeletedAt)
	assert.Equal(t, core.ConnectionStatusRevoked, kept.Status)
	assert.Equal(t, "deleted", *kept.StatusReason)
	assert.False(t, kept.IsDefault)
	left, err := store.ListConnectionSecret(ctx, env.ORM)
	require.NoError(t, err)
	assert.Empty(t, left)

	evs, err := twin.ListConnectionEvents(ctx, "alice", a.ID, 0, 0)
	require.NoError(t, err)
	kinds := make([]string, 0, len(evs))
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	assert.Equal(t, []string{core.ConnectionEventDeleted, core.ConnectionEventReconnected, core.ConnectionEventCreated}, kinds, "newest first")
}

func TestConnectionSecretsLock(t *testing.T) {
	env := storetest.New(t)
	vaultKey(t, env)
	ctx := context.Background()
	twin := env.Repo.Connections()
	c := newConnection("alice", "github", "work")
	require.NoError(t, twin.CreateConnection(ctx, c, []core.ConnectionSecret{secret(core.SecretFieldAccessToken, "t1")}, event(core.ConnectionEventCreated, "alice")))

	exp := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	// The store half: fn gets the locked row and the ctx carrying the lock's
	// transaction, and its writes join it.
	err := store.WithConnectionSecretsLock(ctx, env.ORM, "alice", c.ID,
		func(txCtx context.Context, conn *core.Connection, secrets map[string]core.ConnectionSecret) error {
			assert.Equal(t, c.ID, conn.ID)
			assert.EqualValues(t, 1, secrets[core.SecretFieldAccessToken].Generation)
			return store.ReplaceConnectionSecrets(txCtx, env.ORM, c.ID, []core.ConnectionSecret{secret(core.SecretFieldAccessToken, "t2")}, &exp)
		})
	require.NoError(t, err)

	// The adapter half (*db.SecretsTx): the callback runs its statements
	// with its own ctx, which does not carry the transaction, and they must
	// still land in it (or they would block on the row the lock holds).
	err = twin.WithSecretsLock(ctx, "alice", c.ID, func(tx *db.SecretsTx) error {
		assert.Equal(t, c.ID, tx.Connection().ID)
		access := tx.Secrets()[core.SecretFieldAccessToken]
		require.ErrorIs(t, tx.Replace(ctx, access.Generation+1, nil, nil), core.ErrGenerationConflict)
		if err := tx.Replace(ctx, access.Generation, []core.ConnectionSecret{secret(core.SecretFieldAccessToken, "t3")}, &exp); err != nil {
			return err
		}
		if err := tx.MarkStatus(ctx, core.ConnectionStatusNeedsReauth, "invalid_grant"); err != nil {
			return err
		}
		return tx.AppendEvent(ctx, event(core.ConnectionEventRefreshed, "worker"))
	})
	require.NoError(t, err)
	got, err := twin.GetConnection(ctx, "alice", c.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ConnectionStatusNeedsReauth, got.Status)
	assert.Equal(t, "invalid_grant", *got.StatusReason)
	assert.True(t, got.AccessExpiresAt.Equal(exp))
	secrets, err := store.GetConnectionSecrets(ctx, env.ORM, "alice", c.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 3, secrets[core.SecretFieldAccessToken].Generation)
	assert.Equal(t, []byte("t3"), secrets[core.SecretFieldAccessToken].Ciphertext)

	// A failed callback rolls back everything it wrote.
	require.Error(t, twin.WithSecretsLock(ctx, "alice", c.ID, func(tx *db.SecretsTx) error {
		require.NoError(t, tx.MarkStatus(ctx, core.ConnectionStatusActive, ""))
		return assert.AnError
	}))
	got, err = store.GetUserConnection(ctx, env.ORM, "alice", c.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ConnectionStatusNeedsReauth, got.Status)

	require.ErrorIs(t, store.WithConnectionSecretsLock(ctx, env.ORM, "bob", c.ID,
		func(context.Context, *core.Connection, map[string]core.ConnectionSecret) error { return nil }), core.ErrConnectionNotFound)
}

func TestConnectionEventsPage(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	twin := env.Repo.Connections()
	c := newConnection("alice", "github", "work")
	require.NoError(t, store.InsertUserConnection(ctx, env.ORM, c, nil, event(core.ConnectionEventCreated, "alice")))
	for i := 0; i < 4; i++ {
		ev := event(core.ConnectionEventUsed, "alice")
		ev.ConnectionID, ev.RunID, ev.ToolCallID = c.ID, "run-1", "tc-1"
		require.NoError(t, store.AppendConnectionEvent(ctx, env.ORM, ev))
	}

	page, err := store.ListConnectionEvents(ctx, env.ORM, "alice", c.ID, 2, 0)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Greater(t, page[0].ID, page[1].ID, "newest first")
	assert.Equal(t, "run-1", page[0].RunID)
	assert.Empty(t, page[0].NodeID, "an unset id reads back empty")
	rest, err := twin.ListConnectionEvents(ctx, "alice", c.ID, 10, page[1].ID)
	require.NoError(t, err)
	require.Len(t, rest, 3)
	assert.Equal(t, core.ConnectionEventCreated, rest[2].Kind)
	mine, err := store.ListConnectionEvents(ctx, env.ORM, "alice", c.ID, 10, page[1].ID)
	require.NoError(t, err)
	assert.Equal(t, rest, mine)
	other, err := store.ListConnectionEvents(ctx, env.ORM, "bob", c.ID, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, other, "scoped by user")
}

func TestOAuthFlowSingleUse(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	twin := env.Repo.Connections()
	now := time.Now().UTC().Truncate(time.Microsecond)
	flow := &core.OAuthFlow{
		StateHash: []byte("state-1"), UserID: "alice", SessionIDHash: []byte("sess"), IntegrationID: "github",
		PKCEVerifierSealed: []byte("sealed-pkce"), RedirectAfter: "/settings", ConnectionName: "work",
		Params: map[string]string{"site": "acme"}, ReturnTo: "https://app/return", ExpiresAt: now.Add(10 * time.Minute),
	}
	require.NoError(t, store.InsertOAuthFlow(ctx, env.ORM, flow))
	expired := *flow
	expired.StateHash, expired.ExpiresAt = []byte("state-old"), now.Add(-time.Minute)
	require.NoError(t, twin.CreateOAuthFlow(ctx, &expired))

	peek, err := store.PeekOAuthFlow(ctx, env.ORM, flow.StateHash, now)
	require.NoError(t, err)
	assert.Equal(t, "/settings", peek.RedirectAfter)
	assert.Empty(t, peek.ReconnectConnectionID)
	assert.Equal(t, map[string]string{"site": "acme"}, peek.Params)
	twinPeek, err := twin.PeekOAuthFlow(ctx, flow.StateHash, now)
	require.NoError(t, err)
	assert.Equal(t, twinPeek, peek)

	consumed, err := store.ConsumeOAuthFlow(ctx, env.ORM, flow.StateHash, now)
	require.NoError(t, err)
	assert.Equal(t, peek, consumed)
	_, err = twin.ConsumeOAuthFlow(ctx, flow.StateHash, now)
	assert.ErrorIs(t, err, core.ErrOAuthFlowInvalid, "single use")
	_, err = store.PeekOAuthFlow(ctx, env.ORM, flow.StateHash, now)
	assert.ErrorIs(t, err, core.ErrOAuthFlowInvalid)
	_, err = store.ConsumeOAuthFlow(ctx, env.ORM, expired.StateHash, now)
	assert.ErrorIs(t, err, core.ErrOAuthFlowInvalid, "expired")
}

func connIDs(cs []*core.Connection) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}
