package pgtest_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestNew_RealPostgres boots the shared embedded postgres (or the
// FORGE_TEST_POSTGRES_URL server), creates an isolated database, and
// proves real-postgres DDL the SQLite shadow could never run — a
// schema-qualified table, TIMESTAMPTZ/JSONB/TEXT[] columns, a '::type'
// cast default — applies and round-trips. Skipped under -short: it
// boots a real server (download on first run).
func TestNew_RealPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	defer cleanup()

	stmts := []string{
		`CREATE SCHEMA controlplane`,
		`CREATE TABLE controlplane.widget (
			id BIGSERIAL PRIMARY KEY,
			region TEXT NOT NULL,
			tags TEXT[] NOT NULL DEFAULT '{}'::text[],
			meta JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`INSERT INTO controlplane.widget (region) VALUES ('t1')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM controlplane.widget`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}

// TestBootAfterShutdownReacquires pins the lifecycle contract an in-process
// embedder depends on: Shutdown returns the package to its UNINITIALIZED
// state, so a later New boots a fresh server instead of resurrecting the
// closed one.
//
// This was a SIGSEGV. boot() was guarded by a sync.Once, which stays fired
// forever, while Shutdown nilled shared.baseDB but left the shared pointer in
// place. A second boot therefore returned the gutted struct together with a
// NIL error, and the first `s.baseDB.Exec` in New dereferenced nil:
//
//	database/sql.(*DB).exec(0x0, ...)
//	pgtest.New()  pkg/pgtest/pgtest.go:408
//
// The forge BINARY never reached it — main calls cli.Execute once and Execute
// defers Shutdown, so "after Shutdown" and "after the process exits" are the
// same moment. internal/tierguard breaks that equivalence on purpose by
// driving the pipeline through repeated in-process cli.Execute() calls, and it
// crashed on the second fixture every time.
func TestBootAfterShutdownReacquires(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}

	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("first Ping: %v", err)
	}
	cleanup()

	// The operation that used to poison the package for everyone after it.
	pgtest.Shutdown()

	db2, cleanup2, err := pgtest.New()
	if err != nil {
		t.Fatalf("New after Shutdown must re-acquire, got error: %v", err)
	}
	defer cleanup2()
	if err := db2.Ping(); err != nil {
		t.Fatalf("Ping after re-acquire: %v", err)
	}

	// And the cycle must be repeatable, not merely survivable once.
	pgtest.Shutdown()
	db3, cleanup3, err := pgtest.New()
	if err != nil {
		t.Fatalf("third New: %v", err)
	}
	defer cleanup3()
	if err := db3.Ping(); err != nil {
		t.Fatalf("third Ping: %v", err)
	}
}

// TestCleanupAfterShutdownDoesNotPanic pins the other nil-deref: the cleanup
// closure New returns captured the server STRUCT, so a caller that defers both
// cleanup and Shutdown — the natural order, and what a test with two defers
// writes — ran cleanup after Shutdown had nilled baseDB.
//
// Degrading to a leaked scratch database is correct here; the pool teardown
// drops it. Panicking inside a deferred call is not.
func TestCleanupAfterShutdownDoesNotPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	_, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pgtest.Shutdown()
	cleanup() // must not panic
}
