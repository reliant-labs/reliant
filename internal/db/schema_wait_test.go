// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// The single-migrator rule (see MigrationPolicy) is only safe because the
// non-migrating processes wait for a schema that is actually current. These
// tests cover what "current" means: every embedded migration recorded applied,
// not merely the newest one.

func TestMissingMigrationVersions_FullyMigratedDBHasNone(t *testing.T) {
	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	want, err := embeddedMigrationVersions()
	if err != nil {
		t.Fatalf("embeddedMigrationVersions: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("expected embedded migrations, got none")
	}

	missing, err := missingMigrationVersions(rawDB, want)
	if err != nil {
		t.Fatalf("missingMigrationVersions: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("test DB is migrated, expected no missing versions, got %v", missing)
	}
}

// A migration this binary embeds but the DB has never applied must be reported
// missing even though it sorts *below* the newest applied version — that is the
// case a max(version_id) comparison gets wrong, and goose.WithAllowMissing()
// makes it reachable in practice when branches land out of order.
func TestMissingMigrationVersions_DetectsOlderUnappliedVersion(t *testing.T) {
	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	want, err := embeddedMigrationVersions()
	if err != nil {
		t.Fatalf("embeddedMigrationVersions: %v", err)
	}

	// Sorts below every real migration, so it can only be found by set
	// membership, never by comparing against the highest applied version.
	const unapplied int64 = 1
	augmented := append([]int64{unapplied}, want...)

	missing, err := missingMigrationVersions(rawDB, augmented)
	if err != nil {
		t.Fatalf("missingMigrationVersions: %v", err)
	}
	if len(missing) != 1 || missing[0] != unapplied {
		t.Fatalf("expected exactly [%d] missing, got %v", unapplied, missing)
	}
}

func TestWaitForSchema_ReturnsImmediatelyWhenCurrent(t *testing.T) {
	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	t.Setenv("RELIANT_DB_SCHEMA_WAIT_SECONDS", "5")

	start := time.Now()
	if err := WaitForSchema(rawDB); err != nil {
		t.Fatalf("WaitForSchema on a migrated DB: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("WaitForSchema polled instead of returning immediately (took %s)", elapsed)
	}
}

// A waiter pointed at a database no migrator has touched must fail with a
// message naming the owner, rather than proceeding onto a schema that isn't
// there. Restricting search_path to pg_temp hides goose's bookkeeping table
// from to_regclass, putting the connection in the same state as a database
// nobody has migrated — without needing to create and drop one.
func TestWaitForSchema_TimesOutWhenNoMigratorRuns(t *testing.T) {
	_, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	blind := singleConnCopy(t, rawDB)
	if _, err := blind.Exec(`SET search_path TO pg_temp`); err != nil {
		t.Fatalf("restrict search_path: %v", err)
	}

	// Confirm the setup actually hides the table; otherwise this test would
	// pass for the wrong reason if it stopped taking effect.
	var visible bool
	if err := blind.QueryRow(`SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&visible); err != nil {
		t.Fatalf("probe goose table visibility: %v", err)
	}
	if visible {
		t.Fatal("expected goose_db_version to be hidden by the restricted search_path")
	}

	t.Setenv("RELIANT_DB_SCHEMA_WAIT_SECONDS", "1")

	err := WaitForSchema(blind)
	if err == nil {
		t.Fatal("expected WaitForSchema to time out against an unmigrated database")
	}
	if !strings.Contains(err.Error(), "api-server") {
		t.Fatalf("timeout error should name the schema owner, got: %v", err)
	}
}

// singleConnCopy opens a second pool against the same test database, pinned to
// one connection so a session-level SET applies to every subsequent query on it.
func singleConnCopy(t *testing.T, _ *sql.DB) *sql.DB {
	t.Helper()

	baseDSN := os.Getenv("DATABASE_URL")
	if baseDSN == "" {
		t.Skip("DATABASE_URL not set, skipping database test")
	}

	dsn, err := ensurePkgTestDB(baseDSN)
	if err != nil {
		t.Fatalf("resolve package test database: %v", err)
	}

	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}
