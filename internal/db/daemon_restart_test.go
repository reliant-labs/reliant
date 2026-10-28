package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDaemonRestartObservationSurvivesDisconnectAndIsSessionFenced(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: "drain", UserID: "owner"}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{DaemonID: "drain", UserID: "owner", Source: DaemonAttachmentSourceInbound, AttachedAt: at, LastStreamActivity: at}))
	const status = `{"deadline_unix_ms":1793100000000,"reason":"test","drained":true}`
	require.ErrorIs(t, repo.SetDaemonRestartStatus(ctx, "other", "drain", at, status), sql.ErrNoRows)
	row, err := repo.GetDaemon(ctx, "drain")
	require.NoError(t, err)
	require.Empty(t, row.RestartStatus)
	require.NoError(t, repo.SetDaemonRestartStatus(ctx, "owner", "drain", at, status))
	require.NoError(t, repo.ReleaseDaemonAttachment(ctx, "drain", at))
	require.ErrorIs(t, repo.SetDaemonRestartStatus(ctx, "owner", "drain", at, status), sql.ErrNoRows, "missing attachment is not durable success")
	// Renewal may recreate this same stream's lease. The same observation
	// must then be persistable; stale streams remain excluded below.
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{DaemonID: "drain", UserID: "owner", Source: DaemonAttachmentSourceInbound, AttachedAt: at, LastStreamActivity: at}))
	require.NoError(t, repo.SetDaemonRestartStatus(ctx, "owner", "drain", at, status))
	require.NoError(t, repo.ReleaseDaemonAttachment(ctx, "drain", at))
	row, err = repo.GetDaemon(ctx, "drain")
	require.NoError(t, err)
	require.JSONEq(t, status, row.RestartStatus)
	next := at.Add(time.Second)
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{DaemonID: "drain", UserID: "owner", Source: DaemonAttachmentSourceInbound, AttachedAt: next, LastStreamActivity: next}))
	require.NoError(t, repo.SetDaemonRestartStatus(ctx, "owner", "drain", next, ""))
	require.ErrorIs(t, repo.SetDaemonRestartStatus(ctx, "owner", "drain", at, status), sql.ErrNoRows)
	row, err = repo.GetDaemon(ctx, "drain")
	require.NoError(t, err)
	require.Empty(t, row.RestartStatus, "late old-stream writes cannot update a replacement")
}
