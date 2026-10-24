// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The build a daemon registers with lives on its attachment lease: written by
// the connect upsert, left alone by renewals, replaced by the next session's
// upsert, and zero (an unknown build) when none was reported.
func TestDaemonAttachmentBuildLifecycle(t *testing.T) {
	t.Parallel()
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID, daemonID = "build-user", "build-daemon"
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: daemonID, UserID: userID}))
	t.Cleanup(func() { _, _ = rawDB.Exec(`DELETE FROM daemon_attachment WHERE user_id = $1`, userID) })

	read := func() DaemonBuild {
		t.Helper()
		fresh, err := repo.ListFreshDaemonAttachmentsForUser(ctx, userID, time.Minute)
		require.NoError(t, err)
		require.Len(t, fresh, 1)
		return fresh[0].Build
	}

	first := DaemonBuild{Version: "1.8.3-dev+b675d224", Commit: "b675d22417c3", CommitTimeUnix: 1792800000, ForgeVersion: "v0.1.47"}
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound, AttachedAt: now, Build: first,
	}))
	require.Equal(t, first, read())

	// The all-attachments list serves it too (a different read path).
	all, err := repo.ListAllDaemonAttachments(ctx)
	require.NoError(t, err)
	var found bool
	for _, a := range all {
		if a.DaemonID == daemonID {
			found = true
			require.Equal(t, first, a.Build)
		}
	}
	require.True(t, found)

	// A heartbeat renewal finds the row and leaves the build alone, even when
	// the renewing session's copy differs.
	_, err = repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound, AttachedAt: now,
		LastStreamActivity: now.Add(time.Second), Build: DaemonBuild{Version: "other"},
	})
	require.NoError(t, err)
	require.Equal(t, first, read())

	// A new session replaces it, and one that reports nothing clears it.
	second := DaemonBuild{Version: "1.8.4", Commit: "c0ffee", CommitTimeUnix: 1792900000, ForgeVersion: "v0.1.48"}
	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound, AttachedAt: now.Add(time.Minute), Build: second,
	}))
	require.Equal(t, second, read())

	require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound, AttachedAt: now.Add(2 * time.Minute),
	}))
	require.Equal(t, DaemonBuild{}, read())
	require.False(t, read().Known())

	// A renewal that has to re-create a reaped row writes the build it carries.
	_, err = rawDB.Exec(`DELETE FROM daemon_attachment WHERE daemon_id = $1`, daemonID)
	require.NoError(t, err)
	created, err := repo.RenewDaemonAttachment(ctx, &DaemonAttachment{
		DaemonID: daemonID, UserID: userID, Source: DaemonAttachmentSourceInbound,
		AttachedAt: now, LastStreamActivity: now, Build: first,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, first, read())
}

// A row written before the migration (or by a release that does not know the
// columns) reads as an unknown build.
func TestDaemonAttachmentWithoutBuildColumnsReadsAsUnknown(t *testing.T) {
	t.Parallel()
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	const userID, daemonID = "legacy-build-user", "legacy-build-daemon"
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: daemonID, UserID: userID}))
	t.Cleanup(func() { _, _ = rawDB.Exec(`DELETE FROM daemon_attachment WHERE user_id = $1`, userID) })

	_, err := rawDB.Exec(`INSERT INTO daemon_attachment (daemon_id, user_id, source) VALUES ($1, $2, 'inbound')`, daemonID, userID)
	require.NoError(t, err)

	fresh, err := repo.ListFreshDaemonAttachmentsForUser(ctx, userID, time.Minute)
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.Equal(t, DaemonBuild{}, fresh[0].Build)
}
