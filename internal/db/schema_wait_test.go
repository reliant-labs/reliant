// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/migratekit"
)

// The single-migrator rule (see MigrationPolicy) is only safe because the
// non-migrating processes wait for a schema that is actually current. These
// tests cover what "current" means: a clean recorded version at or past the
// newest embedded migration.

func TestPendingVersions(t *testing.T) {
	t.Parallel()

	want := []int64{10, 20, 30}
	for _, tc := range []struct {
		name  string
		state migratekit.State
		want  []int64
	}{
		{"nothing recorded: everything is pending", migratekit.State{}, []int64{10, 20, 30}},
		{"at the newest: current", migratekit.State{Version: 30, Applied: true}, nil},
		{"behind: the later versions", migratekit.State{Version: 10, Applied: true}, []int64{20, 30}},
		// golang-migrate marks a version dirty while it runs it, so a waiter
		// sees this on every healthy migration and must keep waiting.
		{"dirty: the version being applied is pending", migratekit.State{Version: 30, Dirty: true, Applied: true}, []int64{30}},
		{"ahead of this binary (an older release mid-deploy): current", migratekit.State{Version: 40, Applied: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pendingVersions(tc.state, want); !slices.Equal(got, tc.want) {
				t.Fatalf("pendingVersions(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

func TestPendingMigrations_FullyMigratedDBHasNone(t *testing.T) {
	t.Parallel()

	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	pending, err := PendingMigrations(rawDB)
	if err != nil {
		t.Fatalf("PendingMigrations: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("test DB is migrated, expected no pending versions, got %v", pending)
	}
}

func TestWaitForSchema_ReturnsImmediatelyWhenCurrent(t *testing.T) {
	t.Parallel()

	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	start := time.Now()
	if err := WaitForSchema(rawDB, 5*time.Second); err != nil {
		t.Fatalf("WaitForSchema on a migrated DB: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("WaitForSchema polled instead of returning immediately (took %s)", elapsed)
	}
}

// A waiter pointed at a database no migrator has touched must fail with a
// message naming the owner, rather than proceeding onto a schema that isn't
// there. Restricting search_path to pg_temp hides the migrator's version table
// from to_regclass, putting the connection in the same state as a database
// nobody has migrated — without needing to create and drop one.
func TestWaitForSchema_TimesOutWhenNoMigratorRuns(t *testing.T) {
	t.Parallel()

	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	blind := singleConnCopy(t, rawDB)
	if _, err := blind.Exec(`SET search_path TO pg_temp`); err != nil {
		t.Fatalf("restrict search_path: %v", err)
	}

	// Confirm the setup actually hides the table; otherwise this test would
	// pass for the wrong reason if it stopped taking effect.
	var visible bool
	if err := blind.QueryRow(`SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&visible); err != nil {
		t.Fatalf("probe version table visibility: %v", err)
	}
	if visible {
		t.Fatal("expected schema_migrations to be hidden by the restricted search_path")
	}

	err := WaitForSchema(blind, time.Second)
	if err == nil {
		t.Fatal("expected WaitForSchema to time out against an unmigrated database")
	}
	if !strings.Contains(err.Error(), "api-server") {
		t.Fatalf("timeout error should name the schema owner, got: %v", err)
	}
}

// singleConnCopy opens a second pool against the SAME database the given test
// pool is connected to, pinned to one connection so a session-level SET applies
// to every subsequent query on it.
//
// The DSN is read back from the live connection rather than recomputed: each
// test now owns a private database, so there is no package-wide DSN to look up,
// and pointing this pool anywhere else would silently test a different database.
func singleConnCopy(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()

	conn, err := sql.Open("pgx", dsnForOpenDB(t, db))
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// dsnForOpenDB rebuilds a DSN addressing the database that db is connected to,
// by asking the server which database that is.
func dsnForOpenDB(t *testing.T, db *sql.DB) string {
	t.Helper()

	dsn, err := testDSNOf(db)
	if err != nil {
		t.Fatalf("resolve current database: %v", err)
	}
	return dsn
}
