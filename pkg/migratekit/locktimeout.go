package migratekit

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// A MIGRATION MUST NEVER WAIT FOREVER FOR A LOCK.
//
// DDL takes locks, and ALTER TABLE takes ACCESS EXCLUSIVE, which conflicts
// with everything, readers included. Postgres queues lock requests: while the
// migration waits behind one long-running SELECT (or an application
// transaction left open), every later query on the table — even a plain
// SELECT, which would not conflict with the SELECT already running — queues
// behind the MIGRATION. One slow reader plus one ALTER TABLE is a full outage
// of that table for as long as the reader runs. Without lock_timeout the
// migrator would wait indefinitely and take the table down with it.
//
// So every migration runs under a lock_timeout. The wait, and the outage it
// causes, is bounded by it; a migration that cannot get its lock fails fast,
// rolls back, and is tried again a little later, when the blocker has very
// probably finished. A migration that keeps timing out fails the run with the
// schema left CLEAN at the previous version: nothing was applied, so a
// re-run is a retry, not a recovery.
//
// WHY SESSION-LEVEL SET AND NOT SET LOCAL. SET LOCAL lasts to the end of the
// transaction, which is the right scope for a file run as one implicit
// transaction — but a file holding a lone CREATE INDEX CONCURRENTLY (the only
// way it may run) is sent as one statement OUTSIDE any transaction block,
// where SET LOCAL does nothing. Prepending it would also turn that file into
// two statements and fail it with SQLSTATE 25001. So the limits are set on
// the run's session (set_config, is_local=false) before the file's version is
// recorded and reset on EVERY way out of it — success, failure or panic-free
// early return — because the session is a connection borrowed from the
// caller's pool (Apply) and a limit left on it would follow the connection
// into the application's own queries.

// Defaults applied when Limits leaves a field zero.
const (
	// DefaultLockTimeout bounds how long one attempt at a migration waits for
	// a lock. Long enough to ride out an ordinary short transaction, short
	// enough that the pile-up it causes behind the waiting DDL is not an
	// outage.
	DefaultLockTimeout = 5 * time.Second
	// DefaultStatementTimeout bounds one statement. It is a backstop against
	// a runaway statement, not a budget for the work: generous so a real
	// backfill is not cut off, and overridable per file
	// (`-- forge:statement-timeout`) when one needs hours.
	DefaultStatementTimeout = 30 * time.Minute
	// DefaultLockAttempts is how many times a migration is tried when its
	// lock times out, the first try included.
	DefaultLockAttempts = 5
	// DefaultRetryBackoff is the wait before the second attempt; each later
	// wait doubles, up to retryBackoffMax.
	DefaultRetryBackoff = time.Second
)

// Off, as Limits.LockTimeout or StatementTimeout, disables that timeout. (Zero
// means "the default", so it cannot also mean "none".)
const Off time.Duration = -1

const retryBackoffMax = 15 * time.Second

// Limits is how a run guards the locks and the time its migrations take. The
// zero value is the defaults above. A migration file overrides the two
// timeouts for itself with `-- forge:lock-timeout` and
// `-- forge:statement-timeout` (compat.ParseTimeouts).
type Limits struct {
	// LockTimeout is lock_timeout for each attempt at a migration. Zero is
	// DefaultLockTimeout; Off disables it.
	LockTimeout time.Duration
	// StatementTimeout is statement_timeout. Zero is
	// DefaultStatementTimeout; Off disables it.
	StatementTimeout time.Duration
	// LockAttempts is how many times a migration whose lock timed out is
	// tried, the first included. Zero is DefaultLockAttempts; 1 never
	// retries.
	LockAttempts int
	// RetryBackoff is the wait before the second attempt, doubling after
	// each (with jitter, so replicas do not retry in step). Zero is
	// DefaultRetryBackoff.
	RetryBackoff time.Duration
}

// ParseLimits builds Limits from the spellings a command line or an
// environment variable carries: durations as compat.ParseTimeout reads them
// (`30s`, `5m`, `off`), "" meaning the default, and attempts 0 meaning the
// default.
func ParseLimits(lockTimeout, statementTimeout string, attempts int) (Limits, error) {
	var l Limits
	for _, f := range []struct {
		name, value string
		into        *time.Duration
	}{{"lock timeout", lockTimeout, &l.LockTimeout}, {"statement timeout", statementTimeout, &l.StatementTimeout}} {
		if strings.TrimSpace(f.value) == "" {
			continue
		}
		d, err := compat.ParseTimeout(f.value)
		if err != nil {
			return Limits{}, fmt.Errorf("%s: %w", f.name, err)
		}
		if d == 0 {
			d = Off
		}
		*f.into = d
	}
	if attempts < 0 {
		return Limits{}, fmt.Errorf("lock attempts: %d is negative — 1 never retries, 0 is the default (%d)", attempts, DefaultLockAttempts)
	}
	l.LockAttempts = attempts
	return l, nil
}

