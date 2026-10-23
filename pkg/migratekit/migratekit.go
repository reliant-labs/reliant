// Package migratekit is the invariant half of a forge project's `db migrate`
// command tree: opening a migrator over an embedded migration set, closing
// it, and reporting what the schema is doing.
//
// WHAT IS DELIBERATELY NOT HERE: policy. Whether a dirty schema aborts the
// run or is force-cleared, whether "nothing to apply" exits 0 or non-zero —
// those differ per deployment, so they stay in the project's own
// cmd/<bin>/cmd/db.go where an operator can read and change them.
//
// WHAT IS DELIBERATELY NOT HERE EITHER: moving DOWN. Forge rolls forward
// only, so the Migrator can apply pending migrations and nothing else — there
// is no Steps(-n), Down() or Migrate(<lower version>). A down script claims to
// undo a release and cannot account for what the release already wrote; the
// recovery is a new forward migration. This package only reports FACTS
// (Up.Changed, State.Applied, State.Dirty) and never decides what a fact
// means. That split is the whole design: a helper that returned an error for
// a dirty schema would have made "auto-force in staging" unexpressible
// without dropping the library.
//
// WHY THIS IS NOT IN serverkit. serverkit is imported by every forge server,
// and it is deliberately free of any compile-time golang-migrate dependency
// — serverkit.AutoMigrate takes a `func(*sql.DB, *slog.Logger) error` for
// exactly that reason. `db migrate` is a CLI concern, not a serving concern,
// so putting the migrator here keeps golang-migrate out of the link graph of
// every server that never runs a migration.
//
// THE MIGRATION SOURCE IS AN fs.FS, NEVER A DIRECTORY PATH. A production
// image carries the binary and nothing else — the runtime stage copies
// /app/<project> and no db/ tree — so a migrator reading `file://db/migrations`
// can only ever fail there, and a deploy-time migration step that is
// guaranteed to fail is worse than no step at all. Taking an fs.FS means the
// caller hands over its `//go:embed migrations/*.sql` FS and the same command
// works in a checkout and inside the image.
package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	// Registers the "pgx5" database driver under the scheme NormalizeDSN
	// rewrites DSNs to. The rewrite and the registration are the same fact
	// stated twice, so they live in the same package: a caller that got the
	// rewrite from here and the registration from somewhere else would
	// produce a DSN with no driver behind it.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
)

// Schemes the DSN normalizer understands. Exported so a caller (or a test)
// can assert over the same set the rewrite is driven by, rather than
// restating the string literals.
const (
	// SchemePostgres is the standard postgres:// URL scheme.
	SchemePostgres = "postgres://"
	// SchemePostgresql is the equally standard postgresql:// spelling.
	SchemePostgresql = "postgresql://"
	// SchemePGX5 is what golang-migrate's pgx v5 database driver
	// registers itself as, and therefore the only scheme it will answer
	// to. A DATABASE_URL that a human or a cloud provider wrote uses one
	// of the two above, never this one.
	SchemePGX5 = "pgx5://"
)

// NormalizeDSN rewrites a standard postgres:// or postgresql:// URL to the
// pgx5:// scheme golang-migrate's pgx v5 driver registers under.
//
// It exists because the SAME DSN has to work for two different consumers:
// the application's own pool (database/sql + the pgx stdlib driver, which
// wants postgres://) and golang-migrate's URL constructor (migrate.New, which
// resolves its database driver by URL scheme). Rather than ask an operator to
// set a second DATABASE_URL in a scheme they have never seen, the one they
// wrote is translated here. Open itself no longer goes through that
// constructor — it would open a second session (session.go) — but accepts
// either spelling.
//
// A DSN in any other scheme is returned unchanged: this is a translation for
// a known pair, not a validator. An unusable DSN produces a loud driver
// error at Open, which is a better report than a silent rewrite.
func NormalizeDSN(dsn string) string {
	for _, prefix := range []string{SchemePostgresql, SchemePostgres} {
		if len(dsn) >= len(prefix) && dsn[:len(prefix)] == prefix {
			return SchemePGX5 + dsn[len(prefix):]
		}
	}
	return dsn
}

