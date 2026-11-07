//go:build integration

// TestDiffDatabase exercises the differ against a real SQLite database
// (in-memory mode). It's gated behind the `integration` build tag so
// `task test` (the unit pass) doesn't try to load the SQLite driver,
// open a database handle, or block on out-of-process I/O. Run via:
//
//	task test:integration
//
// The previous `testing.Short()` gate was friendlier to mistakes — it
// skipped silently if anyone forgot `-short`, leaving developers
// thinking they'd run a real test pass when they hadn't. The build-tag
// gate makes the boundary explicit and is the forge convention.

package orm

import (
	"context"
	"database/sql"
	"testing"
)

// TestDiffDatabase is an integration test with a real SQLite database
func TestDiffDatabase(t *testing.T) {
	ctx := context.Background()

	// Create an in-memory SQLite database for testing
	// Note: This test assumes SQLite dialect is available
	// If not available, this test will be skipped
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Skipf("SQLite not available: %v", err)
	}
	defer db.Close()

	// Check if SQLite dialect is registered
	_, err = GetDialect("sqlite")
	if err != nil {
		t.Skipf("SQLite dialect not registered: %v", err)
	}

	client, err := NewClientWithDB(db, "sqlite")
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	defer client.Close()

	// Create a test table with initial schema
	_, err = client.Exec(ctx, `
		CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL,
			name TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Create an index
	_, err = client.Exec(ctx, `CREATE INDEX idx_users_email ON users(email)`)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}

	// Define expected schema with changes
	expectedSchemas := []TableSchema{
		{
			Name: "users",
			Fields: []FieldSchema{
				{Name: "id", Type: TypeInteger, PrimaryKey: true, NotNull: true},
				{Name: "email", Type: TypeText, NotNull: true},
				{Name: "name", Type: TypeText, NotNull: false},
				{Name: "age", Type: TypeInteger, NotNull: false},     // New column
				{Name: "created_at", Type: TypeText, NotNull: false}, // New column
			},
			Indexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: false},
				{Name: "idx_users_name", Fields: []string{"name"}, Unique: false}, // New index
			},
		},
	}

	// Run diff
	diffs, err := DiffDatabase(ctx, client, client.Dialect(), expectedSchemas)
	if err != nil {
		t.Fatalf("DiffDatabase failed: %v", err)
	}

	// Verify we got exactly one diff (for users table)
	if len(diffs) != 1 {
		t.Fatalf("Expected 1 diff, got %d", len(diffs))
	}

	diff := diffs[0]

	// Verify missing columns
	if len(diff.MissingColumns) != 2 {
		t.Errorf("Expected 2 missing columns, got %d", len(diff.MissingColumns))
	}

	missingNames := make(map[string]bool)
	for _, col := range diff.MissingColumns {
		missingNames[col.Name] = true
	}
	if !missingNames["age"] || !missingNames["created_at"] {
		t.Error("Expected 'age' and 'created_at' in missing columns")
	}

	// Verify missing indexes
	if len(diff.MissingIndexes) != 1 {
		t.Errorf("Expected 1 missing index, got %d", len(diff.MissingIndexes))
	}

	if len(diff.MissingIndexes) > 0 && diff.MissingIndexes[0].Name != "idx_users_name" {
		t.Errorf("Expected missing index 'idx_users_name', got %q", diff.MissingIndexes[0].Name)
	}

	// Test with non-existent table
	t.Run("Non-existent table", func(t *testing.T) {
		nonExistentSchemas := []TableSchema{
			{
				Name: "non_existent_table",
				Fields: []FieldSchema{
					{Name: "id", Type: TypeInteger, PrimaryKey: true},
				},
			},
		}

		diffs, err := DiffDatabase(ctx, client, client.Dialect(), nonExistentSchemas)
		if err != nil {
			t.Fatalf("DiffDatabase failed: %v", err)
		}

		if len(diffs) != 1 {
			t.Fatalf("Expected 1 diff, got %d", len(diffs))
		}

		// All fields should be missing
		if len(diffs[0].MissingColumns) != 1 {
			t.Errorf("Expected all fields to be missing for non-existent table")
		}
	})
}
