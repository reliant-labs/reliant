// Copyright (c) 2025 Reliant Labs
package db

import (
	"testing"
)

// setupTestDB creates a database with migrations for testing.
// It creates a test project and user to satisfy foreign key constraints.
// Returns a Repo and a cleanup function.
//
// Delegates to the exported SetupTestDB (testutil.go).
func setupTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()
	return SetupTestDB(t)
}
