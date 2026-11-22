// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
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
	gone, err := store.DetachProjectDaemonOfOtherOrg(f.ctx, f.repo.ORM(), f.project, f.machine, f.user, "globex")
	require.NoError(t, err)
	assert.False(t, gone)
	// No org yet: its owner's default org's, never deleted over.
	exec(`UPDATE projects SET org_id = NULL WHERE id = $1`, f.project)
	gone, err = store.DetachProjectDaemonOfOtherOrg(f.ctx, f.repo.ORM(), f.project, f.machine, f.user, "globex")
	require.NoError(t, err)
	assert.False(t, gone)
	checkouts, audits := f.recorded(t)
	assert.Equal(t, 1, checkouts)
	assert.Zero(t, audits)

	// Still another org's: removed and recorded, once.
	exec(`UPDATE projects SET org_id = 'acme' WHERE id = $1`, f.project)
	for range 2 {
		_, err = store.DetachProjectDaemonOfOtherOrg(f.ctx, f.repo.ORM(), f.project, f.machine, f.user, "globex")
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
		gone, err := store.DetachProjectDaemonOfOtherOrg(f.ctx, f.repo.ORM(), f.project, f.machine, f.user, "globex")
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
	return f.whileAWriterHoldsTheProject(t, "FOR NO KEY UPDATE", `UPDATE projects SET org_id = $1 WHERE id = $2`, toOrg, write)
}

// whileADeletionHoldsTheProject is whileAMoveHoldsTheProject for a deletion
// schedule: it holds the project row FOR UPDATE, as
// store.ScheduleProjectDeletion takes it, and sets delete_after.
func (f *cleanupFixture) whileADeletionHoldsTheProject(t *testing.T, write func() (bool, error)) bool {
	t.Helper()
	return f.whileAWriterHoldsTheProject(t, "FOR UPDATE",
		`UPDATE projects SET delete_after = $1, deleted_by = 'owner' WHERE id = $2`, time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC), write)
}

// whileAWriterHoldsTheProject runs write while another transaction holds the
// project row (lock) and has run update(arg, project), and commits that
// transaction only once write is waiting on the row. It returns write's
// result, which must decide on what the other transaction committed.
func (f *cleanupFixture) whileAWriterHoldsTheProject(t *testing.T, lock, update string, arg any, write func() (bool, error)) bool {
	t.Helper()
	mover, err := sql.Open("pgx", f.dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mover.Close() })
	tx, err := mover.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(f.ctx, `SELECT 1 FROM projects WHERE id = $1 `+lock, f.project)
	require.NoError(t, err)
	_, err = tx.ExecContext(f.ctx, update, arg, f.project)
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
	require.NoError(t, store.DetachProjectFromDaemon(f.ctx, f.repo.ORM(), f.project, f.machine, f.path, nil))

	recorded, err := f.repo.RecordFailedProjectDaemonRestoreInScope(f.ctx, f.project, f.machine, f.path, nil, "restore failed", nil)
	require.NoError(t, err)
	assert.False(t, recorded)
	checkouts, _ := f.recorded(t)
	assert.Zero(t, checkouts)
	detached, err := store.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.repo.ORM(), f.machine)
	require.NoError(t, err)
	assert.Len(t, detached, 1, "the detachment stands")
}

// documentedRestoreSQL is the restore statement of
// dev-docs/machine-org-rollout.md, as an operator runs it, with its time
// window opened to every row. BEGIN and COMMIT are dropped: the test runs the
// one statement between them.
func documentedRestoreSQL(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "dev-docs", "machine-org-rollout.md"))
	require.NoError(t, err)
	text := string(doc)
	at := strings.Index(text, "<!-- machine-org-restore-sql:")
	require.GreaterOrEqual(t, at, 0, "the restore block lost its marker")
	text = text[at:]
	start := strings.Index(text, "```sql\n")
	require.GreaterOrEqual(t, start, 0)
	text = text[start+len("```sql\n"):]
	text = text[:strings.Index(text, "```")]
	var stmt []string
	for _, line := range strings.Split(text, "\n") {
		switch strings.TrimSpace(line) {
		case "BEGIN;", "COMMIT;", "":
			continue
		}
		stmt = append(stmt, line)
	}
	require.Contains(t, text, "'<from>'")
	return strings.ReplaceAll(strings.Join(stmt, "\n"), "'<from>'", "'-infinity'")
}

