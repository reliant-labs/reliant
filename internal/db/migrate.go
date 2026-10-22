// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/reliant-labs/forge/pkg/migratekit"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
	"github.com/reliant-labs/forge/pkg/observe/dbtrace"

	forgedb "github.com/reliant-labs/reliant/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// The schema is forge migrations: db/migrations/*.up.sql, embedded into the
// binary by `forge generate` (db/embed_gen.go) and applied by forge's migrator
// (forge/pkg/migratekit, golang-migrate underneath). Forward only; a version is
// applied once, in order, and recorded in schema_migrations.
//
// The first migration is the baseline: it builds a fresh database. Every
// database we keep was migrated by goose until 2026-10 and adopted by forge's
// migrator on its first boot of a release carrying the goose bridge (reliant
// v1.8.2 onwards). So beside a fresh database, this binary meets only ones
// forge's migrator has a record in, and refuses the rest (MigrationPlan).
// Every forge migration after the baseline must be declared
// `-- forge:backward-compatible` unless it deliberately ends rollback to the
// previous release (`-- forge:breaks-rollback`; forge.yaml's
// database.migration_safety.require_compat_declaration, enforced by
// `forge lint --migration-safety`). See internal/db/SCHEMA.md.

// migrationsTable is golang-migrate's version record: one row, (version, dirty).
const migrationsTable = "schema_migrations"

// MigrationURL is the connection string migrations run on: DirectURL when it
// is set, else URL.
//
// forge's migrator holds session-level advisory locks for the whole run — its
// run lock, which takes replicas and `db migrate up` one at a time, and
// golang-migrate's own. Behind a
// transaction-pooling PgBouncer a session lock lands on a backend the caller
// does not keep, exactly as for TryAdvisoryLock, so migrations need the
// session-capable URL whenever there is one.
func (c DatabaseConfig) MigrationURL() string {
	if c.DirectURL != "" {
		return c.DirectURL
	}
	return c.URL
}

// MigrateResult is what one Migrate run did: forge's migrator's report — the
// versions applied, or a schema ahead of this binary.
type MigrateResult struct {
	migratekit.Result
}

// Migrate applies every migration this binary embeds that the database has
// not applied, and reports what it did. It is the one migration path: the
// api-server's MigrateApply and `reliant db migrate up` both call it, with
// DatabaseConfig.MigrationURL.
//
// It runs on ONE Postgres session of its own, opened from dsn, never on the
// app's pool. MigrationURL is session-capable where the app's URL may not be,
// and one session is what keeps concurrent runs from deadlocking behind a
// session pooler with few server connections (prod's reliant_cp_session pool
// has two per PgBouncer replica). Several callers may run it at once — api
// replicas booting together, or a migrate Job beside them: the migrator's run
// lock makes them take turns, a waiter holds only its own session while it
// waits, and every one after the first finds nothing to do.
//
// What it refuses, it refuses before changing anything: reliant's tables with
// no migration record (MigrationPlan.refusal), a schema left dirty by a
// migration that failed partway (`reliant db migrate force` is the way out),
// and a schema ahead of this binary that was not declared backward-compatible
// (*migratekit.SchemaAheadError).
func Migrate(ctx context.Context, dsn string) (MigrateResult, error) {
	pool, err := dbtrace.Open("pgx", dsn)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("open migration connection: %w", err)
	}
	defer func() { _ = pool.Close() }()
	return MigrateDB(ctx, pool)
}

// ApplyOnBoot is the migrating server's boot step — the api-server's process
// policy runs it before its root is built (migrate=apply) — on pool, a
// connection of its own opened for it on DatabaseConfig.MigrationURL: Migrate,
// then the boot log of what it did.
func ApplyOnBoot(ctx context.Context, pool *sql.DB, logger *slog.Logger) error {
	res, err := MigrateDB(ctx, pool)
	if err != nil {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}
	logMigrateResult(logger, res)
	return nil
}

