// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// User B cannot get, use, delete, rename, default, test or list the events of
// user A's connection, through the service and through the resolver.
func TestOwnership_OtherUserSeesNotFound(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.connect("alice", "a")

	_, err := e.svc.Get(ctx, "bob", a.ID)
	require.ErrorIs(t, err, connections.ErrNotFound)

	list, err := e.svc.List(ctx, "bob", "")
	require.NoError(t, err)
	require.Empty(t, list)

	_, err = e.svc.Rename(ctx, "bob", a.ID, "mine")
	require.ErrorIs(t, err, connections.ErrNotFound)
	_, err = e.svc.SetDefault(ctx, "bob", a.ID)
	require.ErrorIs(t, err, connections.ErrNotFound)
	_, err = e.svc.Test(ctx, "bob", a.ID)
	require.ErrorIs(t, err, connections.ErrNotFound)
	_, err = e.svc.Events(ctx, "bob", a.ID, 10, 0)
	require.ErrorIs(t, err, connections.ErrNotFound)
	require.ErrorIs(t, e.svc.Delete(ctx, "bob", a.ID), connections.ErrNotFound)

	_, err = e.tokens.Token(ctx, "bob", a.ID)
	require.ErrorIs(t, err, connections.ErrNotFound)

	// The store enforces it too, not just the service.
	_, err = e.store.GetSecrets(ctx, "bob", a.ID)
	require.ErrorIs(t, err, core.ErrConnectionNotFound)

	// Alice's connection is untouched.
	got, err := e.svc.Get(ctx, "alice", a.ID)
	require.NoError(t, err)
	require.Equal(t, "a", got.Name)
	require.Equal(t, 0, e.count(`SELECT count(*) FROM connection_events WHERE kind IN ('deleted','renamed') AND connection_id=$1`, a.ID))
}

func TestOwnership_ResolverRefusesForeignConnection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.connect("alice", "a")
	bobRun := e.newRun("bob")

	_, err := e.resolver.ForCall(ctx, connections.CallSite{RunID: bobRun, Placement: connections.PlacementServer},
		connections.Ref{ConnectionID: a.ID})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Contains(t, err.Error(), "no such connection", "must look exactly like a missing connection")
	require.Zero(t, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used'`, a.ID),
		"a refused resolve must not record a use on the victim's connection")

	// By integration, bob has no default of his own.
	_, err = e.resolver.ForCall(ctx, connections.CallSite{RunID: bobRun, Placement: connections.PlacementServer},
		connections.Ref{IntegrationID: "github"})
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)
	require.Contains(t, err.Error(), "no github connection")
}

func TestOwnership_NameUniquePerUserNotGlobal(t *testing.T) {
	e := newEnv(t)
	e.connect("alice", "work")
	e.gh.accountID, e.gh.login = 7, "bobby"
	e.connect("bob", "work") // same name, different user: fine

	e.gh.accountID, e.gh.login = 8, "alice2"
	_, err := e.svc.StartOAuth(context.Background(), connections.StartParams{UserID: "alice", IntegrationID: "github", Name: "work", ClientOrigin: testAppOrigin})
	require.NoError(t, err)
}
