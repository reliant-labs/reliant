// Copyright (c) 2025 Reliant Labs

package connectorgrant_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

func newBinding(userID, clientID, grantID string) *connectorgrant.ClientBinding {
	return &connectorgrant.ClientBinding{
		ID:         uuid.New().String(),
		UserID:     userID,
		ClientID:   clientID,
		GrantID:    grantID,
		ClientName: "ChatGPT",
	}
}

func TestBindingRoundTrip(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))
	require.NoError(t, store.PutBinding(ctx, newBinding(g.UserID, "chatgpt", g.ID)))

	got, err := store.GetBinding(ctx, g.UserID, "chatgpt")
	require.NoError(t, err)
	require.Equal(t, g.ID, got.GrantID)
	require.Equal(t, "ChatGPT", got.ClientName)
}

// TestReconsentMovesRatherThanAccumulates is the property that keeps consent
// meaningful: a client authorized twice must end up with ONE connector, not
// two. A client that could quietly accumulate access to several workspaces is
// exactly what consent exists to prevent.
func TestReconsentMovesRatherThanAccumulates(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	first := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, first))
	second := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, second))

	require.NoError(t, store.PutBinding(ctx, newBinding(first.UserID, "chatgpt", first.ID)))
	require.NoError(t, store.PutBinding(ctx, newBinding(first.UserID, "chatgpt", second.ID)))

	all, err := store.ListBindingsByUser(ctx, first.UserID)
	require.NoError(t, err)
	require.Len(t, all, 1, "re-consenting must move the client, not add a second authorization")
	require.Equal(t, second.ID, all[0].GrantID)
}

// TestBindingToRevokedGrantDoesNotResolve: a choice that no longer exists must
// send the user back to consent rather than silently acting on a dead grant.
func TestBindingToRevokedGrantDoesNotResolve(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))
	require.NoError(t, store.PutBinding(ctx, newBinding(g.UserID, "chatgpt", g.ID)))

	_, err := store.RevokeGrant(ctx, g.UserID, g.ID)
	require.NoError(t, err)

	_, err = store.GetBinding(ctx, g.UserID, "chatgpt")
	require.ErrorIs(t, err, connectorgrant.ErrNotFound)
}

// TestDeletingConnectorRevokesItsClients: "revoke this connector" must cut off
// every application acting through it.
func TestDeletingConnectorRevokesItsClients(t *testing.T) {
	store, rawDB, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))
	require.NoError(t, store.PutBinding(ctx, newBinding(g.UserID, "chatgpt", g.ID)))

	_, err := rawDB.Exec(`DELETE FROM connector_grants WHERE id = $1`, g.ID)
	require.NoError(t, err)

	all, err := store.ListBindingsByUser(ctx, g.UserID)
	require.NoError(t, err)
	require.Empty(t, all, "deleting a connector must disconnect its clients")
}

func TestDeleteBindingIsScopedToOwner(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))
	require.NoError(t, store.PutBinding(ctx, newBinding(g.UserID, "chatgpt", g.ID)))

	deleted, err := store.DeleteBinding(ctx, "someone-else", "chatgpt")
	require.NoError(t, err)
	require.False(t, deleted, "one user must not disconnect another's client")

	deleted, err = store.DeleteBinding(ctx, g.UserID, "chatgpt")
	require.NoError(t, err)
	require.True(t, deleted)
}

// TestDisconnectingAClientKeepsTheConnector: the same connector may serve
// several applications, so disconnecting one must not revoke the others.
func TestDisconnectingAClientKeepsTheConnector(t *testing.T) {
	store, _, daemonID := setupStore(t)
	ctx := context.Background()

	g := newGrant(daemonID)
	require.NoError(t, store.CreateGrant(ctx, g))
	require.NoError(t, store.PutBinding(ctx, newBinding(g.UserID, "chatgpt", g.ID)))
	require.NoError(t, store.PutBinding(ctx, newBinding(g.UserID, "claude", g.ID)))

	_, err := store.DeleteBinding(ctx, g.UserID, "chatgpt")
	require.NoError(t, err)

	remaining, err := store.GetBinding(ctx, g.UserID, "claude")
	require.NoError(t, err, "disconnecting one client must not affect another")
	require.Equal(t, g.ID, remaining.GrantID)

	_, err = store.GetGrantByID(ctx, g.ID)
	require.NoError(t, err, "the connector itself must survive")
}
