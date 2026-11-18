// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestRepoForgeAppQueries: SetRepoForgeApps and ListRepoForgeAppsByProject on
// Postgres, through the store and the Repository on one database.
func TestRepoForgeAppQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, env.Repo.CreateProject(ctx, &core.Project{ID: "proj-fa", UserID: "user-a", Name: "ws", Path: "/srv/ws",
		CreatedAt: now, UpdatedAt: now, LastActive: now}))
	for _, r := range []*core.Repo{
		{ID: "repo-a", ProjectID: "proj-fa", Name: "a", RelativePath: "a", CreatedAt: now, UpdatedAt: now},
		{ID: "repo-b", ProjectID: "proj-fa", Name: "b", RelativePath: "b", CreatedAt: now, UpdatedAt: now},
	} {
		require.NoError(t, env.Repo.CreateRepo(ctx, r))
	}
	set := func(repoID string, complete bool, apps ...core.RepoForgeApp) {
		t.Helper()
		require.NoError(t, env.ORM.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			return store.SetRepoForgeApps(ctx, env.ORM, repoID, apps, complete)
		}))
	}
	list := func() []core.RepoForgeApp {
		t.Helper()
		got, err := store.ListRepoForgeAppsByProject(ctx, env.ORM, "proj-fa")
		require.NoError(t, err)
		return got
	}
	scannedAt := func(repoID string) *time.Time {
		t.Helper()
		r, err := env.Repo.GetRepo(ctx, repoID)
		require.NoError(t, err)
		return r.ForgeAppsScannedAt
	}

	require.Empty(t, list())
	require.Nil(t, scannedAt("repo-a"), "a repo nobody scanned is unknown")

	set("repo-a", true, core.RepoForgeApp{Subpath: "", Name: "api"}, core.RepoForgeApp{Subpath: "web", Name: "site"})
	set("repo-b", false, core.RepoForgeApp{Subpath: "", Name: "worker"})
	require.Equal(t, []core.RepoForgeApp{
		{RepoID: "repo-a", Subpath: "", Name: "api"},
		{RepoID: "repo-a", Subpath: "web", Name: "site"},
		{RepoID: "repo-b", Subpath: "", Name: "worker"},
	}, list())
	require.NotNil(t, scannedAt("repo-a"))
	require.Nil(t, scannedAt("repo-b"), "a partial walk does not stamp the scan")

	// Complete replaces: web is gone, the root renamed.
	set("repo-a", true, core.RepoForgeApp{Subpath: "", Name: "api-v2"})
	require.Equal(t, []core.RepoForgeApp{
		{RepoID: "repo-a", Subpath: "", Name: "api-v2"},
		{RepoID: "repo-b", Subpath: "", Name: "worker"},
	}, list())

	// Complete and empty: known, and none.
	set("repo-a", true)
	require.Equal(t, []core.RepoForgeApp{{RepoID: "repo-b", Subpath: "", Name: "worker"}}, list())
	require.NotNil(t, scannedAt("repo-a"))

	// The Repository's transaction wraps the same query.
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "repo-b", []core.RepoForgeApp{{Subpath: "svc", Name: "svc"}}, true))
	got, err := env.Repo.ListRepoForgeAppsByProject(ctx, "proj-fa")
	require.NoError(t, err)
	require.Equal(t, []core.RepoForgeApp{{RepoID: "repo-b", Subpath: "svc", Name: "svc"}}, got)

	// A repo deleted meanwhile is a no-op, and deleting one takes its apps.
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "repo-gone", []core.RepoForgeApp{{Subpath: "", Name: "x"}}, true))
	require.NoError(t, env.Repo.DeleteRepo(ctx, "repo-b"))
	require.Empty(t, list())
}
