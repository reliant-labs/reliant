// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
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

// Review round 4: the reconcile's insert (RecordObservedProjectDaemonInScope)
// waits for an org move holding the project row and decides on the org the
// move committed: a project moved out of the machine's org is not recorded.
func TestRecordObservedProjectDaemonInScope_WaitsForAMoveOutOfTheMachinesOrg(t *testing.T) {
	f, exec := newCleanupFixture(t)
	exec(`DELETE FROM project_daemons WHERE project_id = $1`, f.project)
	exec(`UPDATE projects SET org_id = 'globex' WHERE id = $1`, f.project) // in the machine's org when listed
	scope := &core.OrgScope{OrgID: "globex"}

	mover, err := sql.Open("pgx", f.dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mover.Close() })
	tx, err := mover.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(f.ctx, `SELECT 1 FROM projects WHERE id = $1 FOR NO KEY UPDATE`, f.project)
	require.NoError(t, err)
	_, err = tx.ExecContext(f.ctx, `UPDATE projects SET org_id = 'acme' WHERE id = $1`, f.project)
	require.NoError(t, err)

	type result struct {
		recorded bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		recorded, err := f.repo.RecordObservedProjectDaemonInScope(f.ctx, f.project, f.machine, f.path, nil, scope)
		done <- result{recorded, err}
	}()
	require.Eventually(t, func() bool {
		select {
		case r := <-done:
			t.Fatalf("the insert finished without waiting on the move's lock: recorded=%v err=%v", r.recorded, r.err)
		default:
		}
		var waiting int
		_ = tx.QueryRowContext(f.ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting)
		return waiting > 0
	}, 30*time.Second, 20*time.Millisecond, "the insert never waited on the move's lock")
	require.NoError(t, tx.Commit())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.False(t, r.recorded, "recorded a project the move took out of the machine's org")
	case <-time.After(30 * time.Second):
		t.Fatal("the insert never finished")
	}
	checkouts, _ := f.recorded(t)
	assert.Zero(t, checkouts)

	// In scope again (moved back): recorded.
	exec(`UPDATE projects SET org_id = 'globex' WHERE id = $1`, f.project)
	recorded, err := f.repo.RecordObservedProjectDaemonInScope(f.ctx, f.project, f.machine, f.path, nil, scope)
	require.NoError(t, err)
	assert.True(t, recorded)
}

// whileAMoveOutHoldsTheProject runs write while an org move holding the
// project row (FOR NO KEY UPDATE, as MoveProjectOrg takes it) moves the
// project to toOrg, and commits the move only once write is waiting on the
// row lock. It returns write's result, which must decide on the moved org.
func (f *cleanupFixture) whileAMoveHoldsTheProject(t *testing.T, toOrg string, write func() (bool, error)) bool {
	t.Helper()
	mover, err := sql.Open("pgx", f.dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mover.Close() })
	tx, err := mover.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(f.ctx, `SELECT 1 FROM projects WHERE id = $1 FOR NO KEY UPDATE`, f.project)
	require.NoError(t, err)
	_, err = tx.ExecContext(f.ctx, `UPDATE projects SET org_id = $1 WHERE id = $2`, toOrg, f.project)
	require.NoError(t, err)

	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ok, err := write()
		done <- result{ok, err}
	}()
	require.Eventually(t, func() bool {
		select {
		case r := <-done:
			t.Fatalf("the write finished without waiting on the move's lock: %v, %v", r.ok, r.err)
		default:
		}
		// Asked on the move's own connection, so a small pool cannot
		// starve the probe. A row-lock wait is a "Lock" wait event.
		var waiting int
		_ = tx.QueryRowContext(f.ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()`).Scan(&waiting)
		return waiting > 0
	}, 30*time.Second, 20*time.Millisecond, "the write never waited on the move's lock")
	require.NoError(t, tx.Commit())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		return r.ok
	case <-time.After(30 * time.Second):
		t.Fatal("the write never finished")
		return false
	}
}

// installRow is the machine's checkout row of the project, as stored.
func (f *cleanupFixture) installRow(t *testing.T) (state, installErr string) {
	t.Helper()
	var row struct {
		State string `bun:"install_state"`
		Err   string `bun:"install_error"`
	}
	require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT install_state, install_error FROM project_daemons WHERE project_id = ? AND daemon_id = ?`,
		f.project, f.machine).Scan(f.ctx, &row))
	return row.State, row.Err
}

