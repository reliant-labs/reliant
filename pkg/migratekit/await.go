package migratekit

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"time"
)

// await.go — the read-only half of the one-migrator rule.
//
// A process that shares its database with another may leave the schema to
// that other process: the api applies the migrations, and the worker and the
// gateway WAIT for them (a root declared `//forge:root <name> migrate=wait`).
// Two appliers are safe — the run lock makes them take turns — but a waiter
// needs no DDL rights, and there is one place to look when a migration fails.
//
// The waiter reads the version table directly instead of opening a Migrator,
// which would create schema_migrations and take session locks: a waiter
// changes nothing, and may be talking to a transaction pooler.

// DefaultAwaitTimeout is how long AwaitSchema waits for another process to
// bring the schema current when AwaitOptions.Timeout is zero.
const DefaultAwaitTimeout = 5 * time.Minute

// defaultAwaitPoll is how often AwaitSchema re-reads the version.
const defaultAwaitPoll = time.Second

// AwaitOptions tunes AwaitSchema. The zero value is the default.
type AwaitOptions struct {
	// Timeout bounds the wait; zero is DefaultAwaitTimeout. A waiter that
	// never sees its schema fails loudly instead of hanging in boot.
	Timeout time.Duration
	// Poll is how often the version is re-read; zero is one second.
	Poll time.Duration
}

// SchemaPendingError is AwaitSchema giving up: the schema is not yet at the
// version this binary embeds.
type SchemaPendingError struct {
	// Want is the newest migration version the binary embeds.
	Want uint
	// State is what the schema reported last.
	State State
	// Waited is how long AwaitSchema waited.
	Waited time.Duration
}

func (e *SchemaPendingError) Error() string {
	got := "no migration applied"
	if e.State.Applied {
		got = fmt.Sprintf("schema version %d", e.State.Version)
		if e.State.Dirty {
			got += " (dirty: a migration is running, or failed partway)"
		}
	}
	return fmt.Sprintf("timed out after %s waiting for the database schema to reach migration %d (%s). "+
		"This process waits for another to apply the migrations (migrate=wait): check that the process that applies them (migrate=apply) started, and that its migration succeeded",
		e.Waited.Round(time.Second), e.Want, got)
}

// AwaitSchema blocks until db's schema is current for the migrations in fsys
// (dir inside it): clean, and at or past the newest version embedded. Past it
// is the previous release meeting the next one's schema mid deploy, which a
// waiter may always serve — the same rule an applier's schema-ahead check
// enforces for incompatible migrations is the applier's to enforce.
//
// golang-migrate applies in order, and forge's migrator refuses to pass a
// version it never applied, so the recorded version is the whole answer. A
// dirty version is not a failure here: the migrator marks the version dirty
// while it runs and clears it on commit, so a waiter sees it on every healthy
// migration.
//
// It returns nil at once when the binary embeds no migrations, ctx's error
// when ctx ends first, and a *SchemaPendingError when opts.Timeout passes.
func AwaitSchema(ctx context.Context, db *sql.DB, fsys fs.FS, dir string, opts AwaitOptions) error {
	want, err := embeddedLatest(fsys, dir)
	if err != nil || want == 0 {
		return err
	}
	timeout, poll := opts.Timeout, opts.Poll
	if timeout <= 0 {
		timeout = DefaultAwaitTimeout
	}
	if poll <= 0 {
		poll = defaultAwaitPoll
	}
	start := time.Now()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		state, err := readVersion(ctx, db)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read schema version: %w", err)
		}
		if state.Applied && !state.Dirty && state.Version >= want {
			return nil
		}
		if waited := time.Since(start); waited >= timeout {
			return &SchemaPendingError{Want: want, State: state, Waited: waited}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// AwaitMigrations is AwaitSchema bound to a binary's embedded migrations and
// logged, in the closure shape serverkit.AwaitMigrationsContext takes — the
// waiter's counterpart to AutoMigrate:
//
//	serverkit.AwaitMigrationsContext(ctx, skCfg, logger,
//		migratekit.AwaitMigrations(forgedb.Source(), forgedb.SourceDir, migratekit.AwaitOptions{}))
func AwaitMigrations(fsys fs.FS, dir string, opts AwaitOptions) func(context.Context, *sql.DB, *slog.Logger) error {
	return func(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
		want, err := embeddedLatest(fsys, dir)
		if err != nil || want == 0 {
			return err
		}
		logf(logger, "waiting for the database schema to reach the migrations this binary embeds", "want", want)
		if err := AwaitSchema(ctx, db, fsys, dir, opts); err != nil {
			return err
		}
		logf(logger, "database schema is current", "version", want)
		return nil
	}
}

// embeddedLatest is the newest migration version fsys embeds under dir, or 0
// when it embeds none (a nil fsys, or a project with no migration yet).
func embeddedLatest(fsys fs.FS, dir string) (uint, error) {
	if fsys == nil {
		return 0, nil
	}
	if dir == "" {
		dir = "."
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("reading embedded migrations (%s): %w", dir, err)
	}
	if !hasSQL(entries) {
		return 0, nil
	}
	source, err := scanSource(fsys, dir)
	if err != nil {
		return 0, err
	}
	return source.latest(), nil
}

// readVersion reads golang-migrate's version table without creating it: a
// database with no table yet reports Applied false.
func readVersion(ctx context.Context, db *sql.DB) (State, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return State{}, err
	}
	if !exists {
		return State{}, nil
	}
	var (
		version int64
		dirty   bool
	)
	err := db.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)
	if err == sql.ErrNoRows {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	if version < 0 {
		// golang-migrate's NilVersion: the table exists, nothing applied.
		return State{}, nil
	}
	return State{Version: uint(version), Dirty: dirty, Applied: true}, nil
}
