package migratekit

import (
	"context"
	"database/sql"
	"maps"
	"testing"
	"time"
)

// A RUN NEEDS ONE SESSION. AutoMigrate borrows a connection from the server's
// own pool, and a desktop or monolith build running embedded postgres may cap
// that pool at ONE. AutoMigrate used to hold its connection (the run lock and
// golang-migrate's driver) and then ask the same pool for a second one for
// every bookkeeping statement (the applied and compatibility records, the
// goose adoption), so at MaxOpenConns=1 it waited forever for a connection it
// was itself holding. Found when a test hung for ten minutes.
//
// Every case below runs under a hard deadline, so a regression fails in
// seconds instead of hanging the package.

// oneConnDeadline bounds one migratekit call on a one-connection pool. A
// healthy run takes well under a second; a deadlocked one never returns.
const oneConnDeadline = 30 * time.Second

// onePool is a pool capped at one connection, as a desktop build hands it to
// AutoMigrate.
func onePool(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db := openRecordDB(t, dsn)
	db.SetMaxOpenConns(1)
	return db
}

// withinDeadline runs fn and fails the test if it has not returned by the
// deadline. A deadlocked call blocks with no context to cancel, so it is run
// in a goroutine; the test's cleanup closes the pool it is blocked on, which
// releases it.
func withinDeadline(t *testing.T, what string, fn func() error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), oneConnDeadline)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatalf("%s still running after %s: migratekit is waiting for a database session it cannot get — a run must need only the one it holds", what, oneConnDeadline)
		return nil
	}
}

func autoMigrateOnOneConn(t *testing.T, files map[string]string, db *sql.DB) error {
	t.Helper()
	err := withinDeadline(t, "AutoMigrate", func() error {
		return AutoMigrate(release(files), "migrations")(db, nil)
	})
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Errorf("%d connection(s) still checked out after AutoMigrate returned; want 0", inUse)
	}
	return err
}

func TestAutoMigrateRunsOnAOneConnectionPool(t *testing.T) {
	t.Parallel()

	t.Run("applies, then boots again with nothing to apply", func(t *testing.T) {
		t.Parallel()
		db := onePool(t, requirePG(t))
		for _, boot := range []string{"first boot", "second boot"} {
			if err := autoMigrateOnOneConn(t, olderFiles, db); err != nil {
				t.Fatalf("%s: %v", boot, err)
			}
		}
		if got, want := appliedRecord(t, db), map[uint]string{1: "accounts", 2: "plans"}; !maps.Equal(got, want) {
			t.Errorf("applied record = %v; want %v", got, want)
		}
	})

	t.Run("adopts a goose database", func(t *testing.T) {
		t.Parallel()
		dsn, _ := gooseMigrated(t, gooseHistory)
		db := onePool(t, dsn)
		if err := autoMigrateOnOneConn(t, cutoverRelease, db); err != nil {
			t.Fatalf("AutoMigrate: %v", err)
		}
		if !tableExists(t, db, "orgs") {
			t.Error("add_orgs did not apply after the adoption")
		}
		if got := appliedRecord(t, db); got[20260201000000] != "baseline" {
			t.Errorf("applied record = %v; want the adopted baseline recorded", got)
		}
	})

	t.Run("an older release boots against a compatible schema ahead", func(t *testing.T) {
		t.Parallel()
		db := onePool(t, requirePG(t))
		if err := autoMigrateOnOneConn(t, merged(olderFiles, compatibleThird), db); err != nil {
			t.Fatalf("newer release boot: %v", err)
		}
		if err := autoMigrateOnOneConn(t, olderFiles, db); err != nil {
			t.Fatalf("older release boot: %v", err)
		}
	})

	t.Run("a failed run records what it applied", func(t *testing.T) {
		t.Parallel()
		db := onePool(t, requirePG(t))
		err := autoMigrateOnOneConn(t, merged(olderFiles, brokenThird), db)
		if err == nil {
			t.Fatal("AutoMigrate over a broken migration succeeded")
		}
		if got, want := appliedRecord(t, db), map[uint]string{1: "accounts", 2: "plans"}; !maps.Equal(got, want) {
			t.Errorf("record after a boot that failed at 3 = %v; want %v", got, want)
		}
	})
}

// openOneConn is Open, whose own pool is capped at one connection: a run
// through it must never ask that pool for a second (sessions_pg_test.go pins
// the session count itself).
func openOneConn(t *testing.T, files map[string]string, dsn string) *Migrator {
	t.Helper()
	mg, err := Open(Options{FS: release(files), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = mg.Close() })
	if max := mg.db.Stats().MaxOpenConnections; max != 1 {
		t.Fatalf("Open's pool allows %d connections; want 1", max)
	}
	return mg
}

func upOnOneConn(t *testing.T, mg *Migrator) (res Result, err error) {
	t.Helper()
	err = withinDeadline(t, "Up", func() error {
		var upErr error
		res, upErr = mg.Up()
		return upErr
	})
	return res, err
}

func TestUpAndForceRunOnOneSession(t *testing.T) {
	t.Parallel()

	t.Run("applies", func(t *testing.T) {
		t.Parallel()
		res, err := upOnOneConn(t, openOneConn(t, olderFiles, requirePG(t)))
		if err != nil || !res.Changed || res.After.Version != 2 {
			t.Fatalf("Up = %+v, %v; want 1 and 2 applied", res, err)
		}
	})

	t.Run("adopts a goose database", func(t *testing.T) {
		t.Parallel()
		dsn, _ := gooseMigrated(t, gooseHistory)
		res, err := upOnOneConn(t, openOneConn(t, cutoverRelease, dsn))
		if err != nil || res.Adopted == nil || res.After.Version != 20260202000000 {
			t.Fatalf("Up = %+v, %v; want the baseline adopted and add_orgs applied", res, err)
		}
	})

	t.Run("force recovers a run that failed part-way", func(t *testing.T) {
		t.Parallel()
		dsn := requirePG(t)
		if _, err := upOnOneConn(t, openOneConn(t, merged(olderFiles, brokenThird), dsn)); err == nil {
			t.Fatal("Up over a broken migration succeeded")
		}
		if _, err := openRecordDB(t, dsn).Exec(fixedThird["3_ledger.up.sql"]); err != nil {
			t.Fatal(err)
		}
		mg := openOneConn(t, merged(olderFiles, fixedThird), dsn)
		if err := withinDeadline(t, "Force", func() error { return mg.Force(3) }); err != nil {
			t.Fatalf("force 3: %v", err)
		}
		res, err := upOnOneConn(t, openOneConn(t, merged(merged(olderFiles, fixedThird), fourth), dsn))
		if err != nil || res.After.Version != 4 {
			t.Fatalf("Up after force = %+v, %v; want 4 applied", res, err)
		}
	})
}
