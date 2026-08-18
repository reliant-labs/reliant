// Copyright (c) 2025 Reliant Labs
package db

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
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

// MigrationPolicy decides what a process does about the schema when it opens
// the database. Exactly one process per database may hold MigrateApply.
//
// Every server used to migrate on open, which is safe for a single process and
// wrong for the deployment we actually run: api-server, temporal-worker and
// gateway all start at once against one Postgres, so goose in each of them
// raced goose in the others. The losers failed on their own success — two
// concurrent CREATE TABLEs collide in the catalog (duplicate key ...
// pg_type_typname_nsp_index) rather than reporting "already exists", and the
// next migration reports `column "seq" of relation "messages" already exists`.
// Both are startup crashes caused purely by the second migrator existing.
//
// Making the DDL idempotent would not have fixed it: CREATE TABLE IF NOT
// EXISTS still races on the same catalog index because the existence check is
// not taken under a lock, and a migration like 20260802000000_add_message_seq
// is a backfill plus an ADD CONSTRAINT — there is no "if not exists" spelling
// of it that stays correct when two processes interleave.
type MigrationPolicy int

const (
	// MigrateApply owns the schema: it runs goose to completion on open. This
	// is the zero value so that any single-process caller (the CLI, the
	// Electron-embedded server, tests) keeps migrating itself by default —
	// a lone process that waited for a migrator would wait forever.
	MigrateApply MigrationPolicy = iota

	// MigrateWait does not migrate. It blocks on open until every embedded
	// migration is recorded applied, then proceeds. Waiting rather than
	// skipping is the point: a process that read a stale schema would fail
	// later, deep in a query, instead of here with a clear message.
	MigrateWait
)

type DatabaseConfig struct {
	Driver  DatabaseDriver
	DataDir string
	URL     string

	// Migrate defaults to MigrateApply; see MigrationPolicy.
	Migrate MigrationPolicy
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

	// Postgres connection pool settings. Defaults are 25 open / 10 idle, but
	// both are env-overridable so dev can cap them low: the dev Postgres is
	// SHARED by N parallel stacks (one per git worktree), and reliant's api +
	// worker pools count against the same instance's max_connections alongside
	// control-plane's services. Dev sets RELIANT_DB_MAX_OPEN_CONNS=8 /
	// RELIANT_DB_MAX_IDLE_CONNS=2 (see control-plane deploy/kcl/dev/main.k
	// _reliant_host_env). Prod/anything that sets neither keeps 25/10.
	db.SetMaxOpenConns(envInt("RELIANT_DB_MAX_OPEN_CONNS", 25))
	db.SetMaxIdleConns(envInt("RELIANT_DB_MAX_IDLE_CONNS", 10))
	db.SetConnMaxLifetime(30 * time.Minute)

	switch cfg.Migrate {
	case MigrateWait:
		if err := WaitForSchema(db); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				logging.Error("Failed to close postgres database after schema wait failure", "error", closeErr)
			}
			return nil, err
		}
	default:
		logging.Debug("Applying database migrations...")
		if err := RunMigrations(db, DriverPostgres); err != nil {
			logging.Error("Failed to apply migrations", "error", err)
			return nil, fmt.Errorf("failed to apply migrations: %w", err)
		}
	}

	logging.Info("Database ready", "driver", DriverPostgres)
	return db, nil
}

// envInt reads a non-negative integer from the named env var, falling back to
// def when the var is unset, empty, or not a positive integer. Used for the
// connection-pool sizing knobs so dev can cap them low (shared dev Postgres)
// while prod keeps the built-in defaults.
func envInt(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		logging.Warn("Ignoring invalid pool-size env var; using default", "var", name, "value", raw, "default", def)
		return def
	}
	return v
}

// migrationsDir is the path of the embedded migrations within FS.
const migrationsDir = "migrations/postgres"

