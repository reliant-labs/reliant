// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The handlers and activities read these rows through the generated stores,
// not through the Repository wrappers that used to refuse an empty id first.
// Two classes of guard were behind those wrappers:
//
//   - a Get by an empty PRIMARY KEY: harmless without the guard, provided the
//     query answers not-found and never some other row. Proven here against a
//     table that holds rows, so an arbitrary-row answer would show.
//   - a read scoped by an id (a project's repos): an empty scope key must be
//     refused, never read. That guard lives in the store function itself.

func TestEmptyPrimaryKeyGetsAreNotFound(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()

	require.NoError(t, store.InsertProject(ctx, env.ORM, &core.Project{ID: "p-any", UserID: "user-a", Name: "any", Path: "/srv/any"}))
	require.NoError(t, store.InsertWorktree(ctx, env.ORM, &core.Worktree{ID: "w-any", ProjectID: "p-any", Name: "any", Path: "/srv/any/w", Branch: "any"}))
	require.NoError(t, env.Repo.UpsertDaemon(ctx, &core.Daemon{ID: "d-any", UserID: "user-a"}))
	chat := boundFixture(t, env, "empty-id", false)
	convWorkflow(t, env, "wf-any", chat.ID, core.WorkflowStateActive, core.StopReasonUnspecified)
	// The table holds rows, so a "first row" answer to an empty id would be a
	// row, not an error.
	for table, id := range map[string]string{"projects": "p-any", "worktrees": "w-any", "daemons": "d-any", "workflows": "wf-any"} {
		var n int
		require.NoError(t, env.SQL.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = $1`, id).Scan(&n))
		require.Equal(t, 1, n, table)
	}

	p, err := store.GetProject(ctx, env.ORM, "")
	require.Nil(t, p)
	require.True(t, svcerr.IsNotFound(err), "GetProject(\"\") = %v", err)

	d, err := store.GetDaemon(ctx, env.ORM, "")
	require.Nil(t, d)
	require.True(t, svcerr.IsNotFound(err), "GetDaemon(\"\") = %v", err)

	w, err := store.GetWorktree(ctx, env.ORM, "")
	require.Nil(t, w)
	require.ErrorIs(t, err, core.ErrWorktreeNotFound)

	wf, err := store.LoadWorkflow(ctx, env.ORM, "")
	require.Nil(t, wf)
	require.True(t, svcerr.IsNotFound(err), "LoadWorkflow(\"\") = %v", err)
}

func TestListReposByProjectRefusesAnEmptyProject(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	require.NoError(t, store.InsertRepo(ctx, env.ORM, &core.Repo{ID: "r-1", ProjectID: storetest.SeedProjectID, Name: "a", RelativePath: "."}))

	got, err := store.ListReposByProject(ctx, env.ORM, "")
	require.Error(t, err, "an empty project id must be refused, never read")
	require.Empty(t, got, "a repo of another project came back for an empty scope")

	// The store a Deps field holds is the same function.
	got, err = store.NewRepoExtStore(env.ORM).ListReposByProject(ctx, "")
	require.Error(t, err)
	require.Empty(t, got)

	// The guard refuses only the empty id.
	got, err = store.ListReposByProject(ctx, env.ORM, storetest.SeedProjectID)
	require.NoError(t, err)
	require.Len(t, got, 1)
}
