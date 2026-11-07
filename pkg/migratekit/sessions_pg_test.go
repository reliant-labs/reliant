package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
)

// ONE SESSION PER RUN, THROUGH EVERY ENTRY POINT. A migration run behind a
// session-mode PgBouncer gets a server connection per client connection, and
// reliant's prod pool has two of them per PgBouncer replica. A Migrator held
// two sessions — the run lock's, and the one golang-migrate's driver opened
// from the DSN — so two `db migrate up` runs at once needed four, and each sat
// on what it had while it waited for the rest. PgBouncer's query_wait_timeout
// killed one after 45s ("open migrator: failed to open database: driver: bad
// connection"). AutoMigrate had already been fixed (#690).
//
// Postgres's per-role CONNECTION LIMIT stands in for the pooler's cap here: a
// session past it is refused outright rather than queued, so a run that needs
// a second session fails fast instead of hanging. The real pooler is
// pgbouncer_integration_test.go.

// sessionCapped returns a DSN for a fresh database that logs in as a role
// allowed n sessions, and an unlimited superuser handle on the same database
// for the test's own queries. Superusers are exempt from CONNECTION LIMIT, so
// the cap only ever binds migratekit's sessions.
func sessionCapped(t *testing.T, n int) (dsn, role string, admin *sql.DB) {
	t.Helper()
	base := requirePG(t)
	admin = openRecordDB(t, base)
	var dbName string
	if err := admin.QueryRow("SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	role = dbName + "_capped"
	for _, s := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'capped' CONNECTION LIMIT %d", role, n),
		fmt.Sprintf("ALTER DATABASE %s OWNER TO %s", dbName, role),
		fmt.Sprintf("GRANT ALL ON SCHEMA public TO %s", role),
	} {
		if _, err := admin.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// Registered after requirePG's cleanup, so it runs first: the role must
	// own nothing, here or in pg_database, before it can be dropped.
	t.Cleanup(func() {
		_, _ = admin.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1", role)
		_, _ = admin.Exec("REASSIGN OWNED BY " + role + " TO CURRENT_USER")
		_, _ = admin.Exec("DROP OWNED BY " + role)
		_, _ = admin.Exec("DROP ROLE IF EXISTS " + role)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "capped")
	return u.String(), role, admin
}

// sessionsOf counts the role's live sessions, and how many of them are waiting
// for the run lock. A waiter polls (runlock.go, A WAITER HOLDS NOTHING), so it
// is mostly idle and invisible to pg_locks: it is a session whose last query
// was the run lock's poll, while the test holds that lock.
func sessionsOf(t *testing.T, admin *sql.DB, role string) (total, waiting int) {
	t.Helper()
	poll := fmt.Sprintf("SELECT pg_try_advisory_lock(%d)", int64(runLockKey))
	err := admin.QueryRow(`SELECT count(*), count(*) FILTER (WHERE query = $2)
		FROM pg_stat_activity WHERE usename = $1`, role, poll).Scan(&total, &waiting)
	if err != nil {
		t.Fatal(err)
	}
	return total, waiting
}

// holdRunLock takes migratekit's run lock on an admin session, as a run in
// progress would, and returns its release.
func holdRunLock(t *testing.T, admin *sql.DB) (release func()) {
	t.Helper()
	conn, err := admin.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lockRun(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			unlockRun(context.Background(), conn)
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// A Migrator is one session from Open to Close, whatever it does in between:
// it works against a server that allows the role exactly one.
func TestAMigratorIsOneSession(t *testing.T) {
	t.Parallel()
	dsn, role, admin := sessionCapped(t, 1)

	mg, err := Open(Options{FS: release(merged(olderFiles, brokenThird)), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("Open on a one-session role: %v", err)
	}
	defer func() { _ = mg.Close() }()
	if total, _ := sessionsOf(t, admin, role); total != 1 {
		t.Errorf("an open Migrator holds %d sessions; want 1", total)
	}
	if _, err := upOnOneConn(t, mg); err == nil {
		t.Fatal("Up over a broken migration succeeded; the test premise no longer holds")
	}
	if st, err := mg.State(); err != nil || st != (State{Version: 3, Dirty: true, Applied: true}) {
		t.Fatalf("State = %+v, %v; want dirty at 3", st, err)
	}
	if _, err := admin.Exec(fixedThird["3_ledger.up.sql"]); err != nil {
		t.Fatal(err)
	}
	if err := withinDeadline(t, "Force", func() error { return mg.Force(3) }); err != nil {
		t.Fatalf("Force(3): %v", err)
	}
	if total, _ := sessionsOf(t, admin, role); total != 1 {
		t.Errorf("after Up and Force the Migrator holds %d sessions; want 1", total)
	}
	if err := mg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForSessions(t, admin, role, 0)

	// The boot path on the same one-session role, through a pool that would
	// happily open more.
	pool := openRecordDB(t, dsn)
	res, err := Apply(context.Background(), pool, release(merged(merged(olderFiles, fixedThird), fourth)), "migrations")
	if err != nil || !res.Changed || res.After.Version != 4 {
		t.Fatalf("Apply on a one-session role = %+v, %v; want 4 applied", res, err)
	}
}

// waitForSessions waits for the role to hold want sessions: a closed
// connection's backend exits asynchronously.
func waitForSessions(t *testing.T, admin *sql.DB, role string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		total, _ := sessionsOf(t, admin, role)
		if total == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("role holds %d sessions; want %d", total, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Concurrent runs behind a two-session cap — the prod pooler's — each wait
// for the run lock on the ONE session they hold, and all finish. Through
// every entry point: Migrator.Up (`db migrate up`), Migrator.Force, and
// AutoMigrate (a server booting with AUTO_MIGRATE).
func TestWaitersHoldOneSessionEach(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(dsn string) error
	}{
		{"db migrate up", func(dsn string) error {
			mg, err := Open(Options{FS: release(olderFiles), Dir: "migrations", DSN: dsn})
			if err != nil {
				return fmt.Errorf("Open: %w", err)
			}
			defer func() { _ = mg.Close() }()
			if _, err := mg.State(); err != nil {
				return fmt.Errorf("State: %w", err)
			}
			_, err = mg.Up()
			return err
		}},
		{"AutoMigrate", func(dsn string) error {
			pool, err := sql.Open("pgx/v5", sqlDSN(dsn))
			if err != nil {
				return err
			}
			defer func() { _ = pool.Close() }()
			return AutoMigrate(release(olderFiles), "migrations")(pool, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dsn, role, admin := sessionCapped(t, 2)
			releaseLock := holdRunLock(t, admin)

			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() { errs <- tc.run(dsn) }()
			}
			// Both runs are now queued behind the held run lock. Each must
			// be waiting on the one session it has.
			waitForWaiters(t, admin, role, 2, errs)
			releaseLock()
			for i := 0; i < 2; i++ {
				if err := withinDeadline(t, tc.name, func() error { return <-errs }); err != nil {
					t.Errorf("run %d: %v", i, err)
				}
			}
			if !tableExists(t, admin, "plans") {
				t.Error("plans missing after both runs")
			}
		})
	}

	// Force waits on its one session too: an open Migrator already holds
	// its session, so two of them fill the cap, and Force must not ask for
	// another while it queues behind the run lock.
	t.Run("Force", func(t *testing.T) {
		t.Parallel()
		dsn, role, admin := sessionCapped(t, 2)
		mustUp(t, release(olderFiles), dsn)
		var mgs []*Migrator
		for i := 0; i < 2; i++ {
			mg, err := Open(Options{FS: release(olderFiles), Dir: "migrations", DSN: dsn})
			if err != nil {
				t.Fatalf("Open %d of 2 behind a two-session cap: %v", i+1, err)
			}
			t.Cleanup(func() { _ = mg.Close() })
			mgs = append(mgs, mg)
		}
		releaseLock := holdRunLock(t, admin)
		errs := make(chan error, 2)
		for _, mg := range mgs {
			go func() { errs <- mg.Force(2) }()
		}
		waitForWaiters(t, admin, role, 2, errs)
		releaseLock()
		for i := 0; i < 2; i++ {
			if err := withinDeadline(t, "Force", func() error { return <-errs }); err != nil {
				t.Errorf("Force %d: %v", i, err)
			}
		}
	})
}

// waitForWaiters waits for want of the role's sessions to block on the run
// lock, and fails if they hold more than one session each, or if a run ends
// (on errs) while the lock is held.
func waitForWaiters(t *testing.T, admin *sql.DB, role string, want int, errs <-chan error) {
	t.Helper()
	deadline := time.Now().Add(oneConnDeadline)
	for {
		total, waiting := sessionsOf(t, admin, role)
		if waiting == want {
			if total != want {
				t.Errorf("%d waiters hold %d sessions; want one each", want, total)
			}
			return
		}
		select {
		case err := <-errs:
			t.Fatalf("a run ended while the run lock was held (sessions=%d, waiting=%d): %v", total, waiting, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %s, %d of %d runs wait on the run lock (%d sessions)", oneConnDeadline, waiting, want, total)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Apply is AutoMigrate's run as a value, so a `db migrate up` that wants the
// boot path's policy can still say what it did.
func TestApplyReportsWhatItDid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("applies, then finds nothing to do", func(t *testing.T) {
		t.Parallel()
		pool := onePool(t, requirePG(t))
		res, err := Apply(ctx, pool, release(olderFiles), "migrations")
		if err != nil || !res.Changed || res.Before.Applied || res.After != (State{Version: 2, Applied: true}) {
			t.Fatalf("first Apply = %+v, %v; want none -> 2", res, err)
		}
		res, err = Apply(ctx, pool, release(olderFiles), "migrations")
		if err != nil || res.Changed || res.After.Version != 2 {
			t.Fatalf("second Apply = %+v, %v; want nothing applied at 2", res, err)
		}
	})

	t.Run("adopts", func(t *testing.T) {
		t.Parallel()
		_, db := gooseMigrated(t, gooseHistory)
		res, err := Apply(ctx, db, release(cutoverRelease), "migrations")
		if err != nil || res.Adopted == nil || res.Adopted.Version != 20260201000000 || !res.Changed || res.After.Version != 20260202000000 {
			t.Fatalf("Apply over a goose database = %+v, %v; want the baseline adopted and add_orgs applied", res, err)
		}
	})

	t.Run("refuses a dirty schema, saying where it is", func(t *testing.T) {
		t.Parallel()
		pool := onePool(t, requirePG(t))
		if _, err := Apply(ctx, pool, release(merged(olderFiles, brokenThird)), "migrations"); err == nil {
			t.Fatal("Apply over a broken migration succeeded")
		}
		res, err := Apply(ctx, pool, release(merged(olderFiles, fixedThird)), "migrations")
		if err == nil || !strings.Contains(err.Error(), "dirty at version 3") {
			t.Fatalf("Apply over a dirty schema err = %v; want the dirty refusal", err)
		}
		if res.Before != (State{Version: 3, Dirty: true, Applied: true}) {
			t.Errorf("refused Apply's Result.Before = %+v; want dirty at 3", res.Before)
		}
	})

	t.Run("embeds nothing", func(t *testing.T) {
		t.Parallel()
		for _, fsys := range []fs.FS{nil, fstest.MapFS{"migrations/.gitkeep": {}}} {
			// A nil pool: nothing embedded must not touch the database.
			res, err := Apply(ctx, nil, fsys, "migrations")
			if err != nil || !res.NothingEmbedded {
				t.Errorf("Apply with no migrations = %+v, %v; want NothingEmbedded", res, err)
			}
		}
	})
}

// A migration that fails says where, as golang-migrate's pgx v5 driver said
// it: the message first, then the line and column, through both entry points.
// The postgres driver the run is now bound to formats only lib/pq's errors
// that way, and printed "migration failed in line 0" over pgx.
func TestAFailedMigrationSaysWhereItFailed(t *testing.T) {
	t.Parallel()
	const want = `migration failed: relation "no_such_table" does not exist (column 13) in line 2:`
	check := func(t *testing.T, err error) {
		t.Helper()
		var dbErr database.Error
		if err == nil || !strings.Contains(err.Error(), want) || !errors.As(err, &dbErr) || dbErr.Line != 2 {
			t.Errorf("failed migration's error = %v; want it to contain %q, as a database.Error at line 2", err, want)
		}
	}
	t.Run("Up", func(t *testing.T) {
		t.Parallel()
		_, err := failedAtThird(t)
		check(t, err)
	})
	t.Run("Apply", func(t *testing.T) {
		t.Parallel()
		_, err := Apply(context.Background(), onePool(t, requirePG(t)), release(merged(olderFiles, brokenThird)), "migrations")
		check(t, err)
	})
}

// golang-migrate's own `x-` options in the DSN keep configuring the run now
// that Open no longer hands the DSN to golang-migrate's URL constructor.
func TestOpenHonoursTheMigratorOptionsInTheDSN(t *testing.T) {
	t.Parallel()
	dsn := requirePG(t)
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	mustUp(t, release(olderFiles), dsn+sep+"x-migrations-table=app_versions&x-statement-timeout=60000")
	db := openRecordDB(t, dsn)
	if !tableExists(t, db, "app_versions") || tableExists(t, db, "schema_migrations") {
		t.Errorf("x-migrations-table=app_versions: app_versions exists=%v, schema_migrations exists=%v; want only app_versions",
			tableExists(t, db, "app_versions"), tableExists(t, db, "schema_migrations"))
	}
}