// RunOptions is what a run takes beyond its migrations and its database.
type RunOptions struct {
	Limits Limits
	// Logger receives one WARN line per attempt whose lock timed out. Nil is
	// slog.Default().
	Logger *slog.Logger
}

func (l Limits) resolved() Limits {
	if l.LockTimeout == 0 {
		l.LockTimeout = DefaultLockTimeout
	}
	if l.StatementTimeout == 0 {
		l.StatementTimeout = DefaultStatementTimeout
	}
	if l.LockAttempts <= 0 {
		l.LockAttempts = DefaultLockAttempts
	}
	if l.RetryBackoff <= 0 {
		l.RetryBackoff = DefaultRetryBackoff
	}
	return l
}

// forFile is the timeouts one migration runs under: the run's, overridden by
// the file's own directives. A declared 0 (`off`) disables the timeout.
func (l Limits) forFile(t compat.Timeouts) (lock, statement time.Duration) {
	lock, statement = l.LockTimeout, l.StatementTimeout
	if t.Lock != nil {
		lock = *t.Lock
		if lock == 0 {
			lock = Off
		}
	}
	if t.Statement != nil {
		statement = *t.Statement
		if statement == 0 {
			statement = Off
		}
	}
	return lock, statement
}

// timeoutSetting renders a timeout as postgres reads it: milliseconds, 0 for
// none.
func timeoutSetting(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// LockTimeoutError is a migration that could not get its lock in any of its
// attempts. Nothing of the migration was applied, and the schema is back at
// the version before it, clean, so running again simply tries the migration
// again.
type LockTimeoutError struct {
	Version uint
	Name    string
	// Grants is true when the statement that timed out was a grants script
	// (grants.go) and not a migration. The run's migrations are committed by
	// then, and the next run applies the grants again.
	Grants bool
	// PartlyApplied is true when the migration could not be proven
	// all-or-nothing (it controls its own transaction, runs statement by
	// statement, or builds something concurrently the migrator cannot clean
	// up). It was not retried, and part of it may be committed: the schema is
	// left dirty at Version for a human to inspect.
	PartlyApplied bool
	Attempts      int
	Timeout       time.Duration
	// Leftover names the INVALID indexes a CONCURRENTLY attempt left behind
	// and that could not be dropped while the blocker still ran (dropping
	// one waits for the same sessions the build did). They do no harm beyond
	// their write cost, and the next run drops them before it builds again.
	Leftover []string
	// Err is the last timeout, as postgres reported it. It is the bare
	// postgres error, never a golang-migrate database.Error: the driver
	// wrapper above this one would rewrap that and lose this type.
	Err error
}

func (e *LockTimeoutError) Error() string {
	var b strings.Builder
	if e.Grants {
		fmt.Fprintf(&b, "the %s could not get their lock in %d attempt(s) of %s each: another session holds a conflicting lock on a table or schema they change. The migrations of this run are committed and the grants are unchanged (the script is one statement, so it is atomic); running again applies them. Find the blocker with `SELECT pid, state, xact_start, query FROM pg_stat_activity WHERE state <> 'idle' ORDER BY xact_start`: %v",
			e.Name, e.Attempts, e.Timeout, e.Err)
		return b.String()
	}
	fmt.Fprintf(&b, "migration %s could not get its lock in %d attempt(s) of %s each: another session holds a conflicting lock — an open transaction or a long query on a table it changes",
		e.Name, e.Attempts, e.Timeout)
	if e.PartlyApplied {
		fmt.Fprintf(&b, ". This migration was not retried: it is not provably all-or-nothing (it controls its own transaction, or runs outside one), so part of it may be committed and the schema is left dirty at version %d — inspect it, finish or undo the partial migration, then clear the dirty flag with `db migrate force`", e.Version)
	} else {
		b.WriteString(". Nothing was applied and the schema is unchanged, so running again retries it")
	}
	if len(e.Leftover) > 0 {
		fmt.Fprintf(&b, " (the attempt left INVALID index(es) %s that could not be dropped while the blocker ran; the retry drops them first)",
			strings.Join(e.Leftover, ", "))
	}
	b.WriteString(". Find the blocker with `SELECT pid, state, xact_start, query FROM pg_stat_activity WHERE state <> 'idle' ORDER BY xact_start`; ")
	fmt.Fprintf(&b, "if the wait is expected, give the migration longer with `%s 30s`", compat.LockTimeoutDirective)
	fmt.Fprintf(&b, ": %v", e.Err)
	return b.String()
}

func (e *LockTimeoutError) Unwrap() error { return e.Err }

// isLockTimeout reports whether err is postgres refusing to wait any longer
// for a lock (SQLSTATE 55P03, lock_not_available).
func isLockTimeout(err error) bool {
	return lockTimeoutCause(err) != nil
}

// lockTimeoutCause is the *pgconn.PgError behind a lock timeout, or nil.
// golang-migrate's database.Error has no Unwrap, so the postgres error behind
// a failed Run is reached through its OrigErr.
func lockTimeoutCause(err error) *pgconn.PgError {
	var dbErr database.Error
	var dbErrPtr *database.Error
	switch {
	case errors.As(err, &dbErr):
		err = dbErr.OrigErr
	case errors.As(err, &dbErrPtr):
		err = dbErrPtr.OrigErr
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return pg
	}
	return nil
}

// lockGuard wraps the database driver golang-migrate runs migrations through,
// and puts every migration under the run's limits. golang-migrate calls
// SetVersion(v, true), then Run, then SetVersion(v, false), for each file;
// the guard sets the limits in the first, retries inside the second, and
// resets them in the third — or on whichever step fails.
type lockGuard struct {
	database.Driver
	conn   *sql.Conn
	source sourceSet
	limits Limits
	log    *slog.Logger
	// atomic is false when golang-migrate runs a file statement by
	// statement (x-multi-statement): a failure then leaves the statements
	// before it applied, so re-running the file is not a retry. A file that
	// commits part of itself is the same (commitRe).
	atomic bool
	// sleep is time.Sleep; a test swaps it.
	sleep func(time.Duration)

	// The migration in flight, set by SetVersion(v, true).
	active  bool
	version int
	prev    int
	lock    time.Duration
	session sessionSettings
}

func newLockGuard(conn *sql.Conn, drv database.Driver, source sourceSet, run RunOptions, atomic bool) *lockGuard {
	log := run.Logger
	if log == nil {
		log = slog.Default()
	}
	return &lockGuard{Driver: drv, conn: conn, source: source, limits: run.Limits.resolved(), log: log, atomic: atomic, sleep: time.Sleep}
}

func (g *lockGuard) file(version int) (compat.Migration, bool) {
	for _, m := range g.source {
		if int(m.Version) == version {
			return m, true
		}
	}
	return compat.Migration{}, false
}

// sessionSettings is the lock_timeout and statement_timeout a connection had
// before the run overrode them, to give back exactly. RESET would not: it
// restores the server's startup defaults, and a pooled connection that had a
// session-level SET of its own (an application's statement_timeout guard)
// would come back from the run without it.
type sessionSettings struct {
	lock, statement string
	saved           bool
}

// override saves conn's current settings and puts it under lock and statement.
func (p *sessionSettings) override(ctx context.Context, conn *sql.Conn, lock, statement time.Duration) error {
	if !p.saved {
		if err := conn.QueryRowContext(ctx, "SELECT current_setting('lock_timeout'), current_setting('statement_timeout')").Scan(&p.lock, &p.statement); err != nil {
			return fmt.Errorf("read the session's lock_timeout and statement_timeout: %w", err)
		}
		p.saved = true
	}
	if _, err := conn.ExecContext(ctx, "SELECT set_config('lock_timeout', $1, false), set_config('statement_timeout', $2, false)",
		timeoutSetting(lock), timeoutSetting(statement)); err != nil {
		return fmt.Errorf("set the lock_timeout and statement_timeout: %w", err)
	}
	return nil
}

// restore gives conn the settings override saved. A connection that cannot be
// restored is discarded, not returned to the pool carrying the run's limits.
func (p *sessionSettings) restore(conn *sql.Conn) {
	if !p.saved {
		return
	}
	p.saved = false
	if _, err := conn.ExecContext(context.Background(), "SELECT set_config('lock_timeout', $1, false), set_config('statement_timeout', $2, false)",
		p.lock, p.statement); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// set puts the session under the limits of the migration at version.
func (g *lockGuard) set(ctx context.Context, version int) error {
	f, _ := g.file(version)
	lock, statement := g.limits.forFile(f.Timeouts)
	g.lock = lock
	return g.session.override(ctx, g.conn, lock, statement)
}

// reset gives the session the settings it had before the migration.
func (g *lockGuard) reset() {
	g.active = false
	g.session.restore(g.conn)
}

func (g *lockGuard) SetVersion(version int, dirty bool) error {
	ctx := context.Background()
	if dirty && version >= 0 {
		prev, _, err := g.Driver.Version()
		if err != nil {
			return err
		}
		g.version, g.prev, g.active = version, prev, true
		if err := g.set(ctx, version); err != nil {
			g.reset()
			return err
		}
	}
	err := g.Driver.SetVersion(version, dirty)
	if err != nil || !dirty {
		g.reset()
	}
	return err
}

func (g *lockGuard) Run(migration io.Reader) error {
	body, err := io.ReadAll(migration)
	if err != nil {
		g.reset()
		return err
	}
	err = g.runWithRetry(body)
	if err != nil {
		g.reset()
	}
	return err
}

// ownedInvalidIndexes are the INVALID indexes a migration's CONCURRENTLY
// builds are responsible for, and no one else: for each CREATE INDEX
// CONCURRENTLY in the file, the index of that name that sits in its table's
// schema, is on that table, and is invalid with no build in progress.
//
// The table is resolved by postgres (to_regclass, so search_path and schema
// qualification mean what they mean to the file itself) and an index lives in
// its table's schema, so `schema_a.items_v` never selects `schema_b.items_v`.
// An invalid index of any other name, schema or table was not built by this
// file and stays, whatever else is failing in the database. One of the file's
// own name that predates the run is a leftover of an earlier failed run of it,
// which the file would otherwise trip over (or, with IF NOT EXISTS, keep).
func (g *lockGuard) ownedInvalidIndexes(ctx context.Context, creates []createIndexTarget) ([]string, error) {
	var out []string
	for _, c := range creates {
		var qualified sql.NullString
		err := g.conn.QueryRowContext(ctx, `SELECT quote_ident(n.nspname) || '.' || quote_ident(ic.relname)
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = ic.relnamespace
WHERE i.indrelid = to_regclass($1) AND ic.relname = $2 AND NOT i.indisvalid
  AND NOT EXISTS (SELECT 1 FROM pg_stat_progress_create_index p WHERE p.index_relid = i.indexrelid)`,
			c.table, c.index).Scan(&qualified)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("look for the invalid index %s a failed attempt left: %w", c.index, err)
		}
		out = append(out, qualified.String)
	}
	return out, nil
}

// dropOwned drops what ownedInvalidIndexes finds. DROP INDEX CONCURRENTLY
// waits for the same sessions the build did, so it can time out for the same
// reason.
func (g *lockGuard) dropOwned(ctx context.Context, creates []createIndexTarget) error {
	stale, err := g.ownedInvalidIndexes(ctx, creates)
	if err != nil {
		return err
	}
	for _, ix := range stale {
		if _, err := g.conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+ix); err != nil {
			return fmt.Errorf("drop the invalid index %s a failed attempt left: %w", ix, err)
		}
	}
	return nil
}

