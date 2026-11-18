package dbgrants

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// The read-only role's setting (default_transaction_read_only) lives in a
// cluster-wide catalog, so the migrators of every database on one server write
// the same row. Before the script checked for it first, two databases migrating
// at once failed one run with "tuple concurrently updated" (XX000): control-
// plane's parallel integration databases, and prod, where two products share a
// Cloud SQL instance and migrate in the same deploy.
//
// N databases on the one shared test server apply the script at the same
// instant, round after round. The first round is the first-time set (the role
// has no setting yet); every later round is the steady state, which is the one
// that failed in practice.
func TestApplyIsSafeUnderConcurrentRunsInDatabasesOnOneServer(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres and opens several databases; runs in task test")
	}
	const (
		databases = 8
		rounds    = 25
	)
	ctx := context.Background()
	dbs := make([]*sql.DB, databases)
	for i := range dbs {
		db, cleanup, err := pgtest.New()
		if err != nil {
			t.Fatalf("test postgres: %v", err)
		}
		t.Cleanup(cleanup)
		if _, err := db.ExecContext(ctx, convergeSchemaSQL); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
		dbs[i] = db
	}
	role := uniqueRole("forge_cc")
	if _, err := dbs[0].ExecContext(ctx, `CREATE ROLE `+role+` NOLOGIN`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		for _, db := range dbs {
			_, _ = db.ExecContext(context.Background(), `DROP OWNED BY `+role)
		}
		_, _ = dbs[0].ExecContext(context.Background(), `DROP ROLE IF EXISTS `+role)
	})
	script, err := RenderFor(role, eventsTable(Column{Name: "id"}, Column{Name: "model"}))
	if err != nil {
		t.Fatal(err)
	}
	// One pinned connection per database, opened before the rounds, so the
	// goroutines released together reach the server together.
	conns := make([]*sql.Conn, databases)
	for i, db := range dbs {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		conns[i] = c
	}

	for round := range rounds {
		var (
			wg    sync.WaitGroup
			start = make(chan struct{})
			errs  = make([]error, databases)
		)
		for i, c := range conns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = Apply(ctx, c, script)
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, database %d: %v", round, i, err)
			}
		}
	}

	// The setting is in place, once, cluster-wide.
	var set int
	if err := dbs[0].QueryRowContext(ctx, `
SELECT count(*) FROM pg_db_role_setting s JOIN pg_roles o ON o.oid = s.setrole
 WHERE s.setdatabase = 0 AND o.rolname = $1 AND 'default_transaction_read_only=on' = ANY (s.setconfig)`, role).Scan(&set); err != nil {
		t.Fatal(err)
	}
	if set != 1 {
		t.Errorf("default_transaction_read_only=on is recorded %d times for %s, want once", set, role)
	}
	// And every database converged its own grants.
	for i, db := range dbs {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM information_schema.column_privileges WHERE grantee = $1`, role).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("database %d: %s holds %d column privileges, want 2 (events.id, events.model)", i, role, n)
		}
	}
}

// The first-time race, made deterministic: another server's migrator writes
// the setting after this run checked for it and before this run's write, so
// this run's write loses to a committed one. Whether it loses as an update
// ("tuple concurrently updated", the role already had a settings row) or as an
// insert (a unique violation on the settings index, it had none), the run
// succeeds: the winner set the same value.
func TestApplySucceedsWhenAConcurrentRunSetTheRoleSettingFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		// prior is run before the race; it decides whether the role has a
		// settings row (update) or none (insert).
		prior string
	}{
		{name: "update", prior: "ALTER ROLE %s SET work_mem = '8MB'"},
		{name: "insert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPGFixture(t, convergeSchemaSQL)
			if tc.prior != "" {
				f.exec(fmt.Sprintf(tc.prior, f.role))
			}
			script, err := RenderFor(f.role, eventsTable(Column{Name: "id"}))
			if err != nil {
				t.Fatal(err)
			}
			race := stageLostRace(t, f, script)
			if err := race.winner.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-race.done; err != nil {
				t.Fatalf("a run that lost the race to set the same value failed: %v", err)
			}
		})
	}
}

// lostRace is a run of the script blocked on another transaction's
// uncommitted write of the role setting.
type lostRace struct {
	winner *sql.Tx
	done   <-chan error
	// release unblocks and reaps the run on every exit path: it cancels the
	// run, rolls the winner back (a no-op once committed), waits for the run
	// to return, and only then closes its connection. Closing the connection
	// first would wait on the blocked query, which waits on the winner's
	// rollback: a deadlock that hangs a failing test until the suite timeout.
	release func()
}

// stageLostRace opens a winner transaction that sets the role setting without
// committing, starts Apply on another connection, and returns once that run is
// waiting on the winner's catalog row. Whatever happens, release runs at the
// end of the test.
func stageLostRace(t *testing.T, f *pgFixture, script string) *lostRace {
	t.Helper()
	ctx := context.Background()
	winner, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	loser, err := f.db.Conn(ctx)
	if err != nil {
		_ = winner.Rollback()
		t.Fatal(err)
	}
	applyCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	exited := make(chan struct{})
	started := false
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			_ = winner.Rollback()
			if started {
				<-exited
			}
			_ = loser.Close()
		})
	}
	t.Cleanup(release)
	if _, err := winner.ExecContext(ctx, fmt.Sprintf(`ALTER ROLE %s SET default_transaction_read_only = on`, f.role)); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := loser.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	started = true
	go func() {
		defer close(exited)
		done <- Apply(applyCtx, loser, script)
	}()

	// The loser has checked, found the setting missing (the winner has not
	// committed), and is waiting on the winner's catalog row. The deadline
	// only bounds the poll; the pg_locks observation is what orders the test.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting bool
		if err := f.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted)`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
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
	return &lostRace{winner: winner, done: done, release: release}
}

