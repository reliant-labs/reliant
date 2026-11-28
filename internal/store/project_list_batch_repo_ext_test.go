// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestProjectListBatchedReads: a project list's repos, forge apps and
// checkouts read in one query each, for exactly the projects named.
func TestProjectListBatchedReads(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC) // fixed: read from no clock

	for _, id := range []string{"proj-1", "proj-2", "proj-other"} {
		require.NoError(t, env.Repo.CreateProject(ctx, &core.Project{ID: id, UserID: "user-a", Name: id, Path: "/srv/" + id,
			CreatedAt: now, UpdatedAt: now, LastActive: now}))
	}
	for _, r := range []*core.Repo{
		{ID: "r1-b", ProjectID: "proj-1", Name: "b", RelativePath: "b"},
		{ID: "r1-root", ProjectID: "proj-1", Name: "root", RelativePath: ""},
		{ID: "r2-root", ProjectID: "proj-2", Name: "root", RelativePath: ""},
		{ID: "ro-root", ProjectID: "proj-other", Name: "root", RelativePath: ""},
	} {
		r.CreatedAt, r.UpdatedAt = now, now
		require.NoError(t, env.Repo.CreateRepo(ctx, r))
	}
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "r1-root", []core.RepoForgeApp{{Subpath: "web", Name: "site"}, {Subpath: "", Name: "api"}}, true))
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "r2-root", []core.RepoForgeApp{{Subpath: "", Name: "two"}}, true))
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "ro-root", []core.RepoForgeApp{{Subpath: "", Name: "other"}}, true))
	require.NoError(t, env.Repo.UpsertProjectDaemon(ctx, "proj-2", "d1", "/srv/proj-2", nil))
	require.NoError(t, env.Repo.UpsertProjectDaemon(ctx, "proj-other", "d1", "/srv/proj-other", nil))

	ids := []string{"proj-2", "proj-1"}
	repos, err := store.ListReposByProjects(ctx, env.ORM, ids)
	require.NoError(t, err)
	var repoIDs []string
	for _, r := range repos {
		repoIDs = append(repoIDs, r.ID)
	}
	require.Equal(t, []string{"r1-root", "r1-b", "r2-root"}, repoIDs, "by project, then relative path")

	apps, err := store.ListRepoForgeAppsByProjects(ctx, env.ORM, ids)
	require.NoError(t, err)
	require.Equal(t, []core.RepoForgeApp{
		{RepoID: "r1-root", Subpath: "", Name: "api"},
		{RepoID: "r1-root", Subpath: "web", Name: "site"},
		{RepoID: "r2-root", Subpath: "", Name: "two"},
	}, apps)

	checkouts, err := store.ListProjectDaemonsForProjects(ctx, env.ORM, ids)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	require.Equal(t, "proj-2", checkouts[0].ProjectID)

	// None named: nothing, without a query.
	none, err := env.Repo.ListReposByProjects(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, none)
	noApps, err := env.Repo.ListRepoForgeAppsByProjects(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, noApps)
	noCheckouts, err := env.Repo.ListProjectDaemonsForProjects(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, noCheckouts)
}
