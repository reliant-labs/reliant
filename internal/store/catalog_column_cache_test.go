// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"unsafe"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// These hold db.Repo's catalog column cache (internal/db/catalog_column_cache.go)
// to what the database stores: a read is served from the cache only while
// the row is the version it was read at, which they see as the same string
// handed out again.

const catalogDaemon = "d-catalog-cache"

func catalogEnv(t *testing.T) *storetest.Env {
	t.Helper()
	env := storetest.New(t)
	mustExec(t, env, `INSERT INTO daemons (id, user_id, created_at, updated_at) VALUES ($1, $2, NOW(), NOW())`, catalogDaemon, storetest.SeedUserID)
	return env
}

func strPtr(s string) *string { return &s }

func pushMachineCatalog(t *testing.T, ctx context.Context, env *storetest.Env, workflows, presets *string) {
	t.Helper()
	at := time.Date(2026, 11, 26, 7, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpsertProjectMachineCatalog(ctx, env.ORM, &store.ProjectMachineCatalog{
		ProjectID: storetest.SeedProjectID, DaemonID: catalogDaemon, WorkflowsJSON: workflows, PresetsJSON: presets, PushedAt: at, UpdatedAt: at,
	}))
}

func machineWorkflows(t *testing.T, ctx context.Context, env *storetest.Env) (*string, bool) {
	t.Helper()
	v, synced, err := env.Repo.GetProjectMachineWorkflowsJSON(ctx, storetest.SeedProjectID, catalogDaemon)
	require.NoError(t, err)
	return v, synced
}

// A machine's catalog read again while its row is unchanged is served from
// the cache; any write to the row, whatever column it changes and whether or
// not it moves a timestamp, is read again; a row that is gone is answered as
// never synced.
func TestMachineCatalogColumnsServeUnchangedRowsAndRereadChangedOnes(t *testing.T) {
	t.Parallel()
	env := catalogEnv(t)
	ctx := context.Background()

	v, synced := machineWorkflows(t, ctx, env)
	require.False(t, synced)
	require.Nil(t, v)

	pushMachineCatalog(t, ctx, env, strPtr(`[{"name":"one"}]`), strPtr(`[{"name":"fast"}]`))
	first, synced := machineWorkflows(t, ctx, env)
	require.True(t, synced)
	require.Equal(t, `[{"name":"one"}]`, *first)
	again, _ := machineWorkflows(t, ctx, env)
	require.Same(t, unsafe.StringData(*first), unsafe.StringData(*again), "an unchanged row is served from the cache")
	require.NotSame(t, first, again, "each caller gets a pointer of its own")
	*again = "scribbled"
	third, _ := machineWorkflows(t, ctx, env)
	require.Equal(t, `[{"name":"one"}]`, *third, "a caller writing through its pointer changes nobody else's")

	presets, synced, err := env.Repo.GetProjectMachinePresetsJSON(ctx, storetest.SeedProjectID, catalogDaemon)
	require.NoError(t, err)
	require.True(t, synced)
	require.Equal(t, `[{"name":"fast"}]`, *presets)

	// A sync.
	pushMachineCatalog(t, ctx, env, strPtr(`[{"name":"two"}]`), nil)
	v, _ = machineWorkflows(t, ctx, env)
	require.Equal(t, `[{"name":"two"}]`, *v)
	presets, synced, err = env.Repo.GetProjectMachinePresetsJSON(ctx, storetest.SeedProjectID, catalogDaemon)
	require.NoError(t, err)
	require.True(t, synced)
	require.Nil(t, presets, "a machine that now syncs no presets")

	// A write of another column, then one that moves no timestamp.
	require.NoError(t, store.SetProjectMachineRepoStates(ctx, env.ORM, storetest.SeedProjectID, catalogDaemon, strPtr(`[]`)))
	v, _ = machineWorkflows(t, ctx, env)
	require.Equal(t, `[{"name":"two"}]`, *v)
	mustExec(t, env, `UPDATE project_machine_catalogs SET workflows_json = '[{"name":"three"}]' WHERE project_id = $1 AND daemon_id = $2`, storetest.SeedProjectID, catalogDaemon)
	three, _ := machineWorkflows(t, ctx, env)
	require.Equal(t, `[{"name":"three"}]`, *three, "a write that moves no timestamp is still seen")

	mustExec(t, env, `DELETE FROM project_machine_catalogs WHERE project_id = $1 AND daemon_id = $2`, storetest.SeedProjectID, catalogDaemon)
	v, synced = machineWorkflows(t, ctx, env)
	require.False(t, synced)
	require.Nil(t, v)
	pushMachineCatalog(t, ctx, env, strPtr(`[{"name":"three"}]`), nil)
	v, synced = machineWorkflows(t, ctx, env)
	require.True(t, synced)
	require.Equal(t, *three, *v)
	require.NotSame(t, unsafe.StringData(*three), unsafe.StringData(*v), "a row stored again is read again")
}

