// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupFixture: a project of org "acme" with a checkout record on a
// machine of org "globex" — what the machine-org cleanup removes.
type cleanupFixture struct {
	repo                   *Repo
	raw                    *sql.DB
	dsn                    string
	ctx                    context.Context
	user, project, machine string
	path                   string
}

func newCleanupFixture(t *testing.T) (*cleanupFixture, func(query string, args ...any)) {
	t.Helper()
	repo, raw, dsn, cleanup := setupTestRepoWithDSN(t)
	t.Cleanup(cleanup)
	f := &cleanupFixture{repo: repo, raw: raw, dsn: dsn, ctx: context.Background(), user: "u-" + uuid.NewString(),
		project: uuid.NewString(), machine: "m-" + uuid.NewString(), path: "/home/workspace/projects/p-" + uuid.NewString()}
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	acme := "acme"
	require.NoError(t, repo.UpsertDaemon(f.ctx, &Daemon{ID: f.machine, UserID: f.user}))
	require.NoError(t, repo.CreateProject(f.ctx, &Project{ID: f.project, UserID: f.user, Name: "p", Path: f.path, OrgID: &acme,
		CreatedAt: at, UpdatedAt: at, LastActive: at}))
	require.NoError(t, repo.UpsertProjectDaemon(f.ctx, f.project, f.machine, f.path, nil))
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := raw.ExecContext(f.ctx, query, args...)
		require.NoError(t, err)
	}
	return f, exec
}

func (f *cleanupFixture) recorded(t *testing.T) (checkouts, audits int) {
	t.Helper()
	require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM project_daemons WHERE project_id = ?`, f.project).Scan(f.ctx, &checkouts))
	require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT count(*) FROM project_daemon_org_cleanups WHERE project_id = ?`, f.project).Scan(f.ctx, &audits))
	return checkouts, audits
}

// The cleanup removes a checkout of a project still in another org than the
// machine's, recording it whole; it keeps one whose project is in the
// machine's org, or has no org yet, whatever the caller read earlier.
func TestDetachProjectDaemonOfOtherOrg_DecidesOnTheProjectsCurrentOrg(t *testing.T) {
	f, exec := newCleanupFixture(t)

	// Moved into the machine's org since the reconcile listed it.
	exec(`UPDATE projects SET org_id = 'globex' WHERE id = $1`, f.project)
	gone, err := f.repo.DetachProjectDaemonOfOtherOrg(f.ctx, f.project, f.machine, f.user, "globex")
	require.NoError(t, err)
	assert.False(t, gone)
	// No org yet: its owner's default org's, never deleted over.
	exec(`UPDATE projects SET org_id = NULL WHERE id = $1`, f.project)
	gone, err = f.repo.DetachProjectDaemonOfOtherOrg(f.ctx, f.project, f.machine, f.user, "globex")
	require.NoError(t, err)
	assert.False(t, gone)
	checkouts, audits := f.recorded(t)
	assert.Equal(t, 1, checkouts)
	assert.Zero(t, audits)

	// Still another org's: removed and recorded, once.
	exec(`UPDATE projects SET org_id = 'acme' WHERE id = $1`, f.project)
	for range 2 {
		_, err = f.repo.DetachProjectDaemonOfOtherOrg(f.ctx, f.project, f.machine, f.user, "globex")
		require.NoError(t, err)
	}
	checkouts, audits = f.recorded(t)
	assert.Zero(t, checkouts)
	assert.Equal(t, 1, audits)
	var row struct {
		Path       string  `bun:"path"`
		ProjectOrg *string `bun:"project_org_id"`
		HasOrgKey  bool    `bun:"has_org_key"`
	}
	require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT detached_row->>'path' AS path, project_org_id, jsonb_exists(detached_row, 'project_org_id') AS has_org_key
		FROM project_daemon_org_cleanups WHERE project_id = ?`, f.project).Scan(f.ctx, &row))
	assert.Equal(t, f.path, row.Path)
	require.NotNil(t, row.ProjectOrg)
	assert.Equal(t, "acme", *row.ProjectOrg)
	assert.False(t, row.HasOrgKey, "the recorded row is the project_daemons row alone, restorable as is")
}

// Review B2 (round 3), interleaved: a project org move holding the project
// row (FOR NO KEY UPDATE, as MoveProjectOrg takes it) moves the project into
// the machine's org while the cleanup runs. The cleanup waits for the move
// and keeps the checkout: it never deletes on the org it read before.
func TestDetachProjectDaemonOfOtherOrg_WaitsForAConcurrentMoveIntoTheMachinesOrg(t *testing.T) {
	f, _ := newCleanupFixture(t)
	// The move runs on a connection of its own: the test pool may hold one.
	mover, err := sql.Open("pgx", f.dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mover.Close() })
	tx, err := mover.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(f.ctx, `SELECT 1 FROM projects WHERE id = $1 FOR NO KEY UPDATE`, f.project)
	require.NoError(t, err)

	type result struct {
		gone bool
		err  error
	}
	done := make(chan result, 1)
	go func() {
		gone, err := f.repo.DetachProjectDaemonOfOtherOrg(f.ctx, f.project, f.machine, f.user, "globex")
		done <- result{gone, err}
	}()

	// The cleanup is blocked on the move's row lock: wait until Postgres
	// says so, then commit the move.
	require.Eventually(t, func() bool {
		// Asked on the move's own connection, so a small pool cannot
		// starve the probe. A row-lock wait is a "Lock" wait event.
		var waiting int
		_ = tx.QueryRowContext(f.ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting)
		select {
		case r := <-done:
			t.Fatalf("the cleanup finished without waiting on the move's lock: gone=%v err=%v", r.gone, r.err)
		default:
		}
		return waiting > 0
	}, 30*time.Second, 20*time.Millisecond, "the cleanup never waited on the move's lock")
	_, err = tx.ExecContext(f.ctx, `UPDATE projects SET org_id = 'globex' WHERE id = $1`, f.project)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.False(t, r.gone, "a project moved into the machine's org lost its checkout")
	case <-time.After(30 * time.Second):
		t.Fatal("the cleanup never finished")
	}
	checkouts, audits := f.recorded(t)
	assert.Equal(t, 1, checkouts)
	assert.Zero(t, audits)
}