// Review round 6 (B2): the machine-org cleanup detaches as a user's detach
// does. The machine's synced config of the project goes with its checkout
// record, so the project's config union no longer carries the other org's
// machine's workflows, and both are audited whole. The documented restore
// puts both back exactly, and the machine's next push (its org fixed, it
// reconnects) brings its files back into the union.
func TestMachineOrgCleanup_DocumentedRestoreBringsTheCheckoutBack(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	machine, other := p.defaultID, p.cicdID
	_, err := p.repo.ORM().Bun().ExecContext(ctx, `UPDATE projects SET org_id = 'acme' WHERE id = ?`, p.projectID)
	require.NoError(t, err)
	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, machine, "/work/"+machine, nil))
	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, other, "/work/"+other, nil))
	release := config.StoredWorkflow{Slug: "release", Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}
	deploy := config.StoredWorkflow{Slug: "deploy", Name: "deploy", RelativePath: "deploy.yaml", YAMLContent: releaseYAML}
	require.True(t, p.push(t, machine, 1000, []config.StoredWorkflow{release}, nil))
	require.True(t, p.push(t, other, 2000, []config.StoredWorkflow{deploy}, nil))
	paths := func() []string {
		var out []string
		for _, w := range p.unionWorkflows(t) {
			out = append(out, w.RelativePath)
		}
		return out
	}
	require.ElementsMatch(t, []string{p.releasePath, "deploy.yaml"}, paths())
	catalogBefore := machineCatalogJSON(t, p.repo, p.projectID, machine)
	require.NotEmpty(t, catalogBefore)

	// The machine is globex's; the project is acme's.
	gone, err := store.DetachProjectDaemonOfOtherOrg(ctx, p.repo.ORM(), p.projectID, machine, "test-user", "globex")
	require.NoError(t, err)
	require.True(t, gone)

	assert.Equal(t, []string{"deploy.yaml"}, paths(), "the other org's machine's workflows are still in the project's config")
	catalogs, err := store.ProjectMachineCatalogs(ctx, p.repo.ORM(), p.projectID)
	require.NoError(t, err)
	require.Len(t, catalogs, 1)
	assert.Equal(t, other, catalogs[0].DaemonID)
	_, synced, err := p.repo.GetProjectMachineConfigRecord(ctx, p.projectID, machine)
	require.NoError(t, err)
	assert.False(t, synced, "the machine's synced config of the project is still there")
	detached, err := store.ListProjectDaemonDetachmentsForDaemon(ctx, p.repo.ORM(), machine)
	require.NoError(t, err)
	assert.Empty(t, detached, "the cleanup is the platform's, not a user's detach")

	// The documented restore: the checkout and the machine's config, exactly.
	_, err = p.repo.ORM().Bun().ExecContext(ctx, documentedRestoreSQL(t))
	require.NoError(t, err)
	rows, err := p.repo.ListProjectDaemonsForProject(ctx, p.projectID)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "the checkout record is back")
	assert.JSONEq(t, catalogBefore, machineCatalogJSON(t, p.repo, p.projectID, machine), "the machine's config came back other than it was")
	record, synced, err := p.repo.GetProjectMachineConfigRecord(ctx, p.projectID, machine)
	require.NoError(t, err)
	require.True(t, synced)
	assert.Equal(t, "from "+machine, *record.UserConfigYAML)

	// The machine's next push rewrites the union from every machine's.
	require.True(t, p.push(t, machine, 3000, []config.StoredWorkflow{release}, nil))
	assert.ElementsMatch(t, []string{p.releasePath, "deploy.yaml"}, paths())
}

// machineCatalogJSON is the machine's project_machine_catalogs row, whole.
func machineCatalogJSON(t *testing.T, repo *Repo, projectID, daemonID string) string {
	t.Helper()
	var out []string
	require.NoError(t, repo.ORM().Bun().NewRaw(`SELECT to_jsonb(c)::text FROM project_machine_catalogs c WHERE project_id = ? AND daemon_id = ?`,
		projectID, daemonID).Scan(context.Background(), &out))
	if len(out) == 0 {
		return ""
	}
	return out[0]
}

// Review round 7 (B1): a config snapshot the gateway resolved to the project
// before the machine-org cleanup ran, and persists after it, recreates
// neither the machine's catalog nor its share of the union. The store
// re-decides under the project's locks: the project's org is not the
// machine's, and the machine has no checkout record of it any more. Either
// alone drops the snapshot.
func TestSyncProjectConfigSnapshot_ResolvedBeforeTheCleanupPersistsNothingAfterIt(t *testing.T) {
	p := newTwoMachineProject(t)
	ctx := context.Background()
	machine, other := p.defaultID, p.cicdID
	_, err := p.repo.ORM().Bun().ExecContext(ctx, `UPDATE projects SET org_id = 'acme' WHERE id = ?`, p.projectID)
	require.NoError(t, err)
	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, machine, "/work/"+machine, nil))
	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, other, "/work/"+other, nil))
	release := config.StoredWorkflow{Slug: "release", Name: "release", RelativePath: p.releasePath, YAMLContent: releaseYAML}
	deploy := config.StoredWorkflow{Slug: "deploy", Name: "deploy", RelativePath: "deploy.yaml", YAMLContent: releaseYAML}
	require.True(t, p.push(t, other, 1000, []config.StoredWorkflow{deploy}, nil))

	// Resolved: the machine's snapshot of the project, as the gateway built
	// it before the cleanup.
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{release})
	require.NoError(t, err)
	snapshot := func() *ProjectConfigRecord {
		return &ProjectConfigRecord{ProjectID: p.projectID, DaemonID: machine, ProjectWorkflowsJSON: wj,
			UserConfigYAML: testStringPtr("from " + machine), PushedAt: time.UnixMilli(2000).UTC()}
	}

	// The cleanup: the machine is globex's, the project acme's.
	gone, err := store.DetachProjectDaemonOfOtherOrg(ctx, p.repo.ORM(), p.projectID, machine, "test-user", "globex")
	require.NoError(t, err)
	require.True(t, gone)

	// Persisted after it, with the machine's scope: dropped.
	stored, err := p.repo.SyncProjectConfigSnapshot(ctx, snapshot(), false, &core.OrgScope{OrgID: "globex"})
	require.NoError(t, err)
	assert.False(t, stored)
	// With no org to keep to (a deployment with no orgs), the missing
	// checkout record alone drops it.
	stored, err = p.repo.SyncProjectConfigSnapshot(ctx, snapshot(), true, nil)
	require.NoError(t, err)
	assert.False(t, stored)

	assert.Empty(t, machineCatalogJSON(t, p.repo, p.projectID, machine), "the snapshot recreated the machine's catalog")
	var paths []string
	for _, w := range p.unionWorkflows(t) {
		paths = append(paths, w.RelativePath)
	}
	assert.Equal(t, []string{"deploy.yaml"}, paths, "the snapshot put the machine's files back in the union")

	// The machine's own org's project, with its checkout: stored.
	_, err = p.repo.ORM().Bun().ExecContext(ctx, `UPDATE projects SET org_id = 'globex' WHERE id = ?`, p.projectID)
	require.NoError(t, err)
	require.NoError(t, p.repo.UpsertProjectDaemon(ctx, p.projectID, machine, "/work/"+machine, nil))
	stored, err = p.repo.SyncProjectConfigSnapshot(ctx, snapshot(), false, &core.OrgScope{OrgID: "globex"})
	require.NoError(t, err)
	assert.True(t, stored)
}