// initGoose points goose at the embedded migrations and selects the dialect.
// Both applying and waiting need this: the waiter reads the same embedded set
// to know what "current" means.
func initGoose() error {
	goose.SetBaseFS(FS)

	if err := goose.SetDialect("postgres"); err != nil {
		logging.Error("Failed to set dialect", "error", err)
		return fmt.Errorf("failed to set dialect: %w", err)
	}

	return nil
}

// RunMigrations runs database migrations using goose
func RunMigrations(db *sql.DB, driver ...DatabaseDriver) error {
	if err := initGoose(); err != nil {
		return err
	}

	if err := goose.Up(db, migrationsDir, goose.WithAllowMissing()); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
}

// WaitForSchema blocks until every embedded migration is recorded applied in
// goose's version table — i.e. until the process holding MigrateApply has
// finished. It is the read-only half of the single-migrator rule.
//
// The check is set-based (every embedded version present) rather than
// "max(version_id) >= newest", because RunMigrations passes
// goose.WithAllowMissing(): a branch can legitimately land a migration with a
// lower version than one already applied, and a max comparison would call that
// schema current while the older migration was still pending.
func WaitForSchema(db *sql.DB) error {
	if err := initGoose(); err != nil {
		return err
	}

	want, err := embeddedMigrationVersions()
	if err != nil {
		return err
	}

	timeout := time.Duration(envInt("RELIANT_DB_SCHEMA_WAIT_SECONDS", 300)) * time.Second
	deadline := time.Now().Add(timeout)

	const pollInterval = time.Second
	var lastLog time.Time

	for {
		missing, err := missingMigrationVersions(db, want)
		if err != nil {
			return fmt.Errorf("failed to read schema version: %w", err)
		}

		if len(missing) == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf(
				"timed out after %s waiting for database migrations: %d of %d not applied (oldest pending: %d). "+
					"The api-server owns the schema — check that it started and applied migrations successfully",
				timeout, len(missing), len(want), missing[0],
			)
		}

		// One line every 10s, not one per poll: a slow backfill can hold this
		// loop for minutes and the log still has to be readable.
		if time.Since(lastLog) >= 10*time.Second {
			logging.Info("Waiting for database migrations to be applied by api-server",
				"pending", len(missing), "total", len(want), "oldest_pending", missing[0])
			lastLog = time.Now()
		}

		time.Sleep(pollInterval)
	}
}

// embeddedMigrationVersions returns the versions of every migration compiled
// into this binary, ascending.
func embeddedMigrationVersions() ([]int64, error) {
	migrations, err := goose.CollectMigrations(migrationsDir, 0, math.MaxInt64)
	if err != nil {
		return nil, fmt.Errorf("failed to collect embedded migrations: %w", err)
	}

	versions := make([]int64, 0, len(migrations))
	for _, m := range migrations {
		versions = append(versions, m.Version)
	}

	slices.Sort(versions)
	return versions, nil
}

// missingMigrationVersions returns those of want that are not recorded applied,
// ascending. A goose version table that does not exist yet means the migrator
// has not reached its first migration: everything is missing, which is a state
// to keep waiting through, not an error.
func missingMigrationVersions(db *sql.DB, want []int64) ([]int64, error) {
	var tableExists bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, goose.TableName()).Scan(&tableExists); err != nil {
		return nil, err
	}

	if !tableExists {
		return want, nil
	}

	rows, err := db.Query(fmt.Sprintf(`SELECT version_id FROM %s WHERE is_applied`, goose.TableName())) //nolint:gosec // goose.TableName is a compile-time constant, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int64]struct{})
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var missing []int64
	for _, v := range want {
		if _, ok := applied[v]; !ok {
			missing = append(missing, v)
		}
	}

	return missing, nil
}

// getUserIDFromAuthFile reads the user ID from the Electron app's auth file.
// This is the source of truth for the currently logged-in user.
func getUserIDFromAuthFile() (string, error) {
	return auth.ReadUserIDFromAuthFile()
}
