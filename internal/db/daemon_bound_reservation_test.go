// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func reserved(t *testing.T, repo *Repo, id string) bool {
	t.Helper()
	d, err := repo.GetDaemon(context.Background(), id)
	require.NoError(t, err)
	return d.RequiresBoundToken
}

// A bound write reserves the id; from then on an unbound write by the same
// owner is refused with its own sentinel and changes nothing, a bound one
// still refreshes the row, and nothing clears the reservation.
func TestUpsertDaemonReservationForBoundCredentials(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id := uuid.NewString()
	cloudHost, laptopHost := "cloud-pod", "laptop"

	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A", Hostname: &cloudHost, RequiresBoundToken: true}))
	require.True(t, reserved(t, repo, id))

	err := repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A", Hostname: &laptopHost})
	require.True(t, errors.Is(err, ErrDaemonRequiresBoundToken), "unbound write on a reserved id: %v", err)
	require.False(t, errors.Is(err, ErrDaemonOwnedByAnotherUser), "the caller owns the row; it is not foreign")
	got, err := repo.GetDaemon(ctx, id)
	require.NoError(t, err)
	require.Equal(t, cloudHost, *got.Hostname, "a refused write changes nothing")
	require.True(t, got.RequiresBoundToken)

	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A", Hostname: &cloudHost, RequiresBoundToken: true}),
		"the bound credential keeps working")

	// Another user is still refused as foreign, reservation or not.
	err = repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-B", RequiresBoundToken: true})
	require.True(t, errors.Is(err, ErrDaemonOwnedByAnotherUser), "foreign write: %v", err)
}

// Unreserved rows (laptops on login tokens) keep working exactly as before,
// and a later bound write reserves them.
func TestUpsertDaemonUnboundRowsStayUnreservedUntilABoundWrite(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id := uuid.NewString()

	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A"}))
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A"}))
	require.False(t, reserved(t, repo, id))

	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A", RequiresBoundToken: true}))
	require.True(t, reserved(t, repo, id))
	require.True(t, errors.Is(repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A"}), ErrDaemonRequiresBoundToken))
}

// The control plane reports a lifecycle only for managed machines, so an
// applied lifecycle observation reserves the id: a managed machine created by
// an identity event is reserved before its pod ever connects.
func TestApplyDaemonLifecycleReservesTheID(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id := uuid.NewString()

	wrote, err := repo.UpsertDaemonIdentity(ctx, DaemonIdentity{DaemonID: id, UserID: "user-A", Name: "cloud", DaemonType: "managed"})
	require.NoError(t, err)
	require.True(t, wrote)
	require.False(t, reserved(t, repo, id), "an identity alone is not the signal")

	applied, err := repo.ApplyDaemonLifecycle(ctx, DaemonLifecycleUpdate{DaemonID: id, Phase: "provisioning", ChangedAt: time.Now().UTC()})
	require.NoError(t, err)
	require.True(t, applied)
	require.True(t, reserved(t, repo, id))
	require.True(t, errors.Is(repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: "user-A"}), ErrDaemonRequiresBoundToken))
}

// The migration's backfill, re-run over the migrated test database (the file
// is idempotent): rows carrying a control-plane signal — a mirrored lifecycle
// phase, or an outbound (gateway-dialed) attachment — are reserved; a row
// whose only claim is the daemon_type the daemon reported itself is not.
func TestDaemonRequiresBoundTokenBackfill(t *testing.T) {
	sqlBytes, err := os.ReadFile("../../db/migrations/20261101221709_daemon_requires_bound_token.up.sql")
	require.NoError(t, err)

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	user := "u-" + uuid.NewString()
	withPhase, dialedIn, selfReported, laptop := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	managed := "managed"
	for _, id := range []string{withPhase, dialedIn, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: user}))
	}
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: selfReported, UserID: user, DaemonType: &managed}))
	_, err = raw.ExecContext(ctx, `UPDATE daemons SET lifecycle_phase = 'ready' WHERE id = $1`, withPhase)
	require.NoError(t, err)
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: dialedIn, UserID: user, Source: DaemonAttachmentSourceOutbound,
		AttachedAt: time.Now().UTC(), LastStreamActivity: time.Now().UTC(),
	}))
	_, err = raw.ExecContext(ctx, `UPDATE daemons SET requires_bound_token = false WHERE user_id = $1`, user)
	require.NoError(t, err)

	_, err = raw.ExecContext(ctx, string(sqlBytes))
	require.NoError(t, err)

	require.True(t, reserved(t, repo, withPhase), "a mirrored control-plane lifecycle reserves the id")
	require.True(t, reserved(t, repo, dialedIn), "a gateway-dialed pod reserves the id")
	require.False(t, reserved(t, repo, selfReported), "a daemon's self-reported type is not trusted")
	require.False(t, reserved(t, repo, laptop))
}