// Options configures Open.
type Options struct {
	// FS holds the migration files — typically the project's embedded
	// `forgedb.MigrationsFS`. A nil FS is a hard error: it means the
	// binary embeds no migrations, and a migrate command that exits 0
	// against an unmigrated database is precisely the silence this
	// package exists to remove.
	FS fs.FS
	// Dir is the directory INSIDE FS holding the .sql files — "migrations"
	// for the conventional `//go:embed migrations/*.sql` layout. Empty
	// means the FS root.
	Dir string
	// DSN is the database URL. Passed through NormalizeDSN, so callers
	// hand over the DATABASE_URL they already have.
	DSN string
}

// Migrator is an open migration session — ONE Postgres session, from Open to
// Close, whatever it does in between (runlock.go, ONE SESSION). Always Close
// it.
type Migrator struct {
	m *migrate.Migrate
	// conn is that session. golang-migrate's driver is bound to it
	// (session.go), and the run lock and every record (applied.go,
	// ahead.go, adopt.go) use it. Nil on a Migrator built around a bare
	// *migrate.Migrate (stateOf), which only ever reads the version.
	conn *sql.Conn
	// db is the pool conn came from when the Migrator owns it (Open):
	// capped at one connection, and closed by Close. Nil when the session
	// is borrowed from a caller's pool (Apply), which stays the caller's.
	db *sql.DB
	// source is what this binary embeds, parsed once at Open.
	source sourceSet
	// grants is the read-only grants script this binary embeds beside its
	// migrations, "" when it embeds none (grants.go).
	grants string
}

// Open builds a Migrator over the embedded migration set in opts.FS, on one
// session to opts.DSN that it holds until Close.
//
// Both failure modes are LOUD and distinguishable, because they call for
// different fixes: a nil FS means the binary shipped without migrations
// (regenerate), while an unusable DSN means the database is unreachable or
// misaddressed (fix the environment).
//
// Open takes the run lock while it builds golang-migrate's driver, so behind
// a running migration it waits — on its one session, which is all it holds.
func Open(opts Options) (*Migrator, error) {
	if opts.FS == nil {
		return nil, fmt.Errorf("migratekit: no migration source: this binary embeds no migrations")
	}
	if opts.DSN == "" {
		return nil, fmt.Errorf("migratekit: DSN is required")
	}
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}

	cfg, err := driverConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	src, err := iofs.New(opts.FS, dir)
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations (%s): %w", dir, err)
	}
	embedded, err := scanSource(opts.FS, dir)
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("open embedded migrations (%s): %w", dir, err)
	}
	grants, _, err := grantsScript(opts.FS, dir)
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	db, err := dbtrace.Open("pgx/v5", sqlDSN(opts.DSN))
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	// The cap states the invariant, so a second session is a deadlock in
	// every test rather than a surprise behind a production pooler.
	db.SetMaxOpenConns(1)
	mg, err := openSession(context.Background(), db, src, embedded, cfg)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	mg.db = db
	mg.grants = grants
	return mg, nil
}

// openSession checks one connection out of db and binds golang-migrate to it,
// under the run lock: building the driver creates schema_migrations, which a
// concurrent run building its own would otherwise race. See runlock.go. On
// error it closes the connection and src (whose handle golang-migrate never
// took ownership of); db stays the caller's.
func openSession(ctx context.Context, db *sql.DB, src source.Driver, embedded sourceSet, cfg *postgres.Config) (*Migrator, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	var m *migrate.Migrate
	err = withRunLock(ctx, conn, func() (err error) {
		m, err = bindMigrate(ctx, conn, src, cfg)
		return err
	})
	if err != nil {
		_ = src.Close()
		_ = conn.Close()
		return nil, err
	}
	return &Migrator{m: m, conn: conn, source: embedded}, nil
}

