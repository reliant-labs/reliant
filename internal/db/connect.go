// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"go.opentelemetry.io/otel/attribute"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"

	"github.com/pressly/goose/v3"
)

type DatabaseDriver string

const (
	DriverSQLite   DatabaseDriver = "sqlite"
	DriverPostgres DatabaseDriver = "postgres"
)

type DatabaseConfig struct {
	Driver  DatabaseDriver
	DataDir string
	URL     string
}

func ParseDatabaseDriver(raw string) (DatabaseDriver, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DriverSQLite):
		return DriverSQLite, nil
	case string(DriverPostgres):
		return DriverPostgres, nil
	default:
		return "", fmt.Errorf("invalid DATABASE_DRIVER %q (expected sqlite or postgres)", raw)
	}
}

func ResolveDatabaseConfig(dataDir string) (DatabaseConfig, error) {
	driver, err := ParseDatabaseDriver(os.Getenv("DATABASE_DRIVER"))
	if err != nil {
		return DatabaseConfig{}, err
	}

	cfg := DatabaseConfig{
		Driver:  driver,
		DataDir: dataDir,
		URL:     strings.TrimSpace(os.Getenv("DATABASE_URL")),
	}

	if cfg.Driver == DriverPostgres && cfg.URL == "" {
		return DatabaseConfig{}, fmt.Errorf("DATABASE_URL is required when DATABASE_DRIVER=postgres")
	}

	if cfg.Driver == DriverSQLite && cfg.DataDir == "" {
		return DatabaseConfig{}, fmt.Errorf("data.dir is not set")
	}

	return cfg, nil
}

func Connect(dataDir string) (*sql.DB, error) {
	cfg, err := ResolveDatabaseConfig(dataDir)
	if err != nil {
		return nil, err
	}

	return ConnectWithConfig(cfg)
}

func ConnectWithConfig(cfg DatabaseConfig) (*sql.DB, error) {
	switch cfg.Driver {
	case DriverSQLite:
		return connectSQLite(cfg)
	case DriverPostgres:
		return connectPostgres(cfg)
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Driver)
	}
}

func connectSQLite(cfg DatabaseConfig) (*sql.DB, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}
	dbPath := filepath.Join(cfg.DataDir, "reliant.db")
	logging.Info("Opening SQLite database", "path", dbPath)

	// Open the SQLite database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		logging.Error("Failed to open database", "path", dbPath, "error", err)
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Register OTel for DB stats metrics
	if _, regErr := otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(attribute.String("db.system", "sqlite"))); regErr != nil {
		logging.Warn("Failed to register DB stats metrics", "error", regErr)
	}

	// Verify connection
	if err = db.Ping(); err != nil {
		logging.Error("Failed to ping database", "path", dbPath, "error", err)
		if closeErr := db.Close(); closeErr != nil {
			logging.Error("Failed to close database after ping failure", "error", closeErr)
		}
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	logging.Debug("Database connection verified", "path", dbPath)

	// Check for incompatible migration gaps before running migrations.
	// If detected, delete the database and start fresh.
	if needsReset, reason := checkMigrationCompatibility(db); needsReset {
		logging.Warn("Database has incompatible migration gap, resetting", "reason", reason)
		if err := db.Close(); err != nil {
			logging.Error("Failed to close database before reset", "error", err)
		}
		if err := deleteDatabase(dbPath); err != nil {
			return nil, fmt.Errorf("failed to delete incompatible database: %w", err)
		}
		logging.Info("Deleted incompatible database, creating fresh one")
		// Re-open fresh database
		db, err = sql.Open("sqlite3", dbPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open fresh database: %w", err)
		}
		if err = db.Ping(); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to connect to fresh database: %w", err)
		}
	}

	// Set connection pool limits for SQLite
	// SQLite with WAL mode supports multiple concurrent readers and 1 writer
	// Increased from 2 to 10 to allow more concurrent read operations
	// The global write mutex in repo.go still serializes write transactions
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0) // Connections live forever unless explicitly closed

	// Set pragmas for better performance and lock handling
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;", // 5 seconds - fail fast, rely on application-level retries
		"PRAGMA page_size = 4096;",
		"PRAGMA cache_size = -8000;",
		"PRAGMA synchronous = NORMAL;",
	}

	for _, pragma := range pragmas {
		if _, err = db.Exec(pragma); err != nil {
			logging.Error("Failed to set pragma", "pragma", pragma, "error", err)
		} else {
			logging.Debug("Set pragma", "pragma", pragma)
		}
	}

	// Log connection pool configuration
	logging.Info("Database connection pool configured",
		"maxOpenConns", 10,
		"maxIdleConns", 5,
		"busyTimeout", "5s")

	// Use embedded filesystem for migrations
	goose.SetBaseFS(FS)

	logging.Debug("Applying database migrations...")
	if err := RunMigrations(db, DriverSQLite); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}
	logging.Debug("Database migrations applied successfully")

	// Seed API key from environment variables (dev mode only)
	if err := seedAPIKeyFromEnv(db); err != nil {
		logging.Warn("Failed to seed API key from environment", "error", err)
		// Don't fail startup for seeding errors
	}

	logging.Info("Database ready", "path", dbPath)
	return db, nil
}

