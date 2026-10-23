package migratekit

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/reliant-labs/forge/pkg/dbgrants"
)

// ForceCommand is the forge command that clears a dirty schema, named by
// AutoMigrate's dirty refusal. It is Migrator.Force over a checkout's
// migrations. Spelled out rather than imported for the reason RebaseCommand
// is, and a test in internal/cli asserts it resolves to a real command.
//
// The refusal names it beside the binary's own `db migrate force`, which a
// scaffolded binary carries: AutoMigrate runs inside a binary whose name and
// command tree it cannot know, so that one can only be described.
const ForceCommand = "forge db migrate force"

// AutoMigrate is the BOOT-TIME half of the migration story, the counterpart to
// the Migrator the `db migrate` CLI drives.
//
// WHY IT TAKES AN OPEN *sql.DB WHILE Open TAKES A DSN. The two callers have
// genuinely different handles, and converting between them is the failure
// mode this signature avoids. The server has already opened its pool by the
// time it migrates — serverkit.AutoMigrate hands that exact *sql.DB over — so
// asking for a DSN here would open a SECOND connection to the same database
// during boot, one that bypasses the pool tuning already applied. The CLI, by
// contrast, has no pool and only a URL. So the migrator that runs beside a
// live pool binds to the pool, and the one that runs alone opens its own.
//
// It returns a CLOSURE rather than running immediately because
// serverkit.AutoMigrate takes a `func(*sql.DB, *slog.Logger) error` — that
// indirection is what keeps golang-migrate out of serverkit's link graph, so
// a server that never migrates does not pay for the driver. Binding the FS
// here and the database there is what lets the project's call site be a
// single expression naming its embedded migrations.
//
// The dir argument is the path INSIDE fsys holding the .sql files
// ("migrations" for the conventional `//go:embed migrations/*.sql` layout).
//
// A nil fsys SKIPS, and this is the one place migratekit is quieter than
// Open, which refuses it. The two are answering different questions. `db
// migrate up` is an explicit instruction, so a binary that embeds nothing has
// failed to do what was asked. Boot is not: AutoMigrate runs on every startup
// of a project that may simply not have written its first migration yet, and
// failing there would make a brand-new project unable to start. Both are the
// behaviour the binary's user is entitled to expect from the command they ran.
//
// Every OTHER failure mode is LOUD. An unreadable embedded FS, an unreadable
// schema_migrations table, or a schema left dirty by a half-applied migration
// each abort startup rather than serve traffic against a database in an
// unknown state.
//
// It is Apply, logged: one line per fact the run reports (an adoption and a
// schema ahead at WARN). A caller that wants the facts as values — a `db
// migrate up` command printing what it did — calls Apply.
func AutoMigrate(fsys fs.FS, dir string) func(*sql.DB, *slog.Logger) error {
	return func(db *sql.DB, logger *slog.Logger) error {
		res, err := Apply(context.Background(), db, fsys, dir)
		logRun(logger, res, err)
		return err
	}
}

// Apply is AutoMigrate's run, returning what it did: every pending migration
// in fsys applied over ONE connection borrowed from db and given back, with
// the run lock taken on that connection — a run waiting its turn holds it and
// nothing else (runlock.go, ONE SESSION).
//
// Its policy is AutoMigrate's: a binary embedding no migrations is
// Result.NothingEmbedded, not an error, and a schema left dirty is refused
// before anything runs, naming the way out. Everything else is Migrator.Up's
// run: adoption, the applied record's refusals, the schema-ahead verdict
// (Result.Ahead, or a *SchemaAheadError), and Result.Changed folding a replica
// that lost the race into "nothing to do". As with Up, on error the Result
// still reports Before and any adoption that committed.
//
// A caller holding only a DSN — a `db migrate up` command — opens a pool,
// caps it at one connection, and hands it here: one session, and the boot
// path's verdicts.
func Apply(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) (Result, error) {
	if fsys == nil {
		// See the AutoMigrate doc comment: absent migrations are a normal
		// state at BOOT (the project has not written its first one), unlike
		// at the explicit `db migrate up` that Open refuses.
		return Result{NothingEmbedded: true}, nil
	}
	if dir == "" {
		dir = "."
	}

	// An embedded-FS read error is a build/packaging bug (corrupt embed,
	// renamed dir) — fail. Only a genuinely empty migration set skips: that
	// is the honest state of a project whose first migration is unwritten.
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return Result{}, fmt.Errorf("reading embedded migrations (%s): %w", dir, err)
	}
	if !hasSQL(entries) {
		return Result{NothingEmbedded: true}, nil
	}
	embedded, err := scanSource(fsys, dir)
	if err != nil {
		return Result{}, err
	}
	src, err := iofs.New(fsys, dir)
	if err != nil {
		return Result{}, fmt.Errorf("creating migration source (%s): %w", dir, err)
	}

	// The migrator runs on ONE connection borrowed from the pool the caller
	// already opened (see the AutoMigrate doc comment), and gives it back
	// when it is done.
	//
	// Not golang-migrate's postgres.WithInstance, although it also takes the
	// pool: it checks out a dedicated *sql.Conn and releases it only in
	// Close, and its Close also closes the *sql.DB — the caller's live pool.
	// So the migrator could never be closed, and that connection stayed
	// checked out for the life of the process: one slot of MaxOpenConns gone
	// and one idle server session held on every server that ever booted. It
	// also stops anyone using the database as a CREATE DATABASE ... TEMPLATE
	// (refused while a session is connected), even after the pool is closed.
	// openSession binds golang-migrate to a connection it checks out itself,
	// and Close then closes only that connection, which returns it to the
	// pool.
	grants, _, err := grantsScript(fsys, dir)
	if err != nil {
		_ = src.Close()
		return Result{}, err
	}
	mg, err := openSession(ctx, db, src, embedded, &postgres.Config{})
	if err != nil {
		return Result{}, err
	}
	mg.grants = grants
	defer func() { _ = mg.Close() }()

	var res Result
	err = withRunLock(ctx, mg.conn, func() error {
		res, err = mg.boot(ctx)
		return err
	})
	return res, err
}

