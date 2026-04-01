package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestListStaleActiveDaemons(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-5 * time.Minute)

	staleConnected := now.Add(-20 * time.Minute)
	freshHeartbeat := now.Add(-1 * time.Minute)
	staleHeartbeat := now.Add(-10 * time.Minute)

	staleByConnected := &Daemon{
		ID:          uuid.New().String(),
		UserID:      "test-user",
		Status:      DaemonStatusActive,
		ConnectedAt: &staleConnected,
	}
	require.NoError(t, repo.UpsertDaemon(ctx, staleByConnected))

	fresh := &Daemon{
		ID:            uuid.New().String(),
		UserID:        "test-user",
		Status:        DaemonStatusActive,
		ConnectedAt:   &staleConnected,
		LastHeartbeat: &freshHeartbeat,
	}
	require.NoError(t, repo.UpsertDaemon(ctx, fresh))

	staleByHeartbeat := &Daemon{
		ID:            uuid.New().String(),
		UserID:        "test-user",
		Status:        DaemonStatusActive,
		ConnectedAt:   &staleConnected,
		LastHeartbeat: &staleHeartbeat,
	}
	require.NoError(t, repo.UpsertDaemon(ctx, staleByHeartbeat))

	disconnected := &Daemon{
		ID:            uuid.New().String(),
		UserID:        "test-user",
		Status:        DaemonStatusDisconnected,
		ConnectedAt:   &staleConnected,
		LastHeartbeat: &staleHeartbeat,
	}
	require.NoError(t, repo.UpsertDaemon(ctx, disconnected))

	stale, err := repo.ListStaleActiveDaemons(ctx, cutoff)
	require.NoError(t, err)

	got := make(map[string]struct{}, len(stale))
	for _, d := range stale {
		got[d.ID] = struct{}{}
		require.Equal(t, DaemonStatusActive, d.Status)
	}

	_, hasStaleConnected := got[staleByConnected.ID]
	_, hasStaleHeartbeat := got[staleByHeartbeat.ID]
	_, hasFresh := got[fresh.ID]
	_, hasDisconnected := got[disconnected.ID]

	require.True(t, hasStaleConnected)
	require.True(t, hasStaleHeartbeat)
	require.False(t, hasFresh)
	require.False(t, hasDisconnected)
}

func TestMarkDaemonsDisconnected(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	connectedAt := now.Add(-30 * time.Minute)
	heartbeat := now.Add(-10 * time.Minute)
	disconnectAt := now

	active1 := &Daemon{
		ID:            uuid.New().String(),
		UserID:        "test-user",
		Status:        DaemonStatusActive,
		ConnectedAt:   &connectedAt,
		LastHeartbeat: &heartbeat,
	}
	active2 := &Daemon{
		ID:            uuid.New().String(),
		UserID:        "test-user",
		Status:        DaemonStatusActive,
		ConnectedAt:   &connectedAt,
		LastHeartbeat: &heartbeat,
	}
	alreadyDisconnected := &Daemon{
		ID:            uuid.New().String(),
		UserID:        "test-user",
		Status:        DaemonStatusDisconnected,
		ConnectedAt:   &connectedAt,
		LastHeartbeat: &heartbeat,
	}

	require.NoError(t, repo.UpsertDaemon(ctx, active1))
	require.NoError(t, repo.UpsertDaemon(ctx, active2))
	require.NoError(t, repo.UpsertDaemon(ctx, alreadyDisconnected))

	require.NoError(t, repo.MarkDaemonsDisconnected(ctx, []string{active1.ID, active2.ID, ""}, disconnectAt))

	reloaded1, err := repo.GetDaemon(ctx, active1.ID)
	require.NoError(t, err)
	require.Equal(t, DaemonStatusDisconnected, reloaded1.Status)
	require.NotNil(t, reloaded1.DisconnectedAt)
	require.WithinDuration(t, disconnectAt, *reloaded1.DisconnectedAt, 2*time.Second)

	reloaded2, err := repo.GetDaemon(ctx, active2.ID)
	require.NoError(t, err)
	require.Equal(t, DaemonStatusDisconnected, reloaded2.Status)
	require.NotNil(t, reloaded2.DisconnectedAt)
	require.WithinDuration(t, disconnectAt, *reloaded2.DisconnectedAt, 2*time.Second)

	reloadedExisting, err := repo.GetDaemon(ctx, alreadyDisconnected.ID)
	require.NoError(t, err)
	require.Equal(t, DaemonStatusDisconnected, reloadedExisting.Status)
}

func TestMarkDaemonsDisconnected_NoIDsNoop(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.MarkDaemonsDisconnected(ctx, nil, time.Now().UTC()))
	require.NoError(t, repo.MarkDaemonsDisconnected(ctx, []string{"", "   "}, time.Now().UTC()))
}
