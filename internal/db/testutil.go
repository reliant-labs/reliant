// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
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

// SetupTestDB creates a test database with migrations for testing.
// It requires DATABASE_URL to be set in the environment pointing to a Postgres instance.
// Returns a Repo and a cleanup function.
//
// This is exported for use by other packages that need to test against a real database.
func SetupTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set, skipping database test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	if err := RunMigrations(db); err != nil {
		db.Close()
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create test project and user to satisfy foreign key constraints on chats
	_, err = db.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at) VALUES ('test-project', 'test-user', 'Test Project', '/tmp/test', NOW(), NOW()) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		db.Close()
		t.Fatalf("failed to create test project: %v", err)
	}

	repo := NewRepoWithDriver(db, DriverPostgres)

	cleanup := func() {
		db.Close()
	}

	return repo, cleanup
}

// SetupTestDBWithRawDB is like SetupTestDB but also returns the underlying *sql.DB
// for tests that need to run raw SQL queries (e.g., verifying side effects).
func SetupTestDBWithRawDB(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set, skipping database test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	if err := RunMigrations(db); err != nil {
		db.Close()
		t.Fatalf("failed to run migrations: %v", err)
	}

	_, err = db.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at) VALUES ('test-project', 'test-user', 'Test Project', '/tmp/test', NOW(), NOW()) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		db.Close()
		t.Fatalf("failed to create test project: %v", err)
	}

	repo := NewRepoWithDriver(db, DriverPostgres)

	cleanup := func() {
		db.Close()
	}

	return repo, db, cleanup
}
