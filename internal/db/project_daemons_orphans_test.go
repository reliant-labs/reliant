// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The project_daemons_drop_orphans migration, re-run over rows it must keep
// and rows it must drop. It is idempotent, so applying it again over the
// migrated test database exercises exactly its statement.
func TestProjectDaemonsDropOrphans(t *testing.T) {
	sqlBytes, err := os.ReadFile("../../db/migrations/20261102004554_project_daemons_drop_orphans.up.sql")
	require.NoError(t, err)

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	user := "u-" + uuid.NewString()
	projectID := uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &Project{ID: projectID, Name: "my-project", Path: "/p", UserID: user,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	live, foreign := "live-"+user, "foreign-"+user
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: live, UserID: user}))
	require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: foreign, UserID: "someone-else"}))
	removed := "removed-" + user // a machine replaced before removal released its rows
	for _, id := range []string{live, foreign, removed} {
		require.NoError(t, repo.UpsertProjectDaemon(ctx, projectID, id, "/p", nil))
	}

	_, err = raw.ExecContext(ctx, string(sqlBytes))
	require.NoError(t, err)

	rows, err := repo.ListProjectDaemonsForProject(ctx, projectID)
	require.NoError(t, err)
	var kept []string
	for _, r := range rows {
		kept = append(kept, r.DaemonID)
	}
	assert.Equal(t, []string{live}, kept, "only the row of a live machine of the project's owner survives")
}

// Removing a machine deletes its installs in the same transaction, so no
// new orphan is ever left for a reader to trip on.
func TestRemoveDaemonDeletesItsProjectInstalls(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	user := "u-" + uuid.NewString()
	projectID := uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &Project{ID: projectID, Name: "my-project", Path: "/p", UserID: user,
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	old, replacement := "old-"+user, "new-"+user
	for _, id := range []string{old, replacement} {
		require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: id, UserID: user}))
		require.NoError(t, repo.UpsertProjectDaemon(ctx, projectID, id, "/p", nil))
	}

	_, removed, err := repo.RemoveDaemon(ctx, old)
	require.NoError(t, err)
	require.True(t, removed)

	rows, err := repo.ListProjectDaemonsForProject(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, replacement, rows[0].DaemonID)
}