// Rebased onto #2890: a project scheduled for deletion gets nothing new from
// its machines. Each machine-observed write — recording a found checkout,
// settling a clone installed or failed, recording a failed restore, syncing
// the machine's config — waits for a deletion schedule holding the project
// row (FOR UPDATE, as store.ScheduleProjectDeletion takes it), then writes
// nothing. Restored (delete_after cleared), each writes again.
func TestMachineObservedWrites_RefuseAProjectScheduledForDeletion(t *testing.T) {
	scope := &core.OrgScope{OrgID: "acme"}
	snapshot := func(f *cleanupFixture) *ProjectConfigRecord {
		return &ProjectConfigRecord{ProjectID: f.project, DaemonID: f.machine, UserConfigYAML: testStringPtr("from the machine"),
			PushedAt: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	}
	for _, tc := range []struct {
		name  string
		seed  string
		write func(f *cleanupFixture) (bool, error)
	}{
		{
			name: "found checkout",
			seed: `DELETE FROM project_daemons WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.RecordObservedProjectDaemonInScope(f.ctx, f.project, f.machine, f.path, nil, scope)
			},
		},
		{
			name: "installed",
			seed: `UPDATE project_daemons SET install_state = 'installing', install_request_id = 'req-1' WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.MarkProjectDaemonInstalledInScope(f.ctx, f.project, f.machine, scope)
			},
		},
		{
			name: "lost clone failed",
			seed: `UPDATE project_daemons SET install_state = 'installing', install_request_id = 'req-1' WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.FailProjectDaemonInstallInScope(f.ctx, f.project, f.machine, "lost", scope)
			},
		},
		{
			name: "restore failed",
			seed: `UPDATE project_daemons SET install_state = 'installed' WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.RecordFailedProjectDaemonRestoreInScope(f.ctx, f.project, f.machine, f.path, nil, "restore failed", scope)
			},
		},
		{
			name: "config synced",
			seed: `UPDATE project_daemons SET install_state = 'installed' WHERE project_id = $1`,
			write: func(f *cleanupFixture) (bool, error) {
				return f.repo.SyncProjectConfigSnapshot(f.ctx, snapshot(f), true, scope)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, exec := newCleanupFixture(t)
			exec(tc.seed, f.project)
			before := f.machineState(t)

			wrote := f.whileADeletionHoldsTheProject(t, func() (bool, error) { return tc.write(f) })
			assert.False(t, wrote, "a machine wrote to a project scheduled for deletion")
			assert.Equal(t, before, f.machineState(t), "the machine's rows changed after the project was scheduled for deletion")

			// Restored: written.
			exec(`UPDATE projects SET delete_after = NULL, deleted_by = NULL WHERE id = $1`, f.project)
			wrote, err := tc.write(f)
			require.NoError(t, err)
			assert.True(t, wrote)
		})
	}
}

// machineState is the machine's checkout row of the project and its synced
// config of it, as stored ("" each when absent).
func (f *cleanupFixture) machineState(t *testing.T) [2]string {
	t.Helper()
	var checkout []string
	require.NoError(t, f.repo.ORM().Bun().NewRaw(`SELECT to_jsonb(pd)::text FROM project_daemons pd WHERE project_id = ? AND daemon_id = ?`,
		f.project, f.machine).Scan(f.ctx, &checkout))
	out := [2]string{"", machineCatalogJSON(t, f.repo, f.project, f.machine)}
	if len(checkout) > 0 {
		out[0] = checkout[0]
	}
	return out
}
