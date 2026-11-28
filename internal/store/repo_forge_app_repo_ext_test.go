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
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC) // fixed: read from no clock

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

// TestListReposHoldingForgeApp: the repos of the named projects that hold a
// forge app of one name, once each however many subpaths declare it, and
// none of another project's.
func TestListReposHoldingForgeApp(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	remote := "git@github.com:acme/shop.git"
	for _, p := range []string{"proj-1", "proj-2", "proj-other"} {
		require.NoError(t, env.Repo.CreateProject(ctx, &core.Project{ID: p, UserID: "user-a", Name: p, Path: "/srv/" + p,
			CreatedAt: now, UpdatedAt: now, LastActive: now}))
	}
	for _, r := range []*core.Repo{
		{ID: "r1-root", ProjectID: "proj-1", Name: "root", RelativePath: "", RemoteURL: &remote, CreatedAt: now, UpdatedAt: now},
		{ID: "r1-lib", ProjectID: "proj-1", Name: "lib", RelativePath: "lib", CreatedAt: now, UpdatedAt: now},
		{ID: "r2-root", ProjectID: "proj-2", Name: "root", RelativePath: "", CreatedAt: now, UpdatedAt: now},
		{ID: "ro-root", ProjectID: "proj-other", Name: "root", RelativePath: "", CreatedAt: now, UpdatedAt: now},
	} {
		require.NoError(t, env.Repo.CreateRepo(ctx, r))
	}
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "r1-root", []core.RepoForgeApp{{Subpath: "", Name: "shop"}, {Subpath: "deploy", Name: "shop"}}, true))
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "r1-lib", []core.RepoForgeApp{{Subpath: "", Name: "lib"}}, true))
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "r2-root", []core.RepoForgeApp{{Subpath: "web", Name: "shop"}}, true))
	require.NoError(t, env.Repo.SetRepoForgeApps(ctx, "ro-root", []core.RepoForgeApp{{Subpath: "", Name: "shop"}}, true))

	got, err := env.Repo.ListReposHoldingForgeApp(ctx, "shop", []string{"proj-1", "proj-2"})
	require.NoError(t, err)
	ids := make([]string, len(got))
	for i, r := range got {
		ids[i] = r.ID
	}
	require.Equal(t, []string{"r1-root", "r2-root"}, ids)
	require.Equal(t, remote, *got[0].RemoteURL)

	got, err = env.Repo.ListReposHoldingForgeApp(ctx, "shop", nil)
	require.NoError(t, err)
	require.Empty(t, got, "no projects, no repos")
}