func connectPostgres(cfg DatabaseConfig) (*sql.DB, error) {
	logging.Info("Opening Postgres database")

	db, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres database: %w", err)
	}

	// Register OTel for DB stats metrics
	if _, regErr := otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(attribute.String("db.system", "postgresql"))); regErr != nil {
		logging.Warn("Failed to register DB stats metrics", "error", regErr)
	}

	if err := db.Ping(); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			logging.Error("Failed to close postgres database after ping failure", "error", closeErr)
		}
		return nil, fmt.Errorf("failed to connect to postgres database: %w", err)
	}

	// Postgres supports concurrent writers; no SQLite-specific locking pragmas.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	logging.Debug("Applying database migrations...")
	if err := RunMigrations(db, DriverPostgres); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	logging.Info("Database ready", "driver", DriverPostgres)
	return db, nil
}

// RunMigrations runs database migrations using goose
func RunMigrations(db *sql.DB, driver ...DatabaseDriver) error {
	selectedDriver := DriverSQLite
	if len(driver) > 0 {
		selectedDriver = driver[0]
	}

	// Use embedded filesystem for migrations
	goose.SetBaseFS(FS)

	dialect := gooseDialectForDriver(selectedDriver)
	if err := goose.SetDialect(dialect); err != nil {
		logging.Error("Failed to set dialect", "error", err)
		return fmt.Errorf("failed to set dialect: %w", err)
	}

	migrationsDir := gooseMigrationsDirForDriver(selectedDriver)
	if err := goose.Up(db, migrationsDir, goose.WithAllowMissing()); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
}

func gooseDialectForDriver(driver DatabaseDriver) string {
	if driver == DriverPostgres {
		return "postgres"
	}
	return "sqlite3"
}

func gooseMigrationsDirForDriver(driver DatabaseDriver) string {
	if driver == DriverPostgres {
		return "migrations/postgres"
	}
	return "migrations/sqlite"
}

// getUserIDFromAuthFile reads the user ID from the Electron app's auth file.
// This is the source of truth for the currently logged-in user.
func getUserIDFromAuthFile() (string, error) {
	return auth.ReadUserIDFromAuthFile()
}

