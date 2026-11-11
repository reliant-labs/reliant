// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	forgedb "github.com/reliant-labs/reliant/db"
)

// These tests apply the read-only grants script this repository checks in
// (db/grants/readonly_gen.sql with its per-table rows, as embedded and as the
// migrator assembles it) to a migrated reliant database. The script's role
// setting (default_transaction_read_only on forge_ro) is cluster-wide, so the
// migrators of every database on one server write the same catalog row: prod
// migrates reliant beside control-plane on one Cloud SQL instance. A run that
// loses the race to set the same value must succeed, a run in the steady state
// must not write the shared row at all, and any other failure must still fail
// the run.
//
// Roles are cluster-wide and pgtest's server is shared by every test process,
// so each test renames forge_ro in the script to a role of its own.

// embeddedGrantsScript is the checked-in script assembled the way
// migratekit's migrator does: the main file with every row file spliced in, in
// file-name order.
func embeddedGrantsScript(t *testing.T) string {
	t.Helper()
	fsys := forgedb.MigrationsFS
	main, err := fs.ReadFile(fsys, migratekit.GrantsFile)
	require.NoError(t, err)
	names, err := fs.Glob(fsys, path.Join(migratekit.GrantsRowsDir, "*_gen.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, names, "the grants script has no per-table rows")
	rows := make([]string, 0, len(names))
	for _, name := range names {
		row, err := fs.ReadFile(fsys, name)
		require.NoError(t, err)
		rows = append(rows, string(row))
	}
	script, err := dbgrants.Assemble(string(main), rows)
	require.NoError(t, err)
	return script
}

type grantsFixture struct {
	t      *testing.T
	db     *sql.DB
	role   string
	script string
}

// newGrantsFixture is a migrated database, a NOLOGIN role of the test's own,
// and the checked-in script retargeted at that role.
func newGrantsFixture(t *testing.T) *grantsFixture {
	t.Helper()
	_, db := newMigratedTestDB(t)
	role := fmt.Sprintf("reliant_ro_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	script := embeddedGrantsScript(t)
	require.Contains(t, script, dbgrants.RoleName)
	script = strings.ReplaceAll(script, dbgrants.RoleName, role)
	_, err := db.Exec(`CREATE ROLE ` + role + ` NOLOGIN`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP OWNED BY ` + role)
		_, _ = db.Exec(`DROP ROLE IF EXISTS ` + role)
	})
	return &grantsFixture{t: t, db: db, role: role, script: script}
}

func (f *grantsFixture) apply() {
	f.t.Helper()
	require.NoError(f.t, dbgrants.Apply(context.Background(), f.db, f.script))
}

func (f *grantsFixture) exec(q string) {
	f.t.Helper()
	_, err := f.db.Exec(q)
	require.NoError(f.t, err)
}

// settingCtid is the physical location of the role's cluster-wide settings
// row: any write to it moves it.
func (f *grantsFixture) settingCtid() string {
	f.t.Helper()
	var c string
	require.NoError(f.t, f.db.QueryRow(`
SELECT s.ctid::text FROM pg_db_role_setting s JOIN pg_roles o ON o.oid = s.setrole
 WHERE s.setdatabase = 0 AND o.rolname = $1`, f.role).Scan(&c))
	return c
}

func (f *grantsFixture) readOnlyIsSet() bool {
	f.t.Helper()
	var on bool
	require.NoError(f.t, f.db.QueryRow(`
SELECT EXISTS (SELECT 1 FROM pg_db_role_setting s JOIN pg_roles o ON o.oid = s.setrole
 WHERE s.setdatabase = 0 AND o.rolname = $1 AND 'default_transaction_read_only=on' = ANY (s.setconfig))`, f.role).Scan(&on))
	return on
}

func (f *grantsFixture) canRead(table, column string) bool {
	f.t.Helper()
	var ok bool
	require.NoError(f.t, f.db.QueryRow(`SELECT has_column_privilege($1, $2, $3, 'SELECT')`, f.role, table, column).Scan(&ok))
	return ok
}

// The checked-in script converges reliant's schema: a declared column is
// readable, a forge:secret one is not, and the role is read-only.
func TestReadOnlyGrants_ConvergeTheMigratedSchema(t *testing.T) {
	testkit.Slow(t, "a DB-backed test that migrates a database on pgtest's Postgres")
	t.Parallel()

	f := newGrantsFixture(t)
	f.apply()
	require.True(t, f.canRead("public.connection_secrets", "connection_id"))
	require.False(t, f.canRead("public.connection_secrets", "ciphertext"), "a forge:secret column must be withheld")
	require.True(t, f.readOnlyIsSet())
}

// The steady state never writes the shared catalog: a role whose setting is
// already in place keeps the very same pg_db_role_setting tuple across a run.
func TestReadOnlyGrants_LeaveAnAlreadySetRoleSettingUntouched(t *testing.T) {
	testkit.Slow(t, "a DB-backed test that migrates a database on pgtest's Postgres")
	t.Parallel()

	f := newGrantsFixture(t)
	f.apply()
	before := f.settingCtid()
	f.apply()
	require.Equal(t, before, f.settingCtid(), "the run rewrote the cluster-wide role setting; a set value must not be written again")
}

// A setting someone changed is put back: the check is on the value, not on the
// row's existence.
func TestReadOnlyGrants_RestoreAChangedRoleSetting(t *testing.T) {
	testkit.Slow(t, "a DB-backed test that migrates a database on pgtest's Postgres")
	t.Parallel()

	f := newGrantsFixture(t)
	f.exec(`ALTER ROLE ` + f.role + ` SET default_transaction_read_only = off`)
	f.apply()
	require.True(t, f.readOnlyIsSet())
}

// stagedRun is a run of the script on its own connection, blocked on another
// transaction's uncommitted write of the role setting: it checked, found the
// setting missing, and now waits on the winner's catalog row.
type stagedRun struct {
	winner *sql.Tx
	done   <-chan error
}

// stageBlockedRun opens the winner, starts the run on another connection after
// running setup there, and returns once the run waits on the winner's lock.
// Cleanup cancels the run and rolls the winner back before closing the run's
// connection, so a failing test cannot hang on the blocked query.
func stageBlockedRun(t *testing.T, f *grantsFixture, setup string) *stagedRun {
	t.Helper()
	ctx := context.Background()
	winner, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	loser, err := f.db.Conn(ctx)
	if err != nil {
		_ = winner.Rollback()
		require.NoError(t, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	exited := make(chan struct{})
	started := false
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			cancel()
			_ = winner.Rollback()
			if started {
				<-exited
			}
			_ = loser.Close()
		})
	})
	_, err = winner.ExecContext(ctx, `ALTER ROLE `+f.role+` SET default_transaction_read_only = on`)
	require.NoError(t, err)
	if setup != "" {
		_, err = loser.ExecContext(ctx, setup)
		require.NoError(t, err)
	}
	var pid int
	require.NoError(t, loser.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	started = true
	go func() {
		defer close(exited)
		done <- dbgrants.Apply(runCtx, loser, f.script)
	}()

	// The deadline only bounds the poll; the pg_locks observation orders the test.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting bool
		require.NoError(t, f.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, pid).Scan(&waiting))
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the run finished without waiting on the concurrent write (err=%v); the race was not staged", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never waited on the concurrent write")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &stagedRun{winner: winner, done: done}
}

// Another database's migrator sets the role setting after this run checked
// for it and before this run's write. Whether this run's write then loses as
// an update ("tuple concurrently updated": the role already had a settings
// row) or as an insert (a unique violation on the settings index: it had
// none), the run succeeds, because the winner set the same value.
func TestReadOnlyGrants_SucceedWhenAConcurrentRunSetTheRoleSettingFirst(t *testing.T) {
	testkit.Slow(t, "a DB-backed test that migrates a database on pgtest's Postgres")
	t.Parallel()

	for _, tc := range []struct {
		name string
		// prior decides whether the role has a settings row (update) or none (insert).
		prior string
	}{
		{name: "update", prior: "ALTER ROLE %s SET work_mem = '8MB'"},
		{name: "insert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newGrantsFixture(t)
			if tc.prior != "" {
				f.exec(fmt.Sprintf(tc.prior, f.role))
			}
			run := stageBlockedRun(t, f, "")
			require.NoError(t, run.winner.Commit())
			require.NoError(t, <-run.done, "a run that lost the race to set the same value failed")
			require.True(t, f.readOnlyIsSet())
			require.True(t, f.canRead("public.connection_secrets", "connection_id"), "the run's grants must have committed")
		})
	}
}

// Only the lost race is forgiven: any other failure of the role-setting write
// (here a lock timeout while it waits on the concurrent write) fails the run,
// and the script's one statement leaves none of its grants behind.
func TestReadOnlyGrants_PropagateAnUnrelatedRoleSettingFailure(t *testing.T) {
	testkit.Slow(t, "a DB-backed test that migrates a database on pgtest's Postgres")
	t.Parallel()

	f := newGrantsFixture(t)
	run := stageBlockedRun(t, f, `SET lock_timeout = '200ms'`)
	err := <-run.done
	require.Error(t, err, "a lock timeout on the role setting must fail the run")
	require.Contains(t, err.Error(), "lock timeout")
	require.NoError(t, run.winner.Rollback())
	require.False(t, f.canRead("public.connection_secrets", "connection_id"), "a failed run must leave no grants behind")
	require.False(t, f.readOnlyIsSet())
}