// sqlDSN turns the DSN a caller handed Open into one database/sql's pgx
// driver accepts: the postgres:// scheme (a caller may already have
// normalized to pgx5://), with golang-migrate's own `x-` options removed —
// they configure the migrator, and the server would reject them as unknown
// run-time parameters. The same filtering golang-migrate applies before it
// connects.
func sqlDSN(dsn string) string {
	u, err := url.Parse(NormalizeDSN(dsn))
	if err != nil {
		return dsn
	}
	u.Scheme = "postgres"
	q := u.Query()
	for k := range q {
		if len(k) > 1 && k[:2] == "x-" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Close releases the migration source and the session, and closes the pool
// the session came from when Open made it.
//
// golang-migrate reports the source and the session independently and either
// can fail; every error is joined into one so a caller cannot report one and
// silently drop the other. Safe on a zero Migrator.
func (mg *Migrator) Close() error {
	if mg == nil || mg.m == nil {
		return nil
	}
	// Closing golang-migrate's driver closes the session it is bound to,
	// which hands it back to its pool.
	srcErr, sessionErr := mg.m.Close()
	var poolErr error
	if mg.db != nil {
		poolErr = mg.db.Close()
	}
	return errors.Join(srcErr, sessionErr, poolErr)
}

// State is what the schema currently reports.
type State struct {
	// Version is the migration version recorded in the schema. Only
	// meaningful when Applied is true.
	Version uint
	// Dirty means a previous migration failed PARTWAY: the version was
	// recorded but its SQL did not finish. migratekit reports it and
	// stops there — whether that aborts the run or is force-cleared is
	// the caller's policy.
	Dirty bool
	// Applied reports whether any migration has ever been applied. False
	// is a normal, healthy state for a fresh database, which is why it is
	// a field rather than an error: golang-migrate signals it as
	// ErrNilVersion, and a caller that forgot to special-case that
	// sentinel would treat an empty database as broken.
	Applied bool
}

// settled is the state the applied record vouches for. A dirty version was
// attempted and did not finish, so until a Force asserts it, it is not
// applied — every version below it is. Versions are integers, so "below the
// dirty one" is "at or below one less".
func (s State) settled() State {
	if !s.Dirty {
		return s
	}
	if s.Version == 0 {
		return State{}
	}
	return State{Version: s.Version - 1, Applied: true}
}

// State reads the current schema state, folding golang-migrate's
// ErrNilVersion sentinel into State.Applied=false. A returned error
// therefore always means the version table is genuinely unreadable.
func (mg *Migrator) State() (State, error) {
	version, dirty, err := mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read migration version: %w", err)
	}
	return State{Version: version, Dirty: dirty, Applied: true}, nil
}

// Result describes what a migration call actually did.
type Result struct {
	// Changed reports whether THIS call applied anything.
	//
	// It is taken from the return of the migrate call itself, not from a
	// before/after version comparison, because the advisory lock is taken
	// INSIDE that call: when several replicas race the same release, one
	// applies and the others are released to find nothing to do. Only the
	// call's own return distinguishes "I applied it" from "someone else
	// did"; a version diff would credit every replica.
	Changed bool
	// Before is the state as of entry, After the state on exit. Both are
	// reported so a caller can log a "N -> M" line without a second round
	// trip, and so "no change" still carries the version it is at.
	Before State
	After  State
	// Ahead is set when the schema was AHEAD of this binary — the previous
	// release meeting the next one's schema mid-deploy — and every version it
	// does not embed was declared
	// backward-compatible. Nothing was applied (Changed is false); the
	// binary's code can run. A caller should say so loudly: it is the fact
	// an operator mid-deploy most needs confirmed. A schema ahead that is
	// NOT proven compatible is a *SchemaAheadError instead, never this.
	Ahead *Ahead
	// Adopted is set when THIS call adopted a database another tool
	// migrated: the baseline that declares that history was recorded as
	// applied without running its SQL (see adopt.go). Before is then the
	// state after adoption, so Changed and Before → After describe only the
	// migrations that ran on top of it. A caller should say so: it is the
	// one run in a database's life where the version moved and no SQL did.
	Adopted *Adopted
	// NothingEmbedded is set by Apply when the binary embeds no migrations
	// (a nil FS, or a directory with no .sql in it): a project that has not
	// written its first one. Nothing was read or applied. Open refuses that
	// instead; see AutoMigrate.
	NothingEmbedded bool
	// Grants is what the run did with the read-only role's grants (grants.go):
	// nil when the binary embeds none, or when the schema was ahead of it.
	Grants *Grants
}

// Up applies every pending migration.
//
// golang-migrate's ErrNoChange is folded into Result.Changed=false rather
// than surfaced as an error: on a concurrent rollout it is the EXPECTED
// outcome for every replica that lost the advisory-lock race, so treating it
// as failure would fail most of a healthy deploy. A caller that genuinely
// wants "fail if nothing was pending" has Changed to test.
//
// A schema AHEAD of this binary (the database is at a version it does not
// embed — what rolling the app back produces) is classified before anything
// runs: a Result with Ahead set when every unknown version was declared
// backward-compatible, a *SchemaAheadError when not. golang-migrate alone
// fails both with "no migration found for version N".
//
// Before either, every version the database recorded as applied is checked
// against the file this binary embeds under that number; a different file is
// a *MigrationMismatchError and nothing runs. After applying, Up records which
// file each new version was. See applied.go.
//
// Up also records this
// binary's compatibility declarations, which is what lets an OLDER binary
// (at boot via AutoMigrate, or here) reach that verdict. See ahead.go.
//
// First of all, a database another tool migrated — or a forge history that
// `forge db squash` replaced — is ADOPTED when the embedded baseline declares
// that history (`-- forge:adopts <tool> <versions>`): the baseline is
// recorded as applied without running, and everything after it applies as
// usual. A database whose history is not the declared one is an
// *AdoptionError and nothing runs. See adopt.go. A database a forge history
// this binary does not carry built, with no declaration to adopt it by, is a
// *ForeignHistoryError (adopt_forge.go).
//
// The whole run holds migratekit's run lock, shared with AutoMigrate, so
// replicas racing a release through either entry point take turns, on the
// Migrator's one session: a run waiting its turn holds that session and
// nothing else. See runlock.go.
//
// On error the Result still says what the run did before it failed: Before,
// and Adopted when an adoption committed — a later refusal does not undo it.
func (mg *Migrator) Up() (res Result, err error) {
	ctx := context.Background()
	err = withRunLock(ctx, mg.conn, func() error {
		res, err = mg.up(ctx)
		return err
	})
	return res, err
}

// up is Up's body, run on mg.conn while it holds the run lock. Apply runs it
// too, after its dirty refusal.
func (mg *Migrator) up(ctx context.Context) (res Result, err error) {
	if res.Before, err = mg.State(); err != nil {
		return res, err
	}
	if res.Before, res.Adopted, err = adopt(ctx, mg.conn, mg.m, mg.source, res.Before); err != nil {
		return res, err
	}
	if err := refuseForeignHistory(ctx, mg.conn, mg.source, res.Before); err != nil {
		return res, err
	}
	// Settled: a schema left dirty is checked as of the last version that
	// finished, so it reaches golang-migrate's dirty refusal instead of
	// reporting the half-applied version as missing.
	if err := checkApplied(ctx, mg.conn, mg.source, res.Before.settled()); err != nil {
		return res, err
	}
	if res.Ahead, err = classifyAhead(ctx, mg.conn, mg.source, res.Before); err != nil || res.Ahead != nil {
		res.After = res.Before
		return res, err
	}
	if err := recordCompat(ctx, mg.conn, mg.source); err != nil {
		return res, err
	}
	applied, err := mg.apply(mg.m.Up)
	if err != nil {
		if recErr := recordFailedRun(ctx, mg.conn, mg.m, mg.source, res.Before); recErr != nil {
			err = errors.Join(err, recErr)
		}
		return res, err
	}
	res.Changed, res.Before, res.After = applied.Changed, applied.Before, applied.After
	if err := recordApplied(ctx, mg.conn, mg.source, res.Before, res.After); err != nil {
		return res, err
	}
	res.Grants, err = applyGrants(ctx, mg.conn, mg.grants)
	return res, err
}

// Force sets the recorded version and CLEARS the dirty flag without running
// any SQL.
//
// It is here so that "auto-force and retry" is expressible as a deployment
// policy in the caller's own db.go — the only alternative being that the
// project imports golang-migrate directly and this package stops being the
// boundary. It does not make forcing safe: the schema is whatever the failed
// migration left behind, and asserting a version does not change that.
//
// It keeps the applied record (applied.go) saying what the version now says:
// the versions the force newly asserts are recorded under this binary's files,
// and rows above the forced version are dropped (forceApplied). Without that,
// the recovery Force exists for — repair a migration that failed part-way,
// force its version, run Up — was refused, because Up found the forced version
// passed and never recorded, and reported it MISSING.
//
// It runs under the run lock, so it never interleaves with a replica's Up or a
// server's AutoMigrate. The record and the version are both written while the
// session holds appliedLockKey, as adopt does, so no reader sees the two
// disagree.
//
// The record is committed FIRST, then the version. A crash between the two
// leaves the record ahead of a version that is still the old one, and running
// the same Force again finishes the job: forceApplied is idempotent. The
// reverse order left the forced version with no record, which Up refuses as
// MISSING, and re-running the Force did not repair it: the version it starts
// from already claims the forced one, so it records nothing new.
func (mg *Migrator) Force(version int) error {
	ctx := context.Background()
	err := withRunLock(ctx, mg.conn, func() error {
		return mg.force(ctx, version)
	})
	if err != nil {
		return fmt.Errorf("force migration version %d: %w", version, err)
	}
	return nil
}

// force is Force's body, run on mg.conn while it holds the run lock.
func (mg *Migrator) force(ctx context.Context, version int) error {
	return withSessionLock(ctx, mg.conn, appliedLockKey, func() error {
		before, err := mg.State()
		if err != nil {
			return err
		}
		tx, err := mg.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := forceApplied(ctx, tx, mg.source, before, version); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return mg.m.Force(version)
	})
}

// apply is the ceremony behind Up: read state, run, fold
// ErrNoChange, read state again.
func (mg *Migrator) apply(run func() error) (Result, error) {
	before, err := mg.State()
	if err != nil {
		return Result{}, err
	}
	changed, runErr := foldNoChange(run())
	if runErr != nil {
		return Result{}, runErr
	}
	after, err := mg.State()
	if err != nil {
		return Result{}, fmt.Errorf("after apply: %w", err)
	}
	return Result{Changed: changed, Before: before, After: after}, nil
}

// foldNoChange classifies the return of a migrate call into "did it change
// anything" plus a genuine failure.
//
// Split out as a pure function because it is the one piece of apply that
// carries a decision, and it is the piece worth testing without a database:
// the fold must be NARROW. It absorbs exactly migrate.ErrNoChange — the
// expected outcome for every replica that loses an advisory-lock race during
// a concurrent rollout — and passes everything else through, because a
// caller's policy of aborting the rollout depends on seeing a real failure.
// A fold written one character wider (any error means "no change") would turn
// every broken migration into a successful deploy.
func foldNoChange(err error) (changed bool, fatal error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, migrate.ErrNoChange):
		return false, nil
	default:
		return false, err
	}
}