// seedAPIKeyFromEnv seeds an API key into the api_keys table from environment variables.
// This is for development use only.
//
// Environment variables:
//   - RELIANT_SEED_API_KEY: The API key to seed (required)
//   - RELIANT_SEED_PROVIDER: The provider name (required, e.g., "anthropic", "openai", "openrouter")
//
// The user ID is read from the Electron app's auth file.
func seedAPIKeyFromEnv(db *sql.DB) error {
	apiKey := os.Getenv("RELIANT_SEED_API_KEY")
	provider := os.Getenv("RELIANT_SEED_PROVIDER")

	// Both must be set
	if apiKey == "" || provider == "" {
		logging.Debug("Skipping API key seeding",
			"hasApiKey", apiKey != "",
			"hasProvider", provider != "")
		return nil // Not configured, skip silently
	}

	logging.Info("Attempting to seed API key from environment", "provider", provider)

	// Get user ID from the Electron app's auth file
	userID, err := getUserIDFromAuthFile()
	if err != nil {
		return fmt.Errorf("failed to get user ID from auth file: %w", err)
	}
	if userID == "" {
		return fmt.Errorf("no user_id found in auth file - please log in first")
	}

	// Validate provider is one of the supported ones
	validProviders := map[string]bool{
		"codex":      true,
		"anthropic":  true,
		"openai":     true,
		"gemini":     true,
		"groq":       true,
		"openrouter": true,
		"xai":        true,
		"azure":      true,
		"bedrock":    true,
		"vertexai":   true,
		"copilot":    true,
	}

	if !validProviders[provider] {
		return fmt.Errorf("invalid provider: %s", provider)
	}

	now := time.Now().UTC()
	keyID := uuid.New().String()

	// Upsert into api_keys table
	_, err = db.Exec(
		`INSERT INTO api_keys (id, user_id, provider, api_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, provider) DO UPDATE SET
		   api_key = excluded.api_key,
		   updated_at = excluded.updated_at`,
		keyID, userID, provider, apiKey, now, now,
	)
	if err != nil {
		return fmt.Errorf("failed to seed API key: %w", err)
	}

	logging.Info("Seeded API key from environment",
		"provider", provider,
		"userID", userID)

	return nil
}

// checkMigrationCompatibility checks if the database has the known migration 17 gap
// that affects users upgrading from v0.2.4. Migration 17 was added after v0.2.4 shipped,
// so users have migrations 18+ but are missing 17, causing goose to fail.
// Returns true if the database needs to be reset.
func checkMigrationCompatibility(db *sql.DB) (needsReset bool, reason string) {
	// Check if goose_db_version table exists
	var tableName string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='goose_db_version'",
	).Scan(&tableName)
	if err == sql.ErrNoRows {
		// No migration history - fresh database, no reset needed
		return false, ""
	}
	if err != nil {
		logging.Error("Failed to check for goose_db_version table", "error", err)
		return false, ""
	}

	// Get the maximum applied migration version
	var maxVersion int64
	err = db.QueryRow(
		"SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version",
	).Scan(&maxVersion)
	if err != nil {
		logging.Error("Failed to get max migration version", "error", err)
		return false, ""
	}

	// If max version is >= 18, check if migration 17 exists
	// This is the specific gap caused by upgrading from v0.2.4
	if maxVersion >= 18 {
		var has17 int
		err = db.QueryRow(
			"SELECT COUNT(*) FROM goose_db_version WHERE version_id = 17",
		).Scan(&has17)
		if err != nil {
			logging.Error("Failed to check for migration 17", "error", err)
			return false, ""
		}

		if has17 == 0 {
			return true, fmt.Sprintf("migration 17 missing, max version is %d", maxVersion)
		}
	}

	return false, ""
}

// deleteDatabase backs up and removes the SQLite database file and its associated WAL/SHM files.
func deleteDatabase(dbPath string) error {
	// Create backup with timestamp
	timestamp := time.Now().Format("20060102-150405")
	backupPath := dbPath + ".backup-" + timestamp

	// Backup main database file
	if err := os.Rename(dbPath, backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to backup database file: %w", err)
	}
	logging.Info("Backed up database", "from", dbPath, "to", backupPath)

	// Delete WAL file (not worth backing up - it's a journal)
	walPath := dbPath + "-wal"
	if err := os.Remove(walPath); err != nil && !os.IsNotExist(err) {
		logging.Warn("Failed to delete WAL file", "path", walPath, "error", err)
	}

	// Delete SHM file (not worth backing up - it's shared memory)
	shmPath := dbPath + "-shm"
	if err := os.Remove(shmPath); err != nil && !os.IsNotExist(err) {
		logging.Warn("Failed to delete SHM file", "path", shmPath, "error", err)
	}

	return nil
}