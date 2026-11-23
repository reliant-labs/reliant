// Copyright (c) 2025 Reliant Labs

package connectionvault_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func newStore(t *testing.T) (connectionvault.Service, *sql.DB) {
	t.Helper()
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	svc, err := connectionvault.New(connectionvault.Deps{DB: repo.ORM()})
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state) VALUES
		('vk-u', 'user', 'userA', 1, 'kek', '\x00', 'primary'), ('vk-o', 'org', 'org-a', 1, 'kek', '\x00', 'primary')`)
	require.NoError(t, err)
	return svc, raw
}

func personal(user, integration, name string) *core.Connection {
	return &core.Connection{ID: uuid.NewString(), OwnerKind: core.ConnectionOwnerUser, UserID: user, IntegrationID: integration,
		AuthKind: core.ConnectionAuthOAuth2, Name: name, Status: core.ConnectionStatusActive}
}

func orgConn(org, creator, integration, name string) *core.Connection {
	return &core.Connection{ID: uuid.NewString(), OwnerKind: core.ConnectionOwnerOrg, UserID: creator, OrgID: &org, IntegrationID: integration,
		AuthKind: core.ConnectionAuthAPIKey, Name: name, Status: core.ConnectionStatusActive}
}

func sealed(vaultKey, field, ciphertext string) core.ConnectionSecret {
	return core.ConnectionSecret{Field: field, VaultKeyID: vaultKey, Ciphertext: []byte(ciphertext)}
}

func created(user string) core.ConnectionEvent {
	return core.ConnectionEvent{UserID: user, Kind: core.ConnectionEventCreated, Actor: "user"}
}

// The ciphertext of user A's connection is never returned to user B, by any read that names the
// connection id.
func TestConnectionSecretsOfUserAAreNeverReturnedToUserB(t *testing.T) {
	t.Parallel()
	svc, _ := newStore(t)
	ctx := context.Background()
	a := personal("userA", "github", "work")
	require.NoError(t, svc.CreateConnection(ctx, a, []core.ConnectionSecret{sealed("vk-u", core.SecretFieldAccessToken, "SEALED-OF-A")}, created("userA")))

	got, err := svc.GetSecrets(ctx, "userA", a.ID)
	require.NoError(t, err)
	assert.Equal(t, "SEALED-OF-A", string(got[core.SecretFieldAccessToken].Ciphertext))

	other, err := svc.GetSecrets(ctx, "userB", a.ID)
	assert.Empty(t, other, "userB names userA's connection id")
	assert.Error(t, err)
	_, err = svc.GetOwnedSecrets(ctx, core.UserConnectionOwner("userB"), a.ID)
	assert.Error(t, err)
	_, err = svc.GetConnection(ctx, "userB", a.ID)
	assert.Error(t, err)
	_, err = svc.GetOwnedConnection(ctx, core.OrgConnectionOwner("org-b"), a.ID)
	assert.Error(t, err, "an org never reads a person's connection")
	assert.Error(t, svc.WithSecretsLock(ctx, "userB", a.ID, func(*connectionvault.SecretsTx) error {
		t.Fatal("the lock callback must not run for another user's connection")
		return nil
	}))
	assert.Error(t, svc.DeleteConnection(ctx, "userB", a.ID, created("userB")))
	list, err := svc.ListConnections(ctx, "userB", "github")
	require.NoError(t, err)
	assert.Empty(t, list)

	// A's connection is untouched by B's attempts.
	still, err := svc.GetSecrets(ctx, "userA", a.ID)
	require.NoError(t, err)
	assert.Len(t, still, 1)
}

// The ciphertext of org A's connection is never returned to org B or to the admin who created it
// when acting as a person.
func TestConnectionSecretsOfOrgAAreNeverReturnedToOrgB(t *testing.T) {
	t.Parallel()
	svc, _ := newStore(t)
	ctx := context.Background()
	a := orgConn("org-a", "adminA", "slack", "bot")
	require.NoError(t, svc.CreateConnection(ctx, a, []core.ConnectionSecret{sealed("vk-o", core.SecretFieldAPIKey, "SEALED-OF-ORG-A")}, created("adminA")))

	got, err := svc.GetOwnedSecrets(ctx, core.OrgConnectionOwner("org-a"), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "SEALED-OF-ORG-A", string(got[core.SecretFieldAPIKey].Ciphertext))

	_, err = svc.GetOwnedSecrets(ctx, core.OrgConnectionOwner("org-b"), a.ID)
	assert.Error(t, err, "org B names org A's connection id")
	_, err = svc.GetSecrets(ctx, "adminA", a.ID)
	assert.Error(t, err, "the creator acting as a person does not reach the org's connection")
	_, err = svc.GetOwnedConnection(ctx, core.OrgConnectionOwner("org-b"), a.ID)
	assert.Error(t, err)
	assert.Error(t, svc.SoftDeleteOwnedConnection(ctx, core.OrgConnectionOwner("org-b"), a.ID, created("adminB")))
	listB, err := svc.ListOwnedConnections(ctx, core.OrgConnectionOwner("org-b"), "slack")
	require.NoError(t, err)
	assert.Empty(t, listB)
	assert.Error(t, svc.WithOwnedSecretsLock(ctx, core.OrgConnectionOwner("org-b"), a.ID, func(*connectionvault.SecretsTx) error {
		t.Fatal("the lock callback must not run for another org's connection")
		return nil
	}))
}

// A deleted connection's ciphertext is destroyed with it.
func TestDeleteDestroysTheCiphertext(t *testing.T) {
	t.Parallel()
	svc, raw := newStore(t)
	ctx := context.Background()
	a := personal("userA", "github", "work")
	require.NoError(t, svc.CreateConnection(ctx, a, []core.ConnectionSecret{sealed("vk-u", core.SecretFieldAccessToken, "SEALED")}, created("userA")))
	require.NoError(t, svc.DeleteConnection(ctx, "userA", a.ID, core.ConnectionEvent{UserID: "userA", Kind: core.ConnectionEventDeleted, Actor: "user"}))
	var n int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM connection_secrets WHERE connection_id = $1`, a.ID).Scan(&n))
	assert.Zero(t, n)
}

func TestNewRequiresADatabase(t *testing.T) {
	t.Parallel()
	_, err := connectionvault.New(connectionvault.Deps{})
	require.Error(t, err)
}
