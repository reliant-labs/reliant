// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The narrow catalog reads answer exactly what the whole-row read answers for
// the one column each selects, including a machine that synced nothing (NULL)
// and one that never synced the project.
func TestProjectMachineCatalogColumnReadsMatchTheRow(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	project := storetest.SeedProjectID
	at := time.Date(2026, 11, 18, 20, 20, 0, 0, time.UTC)
	for _, d := range []string{"d-full", "d-empty", "d-never"} {
		mustExec(t, env, `INSERT INTO daemons (id, user_id, created_at, updated_at) VALUES ($1, $2, NOW(), NOW())`, d, storetest.SeedUserID)
	}
	workflows, presets, removed, states := `[{"name":"release"}]`, `[{"name":"fast"}]`, `[{"kind":"workflow","name":"old"}]`, `[{"repo":"control-plane"}]`
	require.NoError(t, store.UpsertProjectMachineCatalog(ctx, env.ORM, &store.ProjectMachineCatalog{
		ProjectID: project, DaemonID: "d-full", WorkflowsJSON: &workflows, PresetsJSON: &presets, RemovedJSON: &removed,
		PushedAt: at, UpdatedAt: at,
	}))
	require.NoError(t, store.SetProjectMachineRepoStates(ctx, env.ORM, project, "d-full", &states))
	require.NoError(t, store.UpsertProjectMachineCatalog(ctx, env.ORM, &store.ProjectMachineCatalog{
		ProjectID: project, DaemonID: "d-empty", PushedAt: at.Add(time.Minute), UpdatedAt: at,
	}))

	var listed []*store.MachineWorkflowFiles
	for _, daemonID := range []string{"d-full", "d-empty", "d-never"} {
		row, err := store.GetProjectMachineCatalog(ctx, env.ORM, project, daemonID)
		require.NoError(t, err)

		wf, wfFound, err := store.GetProjectMachineWorkflowsJSON(ctx, env.ORM, project, daemonID)
		require.NoError(t, err)
		pr, prFound, err := store.GetProjectMachinePresetsJSON(ctx, env.ORM, project, daemonID)
		require.NoError(t, err)
		pushed, pushedFound, err := store.GetProjectMachineCatalogPushedAt(ctx, env.ORM, project, daemonID)
		require.NoError(t, err)

		require.Equal(t, row != nil, wfFound, daemonID)
		require.Equal(t, row != nil, prFound, daemonID)
		require.Equal(t, row != nil, pushedFound, daemonID)
		if row == nil {
			require.Nil(t, wf)
			require.Nil(t, pr)
			continue
		}
		require.Equal(t, row.WorkflowsJSON, wf, daemonID)
		require.Equal(t, row.PresetsJSON, pr, daemonID)
		require.True(t, row.PushedAt.Equal(pushed), daemonID)
		listed = append(listed, &store.MachineWorkflowFiles{
			DaemonID: daemonID, WorkflowsJSON: row.WorkflowsJSON, RemovedJSON: row.RemovedJSON, RepoStatesJSON: row.RepoStatesJSON,
		})
	}

	files, err := store.ListProjectMachineWorkflowFiles(ctx, env.ORM, project)
	require.NoError(t, err)
	// Ordered by daemon id, as ListProjectMachineCatalogs is.
	require.Equal(t, []*store.MachineWorkflowFiles{listed[1], listed[0]}, files)
}

// ListInbox's two firing reads send their values as bind parameters, so each
// is ONE prepared statement whatever the user, the triggers or the cap. With
// the trigger ids formatted into the text (pgdialect.Array), every distinct
// set of ids was a new statement to parse and plan.
func TestInboxTriggerFiringReadsBindTheirValues(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	// One connection, so every statement this test prepares is listed in
	// the one session's pg_prepared_statements.
	env.SQL.SetMaxOpenConns(1)
	ctx := context.Background()

	for _, call := range []struct {
		user string
		ids  []string
		per  int
	}{
		{"user-a", []string{"tr-bind-1"}, 10},
		{"user-b", []string{"tr-bind-2", "tr-bind-3"}, 250},
		{"user-a", []string{"tr-bind-4", "tr-bind-5", "tr-bind-6"}, 7},
	} {
		_, err := store.FiringsSinceLastSuccess(ctx, env.ORM, call.user, call.ids, call.per)
		require.NoError(t, err)
		_, err = store.RecentTriggerOutcomes(ctx, env.ORM, call.user, call.ids, call.per)
		require.NoError(t, err)
	}

	// Both statements, and only they, read the trigger ids through unnest.
	const both, sinceOnly = "FROM (SELECT DISTINCT unnest(", "SELECT w.occurred_at, w.id"
	recent, since := store.InboxTriggerFiringsSQL()
	require.Contains(t, recent, both)
	require.Contains(t, since, both)
	require.Contains(t, since, sinceOnly)
	require.NotContains(t, recent, sinceOnly)

	rows, err := env.SQL.QueryContext(ctx, `SELECT statement FROM pg_prepared_statements WHERE strpos(statement, $1) > 0`, both)
	require.NoError(t, err)
	var statements []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		statements = append(statements, s)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	require.Len(t, statements, 2, "one prepared statement per read for every call, got:\n%s", strings.Join(statements, "\n---\n"))
	require.NotEqual(t, strings.Contains(statements[0], sinceOnly), strings.Contains(statements[1], sinceOnly), "one of each read")
	for _, s := range statements {
		require.NotContains(t, s, "tr-bind-", "the trigger ids are parameters")
		require.NotContains(t, s, "user-", "the user is a parameter")
	}
}