// boot is Apply's run, on mg.conn while it holds the run lock: refuse a dirty
// schema, then the run Up makes.
func (mg *Migrator) boot(ctx context.Context) (Result, error) {
	before, err := mg.State()
	if err != nil {
		return Result{}, err
	}
	// A dirty schema means a previous migration failed partway. Running more
	// migrations (or serving traffic) against it compounds the damage — stop
	// and make a human look. The CLI's db.go can express the other policy
	// (force and retry) because Force is exported; boot deliberately cannot.
	if before.Dirty {
		return Result{Before: before, After: before}, fmt.Errorf("database schema is dirty at version %d — a previous migration failed partway; "+
			"inspect the database, finish or undo the partial migration, then clear the dirty flag before restarting: "+
			"`%s %d` from a checkout of this release, or this binary's own `db migrate force %d` where it has one",
			before.Version, ForceCommand, before.Version, before.Version)
	}
	// A database another tool migrated (goose) is adopted; a version
	// applied from a different file than this binary's, or one the schema
	// passed without applying, refuses; a schema AHEAD of this binary — the
	// previous release booting against the next one's database, every
	// rolling deploy until the old ReplicaSet is gone — serves only if every
	// unknown version was declared backward-compatible. See Migrator.Up.
	res, err := mg.up(ctx)
	if err != nil {
		return res, fmt.Errorf("running migrations: %w", err)
	}
	return res, nil
}

// logRun logs what Apply reported, for AutoMigrate.
func logRun(logger *slog.Logger, res Result, err error) {
	if res.NothingEmbedded {
		logf(logger, "no migrations embedded, skipping auto-migrate")
		return
	}
	if a := res.Adopted; a != nil {
		warnf(logger, "ADOPTED a database whose migration history the baseline replaces: its history matches the baseline's declaration exactly, so the baseline was recorded as applied WITHOUT running its SQL; the history's own records are left in place",
			"tool", a.Tool, "baseline", a.Name, "version", a.Version, "verified_versions", a.Versions)
	}
	switch {
	case err != nil:
		// Returned to the caller, which reports it.
	case res.Ahead != nil:
		warnf(logger, "database schema is AHEAD of this binary (an older release mid-deploy) — every newer version was declared backward-compatible, so nothing is applied and startup continues",
			"schema_version", res.Ahead.Version, "binary_latest", res.Ahead.Latest, "unknown_versions", joinVersions(res.Ahead.Versions))
	case res.Changed:
		logf(logger, "migrations applied", "from_version", res.Before.Version, "to_version", res.After.Version)
	default:
		logf(logger, "no pending migrations", "version", res.After.Version)
	}
	if g := res.Grants; g != nil && err == nil {
		switch {
		case g.RoleMissing:
			logf(logger, "read-only role not provisioned in this database: read-only grants not applied", "role", dbgrants.RoleName)
		case g.Applied:
			logf(logger, "read-only grants applied", "role", dbgrants.RoleName)
		}
	}
}

// hasSQL reports whether the entry set holds at least one .sql file.
//
// It looks for SQL rather than testing len(entries)==0 because the directory
// can legitimately carry non-SQL company — a .gitkeep holding an
// otherwise-empty dir in git is the common one — and treating that as "there
// are migrations" sends an empty set to iofs.New, which fails.
func hasSQL(entries []fs.DirEntry) bool {
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".sql" {
			return true
		}
	}
	return false
}

// stateOf reads a migrator's state, folding ErrNilVersion into Applied=false.
// Shared with the Migrator.State method so the CLI and boot paths cannot
// disagree about what a fresh database looks like.
func stateOf(m *migrate.Migrate) (State, error) {
	return (&Migrator{m: m}).State()
}

// logf tolerates a nil logger so a caller that has not built one yet (or a
// test) can call AutoMigrate without constructing logging ceremony.
func logf(logger *slog.Logger, msg string, args ...any) {
	if logger != nil {
		logger.Info(msg, args...)
	}
}

// warnf is logf at WARN, for a state that is healthy but must not be missed.
func warnf(logger *slog.Logger, msg string, args ...any) {
	if logger != nil {
		logger.Warn(msg, args...)
	}
}
