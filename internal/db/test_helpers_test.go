// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"testing"
)

// setupTestDB creates an in-memory database with migrations for testing.
// It also creates a test project and user to satisfy foreign key constraints.
// Returns a Repo and a cleanup function.
func setupTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}

	if err := RunMigrations(db); err != nil {
		db.Close()
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create test project and user to satisfy foreign key constraints on chats
	_, err = db.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at) VALUES ('test-project', 'test-user', 'Test Project', '/tmp/test', datetime('now'), datetime('now'))`)
	if err != nil {
		db.Close()
		t.Fatalf("failed to create test project: %v", err)
	}

	repo := NewRepo(db)

	cleanup := func() {
		db.Close()
	}

	return repo, cleanup
}