// A test that fails while the run is still blocked must fail, not hang:
// release, the path every t.Fatal after staging takes, returns with the
// winner neither committed nor rolled back first.
func TestLostRaceReleaseReturnsWhileTheRunIsBlocked(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	script, err := RenderFor(f.role, eventsTable(Column{Name: "id"}))
	if err != nil {
		t.Fatal(err)
	}
	race := stageLostRace(t, f, script)
	released := make(chan struct{})
	go func() {
		race.release()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(30 * time.Second):
		t.Fatal("release did not return while the run was blocked on the winner's write")
	}
}

// The steady state never writes the shared catalog: a role whose setting is
// already in place keeps the very same pg_db_role_setting tuple across a run.
func TestApplyLeavesAnAlreadySetRoleSettingUntouched(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.converge(eventsTable(Column{Name: "id"}))
	ctid := func() string {
		var c string
		if err := f.db.QueryRowContext(context.Background(), `
SELECT s.ctid::text FROM pg_db_role_setting s JOIN pg_roles o ON o.oid = s.setrole
 WHERE s.setdatabase = 0 AND o.rolname = $1`, f.role).Scan(&c); err != nil {
			f.t.Fatalf("read the role setting of %s: %v", f.role, err)
		}
		return c
	}
	before := ctid()
	f.converge(eventsTable(Column{Name: "id"}))
	if after := ctid(); after != before {
		t.Errorf("the run rewrote the cluster-wide role setting (ctid %s -> %s); a set value must not be written again", before, after)
	}
}

// A setting someone changed is put back: the check is on the value, not on the
// row's existence.
func TestApplyRestoresAChangedRoleSetting(t *testing.T) {
	f := newPGFixture(t, convergeSchemaSQL)
	f.exec(fmt.Sprintf(`ALTER ROLE %s SET default_transaction_read_only = off`, f.role))
	f.converge(eventsTable(Column{Name: "id"}))
	var on bool
	if err := f.db.QueryRowContext(context.Background(), `
SELECT 'default_transaction_read_only=on' = ANY (s.setconfig)
  FROM pg_db_role_setting s JOIN pg_roles o ON o.oid = s.setrole
 WHERE s.setdatabase = 0 AND o.rolname = $1`, f.role).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Error("default_transaction_read_only is not on after the run")
	}
}
