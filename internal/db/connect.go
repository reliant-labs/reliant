// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/attribute"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"

	"github.com/pressly/goose/v3"
)

type DatabaseDriver string

const (
	DriverPostgres DatabaseDriver = "postgres"
)

type DatabaseConfig struct {
	Driver  DatabaseDriver
	DataDir string
	URL     string
}

func ParseDatabaseDriver(raw string) (DatabaseDriver, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DriverPostgres):
		return DriverPostgres, nil
	default:
		return "", fmt.Errorf("invalid DATABASE_DRIVER %q (expected postgres)", raw)
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

	if cfg.URL == "" {
		return DatabaseConfig{}, fmt.Errorf("DATABASE_URL is required when DATABASE_DRIVER=postgres")
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
	case DriverPostgres:
		return connectPostgres(cfg)
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Driver)
	}
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

	// Postgres connection pool settings.
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
	// Use embedded filesystem for migrations
	goose.SetBaseFS(FS)

	if err := goose.SetDialect("postgres"); err != nil {
		logging.Error("Failed to set dialect", "error", err)
		return fmt.Errorf("failed to set dialect: %w", err)
	}

	if err := goose.Up(db, "migrations/postgres", goose.WithAllowMissing()); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
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