// Review round 5: every other write the machine's own evidence makes to its
// checkout row — settling a clone as installed (a lost clone's directory, or
// the filesystem announcement), settling a lost clone as failed, recording a
// pool member's failed restore — waits for an org move holding the project
// row, and writes nothing once the project is out of the machine's org. In
// scope again, each writes.
func TestMachineObservedCheckoutWrites_WaitForAMoveOutOfTheMachinesOrg(t *testing.T) {
	scope := &core.OrgScope{OrgID: "globex"}
	for _, tc := range []struct {
		name string
		// seed is the row's state before the write; write is the write
		// under test; then is the row's state after a write in scope.
		seed      string
		write     func(f *cleanupFixture) (bool, error)
		thenState string
	}{
		{
			name: "installed",
			seed: `UPDATE project_daemons SET install_state = 'installing', install_request_id = 'req-1' WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.MarkProjectDaemonInstalledInScope(f.ctx, f.project, f.machine, scope)
			},
			thenState: string(core.ProjectInstallInstalled),
		},
		{
			name: "lost clone failed",
			seed: `UPDATE project_daemons SET install_state = 'installing', install_request_id = 'req-1' WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.FailProjectDaemonInstallInScope(f.ctx, f.project, f.machine, "lost", scope)
			},
			thenState: string(core.ProjectInstallFailed),
		},
		{
			name: "restore failed",
			seed: `UPDATE project_daemons SET install_state = 'installed' WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.RecordFailedProjectDaemonRestoreInScope(f.ctx, f.project, f.machine, f.path, nil, "restore failed", scope)
			},
			thenState: string(core.ProjectInstallFailed),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, exec := newCleanupFixture(t)
			exec(tc.seed, f.project)
			exec(`UPDATE projects SET org_id = 'globex' WHERE id = $1`, f.project) // in the machine's org when read
			before, _ := f.installRow(t)

			wrote := f.whileAMoveHoldsTheProject(t, "acme", func() (bool, error) { return tc.write(f) })
			assert.False(t, wrote, "wrote the checkout of a project the move took out of the machine's org")
			state, _ := f.installRow(t)
			assert.Equal(t, before, state, "the row changed after the project left the machine's org")

			// In scope again (moved back): written.
			exec(`UPDATE projects SET org_id = 'globex' WHERE id = $1`, f.project)
			wrote, err := tc.write(f)
			require.NoError(t, err)
			assert.True(t, wrote)
			state, _ = f.installRow(t)
			assert.Equal(t, tc.thenState, state)
		})
	}
}

// A pool member's failed restore never records over the user's detach of
// the project from the machine, and never ends it: the restore is the
// platform's doing (the write it replaced, UpsertQueuedProjectDaemon, is the
// user's and cleared the detachment).
func TestRecordFailedProjectDaemonRestoreInScope_LeavesADetachedPairAlone(t *testing.T) {
	f, _ := newCleanupFixture(t)
	require.NoError(t, f.repo.DetachProjectFromDaemon(f.ctx, f.project, f.machine, f.path, nil))

	recorded, err := f.repo.RecordFailedProjectDaemonRestoreInScope(f.ctx, f.project, f.machine, f.path, nil, "restore failed", nil)
	require.NoError(t, err)
	assert.False(t, recorded)
	checkouts, _ := f.recorded(t)
	assert.Zero(t, checkouts)
	detached, err := f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.machine)
	require.NoError(t, err)
	assert.Len(t, detached, 1, "the detachment stands")
}
