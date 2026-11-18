// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

// A machine connecting refreshes the forge apps of each checkout its
// reconcile probe finds, from the same repo.discover: no extra round trip. A
// daemon older than the forge-app walk reports none, which is unknown and
// writes nothing.
func TestReconcileProjectDaemonsRecordsForgeAppsOnConnect(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const userID = "reconcile-forgeapps-user"
	daemonID := uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))

	now := time.Now().UTC()
	newer := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "newer",
		Path: "/tmp/newer-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	older := &db.Project{ID: uuid.NewString(), UserID: userID, Name: "older",
		Path: "/tmp/older-" + uuid.NewString(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	for _, p := range []*db.Project{newer, older} {
		require.NoError(t, repo.CreateProject(ctx, p))
		require.NoError(t, repo.CreateRepo(ctx, &core.Repo{ID: uuid.NewString(), ProjectID: p.ID, Name: "api",
			RelativePath: "api", CreatedAt: now, UpdatedAt: now}))
	}

	scan, ok := projectfacts.ParseForgeApps([]byte(`{"forge_apps_scanned": true,
	  "discovered": [{"relative_path": "api", "forge_apps": [{"subpath": "", "name": "api"}]}]}`))
	require.True(t, ok)
	check := func(_ context.Context, path string) (projectPathState, error) {
		if path == newer.Path {
			return projectPathState{Exists: true, ForgeApps: scan, ForgeAppsKnown: true}, nil
		}
		// An old daemon: the path is there, its forge apps are unknown.
		return projectPathState{Exists: true}, nil
	}

	_, _, err := reconcileProjectDaemons(ctx, repo, daemonID, userID, check)
	require.NoError(t, err)

	apps, err := repo.ListRepoForgeAppsByProject(ctx, newer.ID)
	require.NoError(t, err)
	require.Len(t, apps, 1)
	require.Equal(t, "api", apps[0].Name)
	repos, err := repo.ListReposByProject(ctx, newer.ID)
	require.NoError(t, err)
	require.NotNil(t, repos[0].ForgeAppsScannedAt)

	apps, err = repo.ListRepoForgeAppsByProject(ctx, older.ID)
	require.NoError(t, err)
	require.Empty(t, apps)
	repos, err = repo.ListReposByProject(ctx, older.ID)
	require.NoError(t, err)
	require.Nil(t, repos[0].ForgeAppsScannedAt, "an old daemon leaves the apps unknown")
}
