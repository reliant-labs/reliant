package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func TestParseDatabaseDriver(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    DatabaseDriver
		wantErr bool
	}{
		{name: "default empty", input: "", want: DriverSQLite},
		{name: "sqlite explicit", input: "sqlite", want: DriverSQLite},
		{name: "sqlite mixed case", input: "SqLiTe", want: DriverSQLite},
		{name: "postgres", input: "postgres", want: DriverPostgres},
		{name: "invalid", input: "mysql", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDatabaseDriver(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestResolveDatabaseConfig_SQLiteDefault(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "")
	t.Setenv("DATABASE_URL", "")

	cfg, err := ResolveDatabaseConfig("/tmp/reliant-test")
	if err != nil {
		t.Fatalf("ResolveDatabaseConfig failed: %v", err)
	}

	if cfg.Driver != DriverSQLite {
		t.Fatalf("expected sqlite driver, got %q", cfg.Driver)
	}
	if cfg.DataDir != "/tmp/reliant-test" {
		t.Fatalf("expected data dir to be preserved, got %q", cfg.DataDir)
	}
}

func TestResolveDatabaseConfig_PostgresRequiresURL(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("DATABASE_URL", "")

	_, err := ResolveDatabaseConfig("/tmp/reliant-test")
	if err == nil {
		t.Fatal("expected error when DATABASE_URL missing for postgres")
	}
}

func TestResolveDatabaseConfig_PostgresWithURL(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/reliant?sslmode=disable")

	cfg, err := ResolveDatabaseConfig("/tmp/reliant-test")
	if err != nil {
		t.Fatalf("ResolveDatabaseConfig failed: %v", err)
	}

	if cfg.Driver != DriverPostgres {
		t.Fatalf("expected postgres driver, got %q", cfg.Driver)
	}
	if cfg.URL == "" {
		t.Fatal("expected DATABASE_URL to be set")
	}
}

func TestCheckMigrationCompatibility_FreshDatabase(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	needsReset, reason := checkMigrationCompatibility(db)

	if needsReset {
		t.Errorf("expected needsReset=false for fresh DB, got true with reason: %s", reason)
	}
}

func TestCheckMigrationCompatibility_NoGap(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	// Create goose table with contiguous migrations 1-20
	setupGooseTable(t, db)
	for i := 1; i <= 20; i++ {
		insertMigration(t, db, i)
	}

	needsReset, reason := checkMigrationCompatibility(db)

	if needsReset {
		t.Errorf("expected needsReset=false for contiguous migrations, got true with reason: %s", reason)
	}
}

func TestCheckMigrationCompatibility_Migration17Gap(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	// Create goose table with migrations 1-16, 18-20 (missing 17)
	setupGooseTable(t, db)
	for i := 1; i <= 16; i++ {
		insertMigration(t, db, i)
	}
	for i := 18; i <= 20; i++ {
		insertMigration(t, db, i)
	}

	needsReset, reason := checkMigrationCompatibility(db)

	if !needsReset {
		t.Error("expected needsReset=true for migration 17 gap, got false")
	}
	if !strings.Contains(reason, "migration 17 missing") {
		t.Errorf("expected reason to mention migration 17, got: %s", reason)
	}
}

func TestCheckMigrationCompatibility_MaxVersionBelow18(t *testing.T) {
	db := createTestDB(t)
	defer db.Close()

	// Create goose table with only migrations 1-10
	setupGooseTable(t, db)
	for i := 1; i <= 10; i++ {
		insertMigration(t, db, i)
	}

	needsReset, reason := checkMigrationCompatibility(db)

	if needsReset {
		t.Errorf("expected needsReset=false when max version < 18, got true with reason: %s", reason)
	}
}

func TestDeleteDatabase_CreatesBackup(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create a test database file
	if err := os.WriteFile(dbPath, []byte("test data"), 0644); err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}

	// Create WAL and SHM files
	if err := os.WriteFile(dbPath+"-wal", []byte("wal data"), 0644); err != nil {
		t.Fatalf("failed to create wal file: %v", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("shm data"), 0644); err != nil {
		t.Fatalf("failed to create shm file: %v", err)
	}

	// Delete (backup) the database
	if err := deleteDatabase(dbPath); err != nil {
		t.Fatalf("deleteDatabase failed: %v", err)
	}

	// Verify original files are gone
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("expected database file to be removed")
	}
	if _, err := os.Stat(dbPath + "-wal"); !os.IsNotExist(err) {
		t.Error("expected WAL file to be removed")
	}
	if _, err := os.Stat(dbPath + "-shm"); !os.IsNotExist(err) {
		t.Error("expected SHM file to be removed")
	}

	// Verify backup was created
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read temp dir: %v", err)
	}

	var backupFound bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "test.db.backup-") {
			backupFound = true
			// Verify backup contains original data
			data, err := os.ReadFile(filepath.Join(tmpDir, e.Name()))
			if err != nil {
				t.Fatalf("failed to read backup: %v", err)
			}
			if string(data) != "test data" {
				t.Errorf("backup content mismatch: got %q", string(data))
			}
			break
		}
	}

	if !backupFound {
		t.Error("expected backup file to be created")
	}
}

// Helper functions

func createTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	return db
}

func setupGooseTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("failed to create goose table: %v", err)
	}

	// Insert version 0 (goose's initial record)
	_, err = db.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1)")
	if err != nil {
		t.Fatalf("failed to insert version 0: %v", err)
	}
}

func insertMigration(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	_, err := db.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", version)
	if err != nil {
		t.Fatalf("failed to insert migration %d: %v", version, err)
	}
}