// MigrateDB is Migrate on pool, which it narrows to the one session the run
// needs and the caller closes.
func MigrateDB(ctx context.Context, pool *sql.DB) (MigrateResult, error) {
	// One session for the whole run: migratekit.Apply holds its run lock on
	// it. Idle connections are never retired, so the session is not swapped
	// out from under the lock.
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(0)
	pool.SetConnMaxIdleTime(0)

	plan, err := PlanMigration(ctx, pool)
	if err != nil {
		return MigrateResult{}, err
	}
	if err := plan.refusal(); err != nil {
		return MigrateResult{}, err
	}
	res, err := migratekit.Apply(ctx, pool, forgedb.Source(), forgedb.SourceDir)
	if err != nil {
		return MigrateResult{Result: res}, fmt.Errorf("migrate up: %w", err)
	}
	return MigrateResult{Result: res}, nil
}

// Lines reports the run one fact per line: what `reliant db migrate up`
// prints, and what the api-server logs at boot.
func (r MigrateResult) Lines() []string {
	switch {
	case r.NothingEmbedded:
		return []string{"no migrations embedded; nothing to do"}
	case r.Ahead != nil:
		return []string{fmt.Sprintf("schema is AHEAD of this binary (version %d; this binary's newest is %d) and every newer version (%s) is declared backward-compatible: nothing applied",
			r.Ahead.Version, r.Ahead.Latest, formatUints(r.Ahead.Versions))}
	case r.Changed:
		return []string{fmt.Sprintf("migrations applied: %s -> %d", versionLabel(r.Before), r.After.Version)}
	default:
		return []string{fmt.Sprintf("no pending migrations (version %s)", versionLabel(r.After))}
	}
}

// logMigrateResult logs Lines at boot. A schema ahead of the binary is
// healthy but must not be missed: WARN.
func logMigrateResult(logger *slog.Logger, r MigrateResult) {
	level := slog.LevelInfo
	if r.Ahead != nil {
		level = slog.LevelWarn
	}
	for _, line := range r.Lines() {
		logger.Log(context.Background(), level, line)
	}
}

// Force records version as the schema's version and clears the dirty flag
// WITHOUT running any SQL — the way out once a migration failed partway and
// the database has been repaired by hand. It asserts a fact and verifies
// nothing. -1 records no version at all. It runs under the migrator's run
// lock, on one session, and keeps the applied record in step with this
// binary's migrations, so run the release whose migration failed.
func Force(dsn string, version int) error {
	m, err := migratekit.Open(migratekit.Options{FS: forgedb.Source(), Dir: forgedb.SourceDir, DSN: dsn})
	if err != nil {
		return fmt.Errorf("open migrator: %w", err)
	}
	defer func() {
		if cerr := m.Close(); cerr != nil {
			logging.Warn("Failed to close the migrator", "error", cerr)
		}
	}()
	return m.Force(version)
}

// DefaultSchemaWait is how long a MigrateWait process waits for the schema
// when its config does not say (RELIANT_DB_SCHEMA_WAIT_SECONDS).
const DefaultSchemaWait = 5 * time.Minute

// WaitForSchema blocks until the database has applied every migration this
// binary embeds — i.e. until the process holding MigrateApply has finished. It
// is the read-only half of the single-migrator rule.
//
// golang-migrate applies in order and forge's migrator refuses to pass a
// version it never applied, so the recorded version is the whole answer: the
// schema is current when it is clean and at or past the newest embedded
// version. Past it is the previous release meeting the next one's schema mid
// deploy, which every waiter has always been allowed to serve.
//
// It reads schema_migrations directly rather than opening a migrator, which
// would create that table and take session locks: a waiter changes nothing,
// and may be talking to a transaction pooler.
//
// timeout bounds the wait; 0 is DefaultSchemaWait.
func WaitForSchema(db *sql.DB, timeout time.Duration) error {
	want, err := EmbeddedMigrationVersions()
	if err != nil {
		return err
	}

	if timeout <= 0 {
		timeout = DefaultSchemaWait
	}
	deadline := time.Now().Add(timeout)

	const pollInterval = time.Second
	var lastLog time.Time

	for {
		state, err := ReadMigrationState(db)
		if err != nil {
			return fmt.Errorf("failed to read schema version: %w", err)
		}

		// A dirty version is not a failure to report here: golang-migrate
		// marks the version dirty while it runs and clears it when the
		// migration commits, so a waiter sees it on every healthy migration.
		missing := pendingVersions(state, want)
		if len(missing) == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf(
				"timed out after %s waiting for database migrations: %d of %d not applied (oldest pending: %d, schema version %d, dirty %t). "+
					"The api-server owns the schema — check that it started and applied migrations successfully",
				timeout, len(missing), len(want), missing[0], state.Version, state.Dirty,
			)
		}

		// One line every 10s, not one per poll: a slow backfill can hold
		// this loop for minutes and the log still has to be readable.
		if time.Since(lastLog) >= 10*time.Second {
			logging.Info("Waiting for database migrations to be applied by api-server",
				"pending", len(missing), "total", len(want), "oldest_pending", missing[0])
			lastLog = time.Now()
		}

		time.Sleep(pollInterval)
	}
}

