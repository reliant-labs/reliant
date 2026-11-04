package migratekit

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"
)

// ONE MIGRATION RUN PER DATABASE, WHICHEVER ENTRY POINT STARTED IT.
//
// golang-migrate serializes its own Up with a session advisory lock keyed on
// a hash of (database name, schema, table). The two entry points built its
// driver differently, and golang-migrate named the database differently in
// each: Open went through the pgx v5 driver's URL constructor, which takes the
// URL PATH ("/app"); AutoMigrate wrapped a pool connection, and that
// constructor asks current_database() ("app"). Same database, two lock keys — so
// `db migrate up` (a deploy initContainer, the dev loop's migrate job) never
// excluded AutoMigrate (a server booting with AUTO_MIGRATE) at all. Measured
// with six replicas, half through each entry point, against one database: two
// ran the same migration at once (one saw "Dirty database version"), and one
// lost a race creating schema_migrations itself (duplicate key in
// pg_type_typname_nsp_index). That is every replica of a release starting
// together — exactly the moment a deploy, or a goose cutover, happens.
//
// So migratekit takes its OWN session lock, under one fixed key both entry
// points share (advisory locks are scoped to the current database, so a fixed
// key is already per-database). It is taken before golang-migrate's driver is
// built, because building it creates schema_migrations; and Up and
// AutoMigrate hold it for the whole run, which makes adopt → check → classify
// → record → apply → record one critical section per database rather than a
// row of separately locked steps.
//
// The order is always this lock first, golang-migrate's second, so the two
// cannot deadlock; a binary from before this lock takes only the second.
// (Both entry points now build golang-migrate's driver the same way, over the
// run's own session (session.go), so its key no longer differs between them
// either; a binary from before that still uses the URL path.)
const runLockKey = 7250513029716893483

// ONE SESSION DOES THE WHOLE RUN.
//
// A run is one Postgres session, through every entry point: the run lock,
// golang-migrate's driver (bound to the session, session.go) and every record
// migratekit keeps all use the connection that holds the lock, and a run that
// is waiting for the lock holds that connection and nothing else.
//
// Two places cap sessions, and a run that took a second one deadlocked at
// both. At boot the connection is borrowed from the server's own pool, which
// a desktop or monolith build on embedded postgres may cap at ONE: a run that
// held the run lock and asked the pool for another connection waited forever
// for the one it was holding (#690). And a run through a session-mode
// PgBouncer gets one server connection per client connection, and reliant's
// prod pool has two per PgBouncer replica: a Migrator held its run-lock
// session and a second one golang-migrate opened from the DSN, so two
// `db migrate up` runs at once needed four, each sat on what it had, and
// PgBouncer's query_wait_timeout killed one after 45s. The record helpers
// take a *sql.Conn, not a *sql.DB, so a pool cannot be handed to them; and a
// Migrator's own pool is capped at one connection.
//
// golang-migrate's driver sharing the session adds one rule: no migratekit transaction may be open across a call into
// golang-migrate. Its SetVersion (behind Force, and every step of Up) opens a
// transaction of its own on the same session, and postgres does not nest
// them — the inner BEGIN is a warning, and golang-migrate's COMMIT commits
// migratekit's work early and drops its transaction-scoped locks. Where a
// record must stay locked across a golang-migrate write (adoption, Force),
// the record's lock is held at SESSION level around both, with the record's
// own transaction committed before golang-migrate runs (withSessionLock).

// lockRun waits until conn's session holds the run lock, polling (see A
// WAITER HOLDS NOTHING below). The caller keeps conn for the whole run and
// must unlockRun it.
func lockRun(ctx context.Context, conn *sql.Conn) error {
	if err := advisoryLock(ctx, conn, runLockKey); err != nil {
		return fmt.Errorf("take the migration run lock: %w", err)
	}
	return nil
}

// unlockRun releases the run lock.
func unlockRun(ctx context.Context, conn *sql.Conn) {
	advisoryUnlock(ctx, conn, runLockKey)
}

// withRunLock runs fn while conn, the run's session, holds the run lock. fn
// does the run's work on conn (see ONE SESSION above).
func withRunLock(ctx context.Context, conn *sql.Conn, fn func() error) error {
	if err := lockRun(ctx, conn); err != nil {
		return err
	}
	defer unlockRun(ctx, conn)
	return fn()
}

// withSessionLock runs fn while conn's session holds the advisory lock key:
// a record's lock that must outlive the record's transaction, because a
// golang-migrate write follows it (see ONE SESSION above). fn must finish
// every transaction it opens before it returns — the unlock runs on the same
// session, and a transaction left open would swallow it.
func withSessionLock(ctx context.Context, conn *sql.Conn, key int64, fn func() error) error {
	if err := advisoryLock(ctx, conn, key); err != nil {
		return err
	}
	defer advisoryUnlock(ctx, conn, key)
	return fn()
}

// A WAITER HOLDS NOTHING WHILE IT WAITS.
//
// Every replica of a release boots together, so all but one wait for the run
// lock while the first migrates. They used to wait inside `SELECT
// pg_advisory_lock(k)`: one statement that runs until the lock is free, and a
// running statement holds a snapshot. CREATE INDEX CONCURRENTLY waits for
// every snapshot older than its own to end before it finishes, so the index
// build waited on the waiters while the waiters waited on the run lock the
// build's session held. Postgres breaks that cycle by killing one side: a
// waiter (`take the migration run lock: … deadlock detected`, every replica
// but one failing its boot), or the build itself, leaving the schema dirty
// and the index INVALID. Measured: four replicas racing one CONCURRENTLY
// migration failed every trial.
//
// So a waiter POLLS: pg_try_advisory_lock answers at once, and between
// attempts the session is idle — no statement, no transaction, no snapshot
// for an index build to wait on. The interval starts short, so a run that
// finishes in milliseconds hands over in milliseconds, and backs off to a
// ceiling, so a run that takes an hour costs one query a second per waiter.
// The wait ends when ctx does. Through a session-mode PgBouncer it is the
// same: each poll is a query on the session the run already holds.
//
// Every advisory lock migratekit takes at session level waits this way: the
// run lock, and the record locks held across a golang-migrate write
// (withSessionLock).
const (
	lockPollFirst = 10 * time.Millisecond
	lockPollMax   = time.Second
)

// advisoryLock takes the session advisory lock key on conn, polling until it
// is free or ctx ends. conn must not be in a transaction: the wait would then
// hold that transaction's snapshot, which is the deadlock this exists to
// avoid.
func advisoryLock(ctx context.Context, conn *sql.Conn, key int64) error {
	query := fmt.Sprintf("SELECT pg_try_advisory_lock(%d)", key)
	interval := lockPollFirst
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil {
			return err
		}
		if got {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for advisory lock %d: %w", key, ctx.Err())
		case <-timer.C:
		}
		interval = min(2*interval, lockPollMax)
	}
}

// advisoryUnlock releases a session advisory lock.
//
// A session lock belongs to the server SESSION, not to the *sql.Conn handle,
// and closing the handle hands the session back to the pool still holding
// it — every later run against the database would then wait forever. So a
// connection whose unlock fails is discarded (driver.ErrBadConn) rather than
// returned; closing the session is what releases the lock then.
func advisoryUnlock(ctx context.Context, conn *sql.Conn, key int64) {
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SELECT pg_advisory_unlock(%d)", key)); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}
