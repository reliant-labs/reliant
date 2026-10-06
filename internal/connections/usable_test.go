// Copyright (c) 2025 Reliant Labs

package connections_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// Usable answers what ForCall would, per ref, for the run's owner — read from
// the run, as ForCall reads it — without touching the secret or the audit
// trail.
func TestResolver_UsableAgreesWithForCall(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn := e.connect("alice", "a")
	github := connections.Ref{IntegrationID: "github"}
	svc := connections.Ref{IntegrationID: "svc"}
	site := func(run string) connections.CallSite {
		return connections.CallSite{RunID: run, Placement: connections.PlacementServer}
	}

	aliceRun := e.newRun("alice")
	got, err := e.resolver.Usable(ctx, site(aliceRun), []connections.Ref{github, svc})
	require.NoError(t, err)
	require.NoError(t, got[github], "alice's default GitHub connection is usable from her run")
	require.ErrorIs(t, got[svc], connections.ErrFailedPrecondition, "she has no svc connection")
	_, forCallErr := e.resolver.ForCall(ctx, site(aliceRun), svc)
	require.Equal(t, connections.CodeOf(forCallErr), connections.CodeOf(got[svc]), "the same answer ForCall gives")

	got, err = e.resolver.Usable(ctx, site(e.newRun("bob")), []connections.Ref{github})
	require.NoError(t, err)
	require.ErrorIs(t, got[github], connections.ErrFailedPrecondition, "alice's connection is not bob's")

	require.NoError(t, e.store.WithSecretsLock(ctx, "alice", conn.ID, func(tx core.SecretsTx) error {
		return tx.MarkStatus(ctx, core.ConnectionStatusNeedsReauth, "invalid_grant")
	}))
	got, err = e.resolver.Usable(ctx, site(aliceRun), []connections.Ref{github})
	require.NoError(t, err)
	require.Equal(t, connections.CodeNeedsReauth, connections.CodeOf(got[github]), "a connection needing re-auth is not usable")

	require.Equal(t, 0, e.count(`SELECT count(*) FROM connection_events WHERE connection_id=$1 AND kind='used'`, conn.ID),
		"checking is not using")
}

// The owner must be readable from the run, and only server placement may ask.
func TestResolver_UsableNeedsAnOwnerAndServerPlacement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.connect("alice", "a")
	refs := []connections.Ref{{IntegrationID: "github"}}

	_, err := e.resolver.Usable(ctx, connections.CallSite{RunID: "no-such-run", Placement: connections.PlacementServer}, refs)
	require.ErrorIs(t, err, connections.ErrFailedPrecondition)

	_, err = e.resolver.Usable(ctx, connections.CallSite{RunID: e.newRun("alice"), Placement: connections.PlacementDaemon}, refs)
	require.ErrorIs(t, err, connections.ErrDaemonPlacement)
}