// ReadMigrationState reads the version forge's migrator recorded, without
// opening a migrator (see WaitForSchema). A database with no record yet
// reports Applied false.
func ReadMigrationState(db *sql.DB) (migratekit.State, error) {
	return readMigrationState(context.Background(), db)
}

func readMigrationState(ctx context.Context, q querier) (migratekit.State, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, migrationsTable).Scan(&exists); err != nil {
		return migratekit.State{}, err
	}
	if !exists {
		return migratekit.State{}, nil
	}

	var (
		version int64
		dirty   bool
	)
	err := q.QueryRowContext(ctx, `SELECT version, dirty FROM `+migrationsTable+` LIMIT 1`).Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return migratekit.State{}, nil
	}
	if err != nil {
		return migratekit.State{}, err
	}
	if version < 0 {
		// golang-migrate's NilVersion: no version recorded.
		return migratekit.State{Dirty: dirty}, nil
	}
	return migratekit.State{Version: uint(version), Dirty: dirty, Applied: true}, nil
}

// pendingVersions returns those of want (ascending) that state has not
// applied. A dirty version was attempted and did not finish, so it is pending.
func pendingVersions(state migratekit.State, want []int64) []int64 {
	if !state.Applied {
		return want
	}
	recorded := int64(state.Version)
	var pending []int64
	for _, v := range want {
		if v > recorded || (state.Dirty && v == recorded) {
			pending = append(pending, v)
		}
	}
	return pending
}

// PendingMigrations returns the versions this binary embeds that the database
// has not applied, ascending — the set WaitForSchema blocks on.
func PendingMigrations(db *sql.DB) ([]int64, error) {
	want, err := EmbeddedMigrationVersions()
	if err != nil {
		return nil, err
	}
	state, err := ReadMigrationState(db)
	if err != nil {
		return nil, err
	}
	return pendingVersions(state, want), nil
}

// EmbeddedMigrationVersions returns every migration version compiled into this
// binary, ascending. Paired with PendingMigrations it gives "n of m applied"
// without a second source of truth for what m is.
func EmbeddedMigrationVersions() ([]int64, error) {
	ms, err := embeddedMigrations()
	if err != nil {
		return nil, err
	}
	versions := make([]int64, 0, len(ms))
	for _, m := range ms {
		versions = append(versions, int64(m.Version))
	}
	slices.Sort(versions)
	return versions, nil
}

func embeddedMigrations() ([]compat.Migration, error) {
	ms, err := compat.Scan(forgedb.Source(), forgedb.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded migrations: %w", err)
	}
	if len(ms) == 0 {
		return nil, errors.New("this binary embeds no migrations (db/migrations)")
	}
	return ms, nil
}

// versionLabel renders a recorded version, or "none".
func versionLabel(s migratekit.State) string {
	if !s.Applied {
		return "none"
	}
	return fmt.Sprint(s.Version)
}

func formatUints(vs []uint) string {
	out := make([]int64, len(vs))
	for i, v := range vs {
		out[i] = int64(v)
	}
	return formatVersions(out)
}