// A transaction reads its own writes, and what it read is never kept: rolled
// back, the next read is served the committed value the cache held before.
func TestMachineCatalogColumnsInATransactionDoNotFillTheCache(t *testing.T) {
	t.Parallel()
	env := catalogEnv(t)
	ctx := context.Background()
	pushMachineCatalog(t, ctx, env, strPtr(`[{"name":"committed"}]`), nil)
	committed, _ := machineWorkflows(t, ctx, env)
	require.Equal(t, `[{"name":"committed"}]`, *committed)

	rollback := errors.New("roll back")
	err := env.Repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		v, _ := machineWorkflows(t, ctx, env)
		require.Same(t, unsafe.StringData(*committed), unsafe.StringData(*v), "a transaction is served the committed row from the cache")
		pushMachineCatalog(t, ctx, env, strPtr(`[{"name":"uncommitted"}]`), nil)
		v, _ = machineWorkflows(t, ctx, env)
		require.Equal(t, `[{"name":"uncommitted"}]`, *v, "and sees its own write")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	v, _ := machineWorkflows(t, ctx, env)
	require.Equal(t, `[{"name":"committed"}]`, *v)
	require.Same(t, unsafe.StringData(*committed), unsafe.StringData(*v), "the transaction's read was not kept")
}

// A project's own catalog columns are cached the same way, and a project with
// no config record is still NotFound.
func TestProjectConfigColumnsServeUnchangedRowsAndRereadChangedOnes(t *testing.T) {
	t.Parallel()
	env := catalogEnv(t)
	ctx := context.Background()
	project := storetest.SeedProjectID
	mustExec(t, env, `DELETE FROM project_configs WHERE project_id = $1`, project)

	_, err := env.Repo.GetProjectWorkflowsJSON(ctx, project)
	require.True(t, svcerr.IsNotFound(err), "no record: %v", err)
	_, err = env.Repo.GetProjectPresetsJSON(ctx, project)
	require.True(t, svcerr.IsNotFound(err), "no record: %v", err)

	mustExec(t, env, `INSERT INTO project_configs (id, project_id, daemon_id, pushed_at, project_workflows_json, project_presets_json)
VALUES ('pc-catalog-cache', $1, $2, NOW(), '[{"name":"union"}]', NULL)`, project, catalogDaemon)
	first, err := env.Repo.GetProjectWorkflowsJSON(ctx, project)
	require.NoError(t, err)
	require.Equal(t, `[{"name":"union"}]`, *first)
	again, err := env.Repo.GetProjectWorkflowsJSON(ctx, project)
	require.NoError(t, err)
	require.Same(t, unsafe.StringData(*first), unsafe.StringData(*again), "an unchanged record is served from the cache")
	presets, err := env.Repo.GetProjectPresetsJSON(ctx, project)
	require.NoError(t, err)
	require.Nil(t, presets)

	mustExec(t, env, `UPDATE project_configs SET project_workflows_json = '[{"name":"next"}]', project_presets_json = '[]' WHERE project_id = $1`, project)
	v, err := env.Repo.GetProjectWorkflowsJSON(ctx, project)
	require.NoError(t, err)
	require.Equal(t, `[{"name":"next"}]`, *v)
	presets, err = env.Repo.GetProjectPresetsJSON(ctx, project)
	require.NoError(t, err)
	require.Equal(t, `[]`, *presets)

	mustExec(t, env, `DELETE FROM project_configs WHERE project_id = $1`, project)
	_, err = env.Repo.GetProjectWorkflowsJSON(ctx, project)
	require.True(t, svcerr.IsNotFound(err), "a deleted record: %v", err)
}