// rollback ends a transaction a file of its own BEGIN left aborted, so the
// next attempt and the bookkeeping start clean. Outside a transaction it is a
// no-op that postgres only notes.
func (g *lockGuard) rollback(ctx context.Context) {
	_, _ = g.conn.ExecContext(ctx, "ROLLBACK")
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func (g *lockGuard) runWithRetry(body []byte) error {
	ctx := context.Background()
	name := fmt.Sprintf("%d", g.version)
	if f, ok := g.file(g.version); ok {
		name = f.Name
	}
	// Retrying is safe only when a failed attempt rolled back completely, and
	// that is proven from the file, not assumed (sqlscan.go): one implicit
	// transaction, no transaction control of its own, and nothing concurrent
	// the migrator cannot clean up after.
	plan := planFile(body)
	retryable := g.atomic && plan.retryable()
	if !retryable {
		// Cleanup is for a file proven to be a lone concurrent build; a file
		// that is not proven is not touched on its behalf (sqlscan.go).
		plan.creates = nil
	}
	attempts := g.limits.LockAttempts
	if !retryable {
		attempts = 1
	}

	backoff := g.limits.RetryBackoff
	for attempt := 1; ; attempt++ {
		// A CONCURRENTLY build that timed out left its index INVALID, and the
		// build fails again on the name (or, with IF NOT EXISTS, quietly keeps
		// the invalid index and reports success). So an attempt starts by
		// dropping what the last one left. The drop waits for the same
		// transactions the build did, so it can time out for the same reason:
		// that is a failed attempt like any other.
		var err error
		if len(plan.creates) > 0 {
			err = g.dropOwned(ctx, plan.creates)
		}
		if err == nil {
			err = g.Driver.Run(bytes.NewReader(body))
			if err != nil {
				g.rollback(ctx)
			}
		}
		if err == nil {
			return nil
		}
		if !isLockTimeout(err) {
			return err
		}
		if attempt >= attempts {
			return g.exhausted(ctx, name, attempt, err, plan, retryable)
		}
		wait := jitter(backoff)
		g.log.Warn("migration lock wait timed out; retrying",
			"migration", name, "attempt", attempt, "attempts", attempts,
			"lock_timeout", timeoutSetting(g.lock), "retry_in", wait.String(), "error", err.Error())
		g.sleep(wait)
		backoff = min(backoff*2, retryBackoffMax)
	}
}

// exhausted is the end of a migration whose lock timed out every time.
//
// When the file is proven all-or-nothing (retryable), every attempt rolled
// back, so the version golang-migrate recorded as dirty — written before the
// first attempt — is a lie: nothing of the migration is applied. It is put
// back to the version before, clean, so the next run retries instead of
// refusing a "dirty" schema nobody has to repair. A file that is not proven
// stays dirty at its version: part of it may have committed, and a human
// decides.
func (g *lockGuard) exhausted(ctx context.Context, name string, attempts int, cause error, plan filePlan, retryable bool) error {
	lerr := &LockTimeoutError{Version: uint(g.version), Name: name, Attempts: attempts, Timeout: g.lock, Err: lockTimeoutCause(cause), PartlyApplied: !retryable}
	if len(plan.creates) > 0 {
		// One more try to drop the last attempt's index: the blocker may have
		// ended since, and a leftover that is gone should not be reported.
		_ = g.dropOwned(ctx, plan.creates)
		stale, err := g.ownedInvalidIndexes(ctx, plan.creates)
		if err != nil {
			return errors.Join(lerr, err)
		}
		lerr.Leftover = stale
	}
	if !retryable {
		return lerr
	}
	if err := g.Driver.SetVersion(g.prev, false); err != nil {
		return errors.Join(lerr, fmt.Errorf("put the schema back at version %d: %w", g.prev, err))
	}
	return lerr
}

// jitter spreads d by up to ±25%, so replicas that timed out together do not
// retry together.
func jitter(d time.Duration) time.Duration {
	return d - d/4 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// retrySleep is time.Sleep; a test swaps it.
var retrySleep = time.Sleep

// guardedStatement runs fn, one atomic statement on conn, under the run's
// lock_timeout and statement_timeout, retrying it when its lock times out and
// resetting the session's limits when it is done. It is the lockGuard's
// treatment of a migration file, for the statements a run issues itself.
//
// THE SEAM. Anything a run executes that takes a lock the application's
// queries also take — the grants scripts today (grants.go) — goes through
// here, so a REVOKE/GRANT convergence queues behind a long reader for at most
// lock_timeout and not for as long as the reader runs. fn MUST be a single
// statement (or otherwise all-or-nothing): a retry runs it again from the
// start. name says what it was, for the log and the error.
func guardedStatement(ctx context.Context, conn *sql.Conn, run RunOptions, name string, fn func() error) (err error) {
	limits := run.Limits.resolved()
	log := run.Logger
	if log == nil {
		log = slog.Default()
	}
	var session sessionSettings
	if serr := session.override(ctx, conn, limits.LockTimeout, limits.StatementTimeout); serr != nil {
		return fmt.Errorf("%s: %w", name, serr)
	}
	defer session.restore(conn)

	backoff := limits.RetryBackoff
	for attempt := 1; ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !isLockTimeout(err) {
			return err
		}
		if attempt >= limits.LockAttempts {
			return &LockTimeoutError{Name: name, Grants: true, Attempts: attempt, Timeout: limits.LockTimeout, Err: lockTimeoutCause(err)}
		}
		wait := jitter(backoff)
		log.Warn("lock wait timed out; retrying",
			"statement", name, "attempt", attempt, "attempts", limits.LockAttempts,
			"lock_timeout", timeoutSetting(limits.LockTimeout), "retry_in", wait.String(), "error", err.Error())
		retrySleep(wait)
		backoff = min(backoff*2, retryBackoffMax)
	}
}
