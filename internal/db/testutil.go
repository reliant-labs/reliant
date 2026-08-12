// Copyright (c) 2025 Reliant Labs
package db

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Test-database isolation model
// -----------------------------
// The DB-backed tests are written against an EMPTY database: they hardcode IDs
// like "thread-1" and assume nothing else exists. Two facts about how they run
// shape the isolation strategy:
//
//  1. `go test ./...` builds one binary per package and runs those binaries
//     CONCURRENTLY, all pointed at the same DATABASE_URL. If every package
//     shared a single database, a reset in one package would wipe another
//     package's in-flight rows.
//  2. Within a package, tests run sequentially (none use t.Parallel), but they
//     reuse the same constant IDs, so each test needs a clean slate.
//
// So: each test BINARY gets its own database (created + migrated once per
// process, named deterministically from the package's working directory), and
// each SetupTestDB call TRUNCATES + reseeds that database so every test starts
// empty. Migrating once per binary (~1.8s) instead of per test keeps it fast;
// truncation of all tables is a few tens of milliseconds.

var (
	pkgDBOnce sync.Once
	pkgDBDSN  string
	pkgDBErr  error
)

// NewTestRepo creates a test Repo for use in unit tests.
// It requires DATABASE_URL to be set in the environment pointing to a Postgres instance.
// Tests are skipped if DATABASE_URL is not set.
func NewTestRepo(t *testing.T) *Repo {
	t.Helper()
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	return repo
}

// SetupTestDB returns a Repo backed by this package's isolated test database,
// reset to an empty state (plus the seeded "test-project"). It requires
// DATABASE_URL to be set; tests are skipped otherwise.
//
// Exported for use by other packages that need to test against a real database.
func SetupTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()
	repo, _, cleanup := setupTestRepo(t)
	return repo, cleanup
}

// SetupTestDBWithRawDB is like SetupTestDB but also returns the underlying *sql.DB
// for tests that need to run raw SQL queries (e.g., verifying side effects).
func SetupTestDBWithRawDB(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()
	return setupTestRepo(t)
}

// setupTestRepo is the shared implementation: it ensures the per-package test
// database exists and is migrated, opens a connection to it, resets it to an
// empty state, seeds the shared test project, and returns a Repo.
func setupTestRepo(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()

	baseDSN := os.Getenv("DATABASE_URL")
	if baseDSN == "" {
		t.Skip("DATABASE_URL not set, skipping database test")
	}

	dsn, err := ensurePkgTestDB(baseDSN)
	if err != nil {
		t.Fatalf("failed to prepare package test database: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Reset to an empty schema so each test starts clean, then seed the shared
	// project that satisfies the projects FK on chats.
	if err := truncateAllTables(db); err != nil {
		db.Close()
		t.Fatalf("failed to reset test database: %v", err)
	}
	// Seed the shared "test-project" that many tests reference by ID to satisfy
	// the projects FK on chats. Its path is a unique sentinel (not the common
	// "/tmp/test") so it never collides with tests that create their own project
	// at that path via the projects_user_id_path_key unique index.
	if _, err := db.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active) VALUES ('test-project', 'test-user', 'Test Project', '/tmp/reliant-test-seed-project', NOW(), NOW(), NOW()) ON CONFLICT (id) DO NOTHING`); err != nil {
		db.Close()
		t.Fatalf("failed to create test project: %v", err)
	}

	repo := NewRepoWithDriver(db, DriverPostgres)
	cleanup := func() {
		db.Close()
	}
	return repo, db, cleanup
}

// ensurePkgTestDB creates (once per test binary) an isolated database for this
// package and migrates it, returning a DSN pointing at that database. The
// database name is derived from the package's working directory, so each
// concurrently-running package binary gets its own database and the same
// package reuses the same database across runs.
func ensurePkgTestDB(baseDSN string) (string, error) {
	pkgDBOnce.Do(func() {
		pkgDBDSN, pkgDBErr = createPkgTestDB(baseDSN)
	})
	return pkgDBDSN, pkgDBErr
}

func createPkgTestDB(baseDSN string) (string, error) {
	u, err := url.Parse(baseDSN)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	baseName := strings.TrimPrefix(u.Path, "/")
	if baseName == "" {
		baseName = "postgres"
	}

	// Deterministic, valid, short database name unique to this package. go test
	// sets the working directory to the package directory, so this differs per
	// package and is stable across runs.
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	sum := sha1.Sum([]byte(wd))
	pkgName := fmt.Sprintf("%s_test_%s", baseName, hex.EncodeToString(sum[:6]))

	// Connect to the base (maintenance) database to create the package database
	// if it does not already exist. CREATE DATABASE cannot run inside the target
	// database and has no IF NOT EXISTS form, so we check pg_database first.
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		return "", fmt.Errorf("open admin connection: %w", err)
	}
	defer admin.Close()

	var exists bool
	if err := admin.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, pkgName).Scan(&exists); err != nil {
		return "", fmt.Errorf("check package database: %w", err)
	}
	if !exists {
		if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %s`, quoteIdent(pkgName))); err != nil {
			// Tolerate a race with a parallel process that created it first.
			if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return "", fmt.Errorf("create package database: %w", err)
			}
		}
	}

	// Build the DSN for the package database (preserving user, host, query args)
	// and run migrations into it. RunMigrations is idempotent, so it is cheap on
	// repeat runs and applies any new migrations when the schema changes.
	pkgURL := *u
	pkgURL.Path = "/" + pkgName
	dsn := pkgURL.String()

	migDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return "", fmt.Errorf("open package database: %w", err)
	}
	defer migDB.Close()
	if err := RunMigrations(migDB); err != nil {
		return "", fmt.Errorf("migrate package database: %w", err)
	}

	return dsn, nil
}

// truncateAllTables empties every table in the public schema (except goose's
// migration bookkeeping) with a single TRUNCATE ... RESTART IDENTITY CASCADE.
// The table list is discovered dynamically so new migrations never require
// updating this helper.
func truncateAllTables(db *sql.DB) error {
	var stmt sql.NullString
	err := db.QueryRow(`
		SELECT 'TRUNCATE TABLE ' || string_agg(format('%I.%I', schemaname, tablename), ', ') || ' RESTART IDENTITY CASCADE'
		FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version'
	`).Scan(&stmt)
	if err != nil {
		return fmt.Errorf("build truncate statement: %w", err)
	}
	if !stmt.Valid || stmt.String == "" {
		return nil // no tables yet
	}
	if _, err := db.Exec(stmt.String); err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	return nil
}

// quoteIdent double-quotes a Postgres identifier for safe interpolation into
// DDL (database names cannot be parameterized).
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
