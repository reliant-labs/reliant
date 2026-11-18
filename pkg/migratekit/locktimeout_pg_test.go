package migratekit

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// These tests hold a lock in another session and watch what the migrator does
// about it. They run against real postgres (requirePG): lock_timeout, the
// lock queue and CREATE INDEX CONCURRENTLY's waits are the server's behaviour,
// and nothing else can stand in for them.

var lockBase = map[string]string{
	"1_items.up.sql": "CREATE TABLE items (id INT PRIMARY KEY, v TEXT);\n" +
		"INSERT INTO items SELECT g, md5(g::text) FROM generate_series(1, 2000) g;\n" +
		"CREATE TABLE other (id INT PRIMARY KEY);",
}

const addColumn = "-- forge:backward-compatible\nALTER TABLE items ADD COLUMN note TEXT;"

// holdLock opens a session, starts a transaction at isolation, runs stmt in it
// and leaves it open: the long-running reader (or writer) a migration's DDL
// queues behind. The returned release ends it; it is also run at cleanup.
func holdLock(t *testing.T, dsn, isolation, stmt string) (release func()) {
	t.Helper()
	conn, err := openRecordDB(t, dsn).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"BEGIN ISOLATION LEVEL " + isolation, stmt} {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// holdReader is a session with an open transaction that has read items: it
// holds ACCESS SHARE on it, which blocks ALTER TABLE.
func holdReader(t *testing.T, dsn string) func() {
	return holdLock(t, dsn, "READ COMMITTED", "SELECT count(*) FROM items")
}

// holdSnapshot is a session with an open repeatable-read transaction that has
// read another table: no lock on items, but a snapshot older than any index
// build that starts now, which CREATE INDEX CONCURRENTLY waits out.
func holdSnapshot(t *testing.T, dsn string) func() {
	return holdLock(t, dsn, "REPEATABLE READ", "SELECT count(*) FROM other")
}

func baseSchema(t *testing.T) string {
	t.Helper()
	dsn := requirePG(t)
	mustUp(t, release(lockBase), dsn)
	return dsn
}

func upWith(fsys fstest.MapFS, dsn string, run RunOptions) (Result, error) {
	mg, err := Open(Options{FS: fsys, Dir: "migrations", DSN: dsn, Run: run})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = mg.Close() }()
	return mg.Up()
}

func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

// releaseOnRetry is the log of a run that a blocker holds up: the first
// record carrying every one of marks ends the blocker, synchronously, before
// the run sleeps and tries again. A test that asserts the run retried and
// then succeeded is then deterministic. Releasing on a timer raced the first
// attempt: on a loaded machine the run could reach the blocked statement only
// after the timer had fired, take the lock at once, and never retry.
type releaseOnRetry struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	marks    []string
	release  func()
	released bool
}

func newReleaseOnRetry(release func(), marks ...string) *releaseOnRetry {
	return &releaseOnRetry{release: release, marks: marks}
}

// Write receives one slog record per call (slog's TextHandler writes each
// record whole).
func (w *releaseOnRetry) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.released {
		match := true
		for _, m := range w.marks {
			match = match && bytes.Contains(p, []byte(m))
		}
		if match {
			w.released = true
			w.release()
		}
	}
	return w.buf.Write(p)
}

func (w *releaseOnRetry) logger() *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

func (w *releaseOnRetry) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func stateAt(t *testing.T, dsn string) State {
	t.Helper()
	mg, err := Open(Options{FS: release(lockBase), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mg.Close() }()
	st, err := mg.State()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func timed(f func()) time.Duration {
	start := time.Now()
	f()
	return time.Since(start)
}

func hasColumn(t *testing.T, dsn, column string) bool {
	t.Helper()
	var n int
	if err := openRecordDB(t, dsn).QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'items' AND column_name = $1`, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// TestMigrationFailsFastOnLockTimeoutAndBlocksNobody is the outage this exists
// to prevent. A session holds ACCESS SHARE on items; the migration's ALTER
// TABLE queues for ACCESS EXCLUSIVE; and every query on items after it queues
// behind the ALTER — even a plain SELECT, which does not conflict with the
// reader already running. Without lock_timeout that lasts as long as the
// reader. With it, the pile-up is bounded by the timeout, the migration fails
// with a clear error, and nothing stays blocked.
func TestMigrationFailsFastOnLockTimeoutAndBlocksNobody(t *testing.T) {
	dsn := baseSchema(t)
	holdReader(t, dsn)
	const lockTimeout = 600 * time.Millisecond

	var (
		upErr    error
		upTook   time.Duration
		probeErr error
		probe    time.Duration
		wg       sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(200 * time.Millisecond) // the ALTER is queued by now
		probe = timed(func() {
			probeErr = openRecordDB(t, dsn).QueryRow("SELECT count(*) FROM items").Scan(new(int))
		})
	}()
	upTook = timed(func() {
		_, upErr = upWith(release(merged(lockBase, map[string]string{"2_note.up.sql": addColumn})), dsn,
			RunOptions{Limits: Limits{LockTimeout: lockTimeout, LockAttempts: 1}})
	})
	wg.Wait()

	var lerr *LockTimeoutError
	if !errors.As(upErr, &lerr) {
		t.Fatalf("Up returned %v, want a *LockTimeoutError", upErr)
	}
	if lerr.Version != 2 || lerr.Attempts != 1 || lerr.Timeout != lockTimeout {
		t.Errorf("error says version=%d attempts=%d timeout=%s, want 2, 1, %s", lerr.Version, lerr.Attempts, lerr.Timeout, lockTimeout)
	}
	if upTook < lockTimeout || upTook > lockTimeout+5*time.Second {
		t.Errorf("Up took %s, want about the lock timeout (%s)", upTook, lockTimeout)
	}
	// The probe queued behind the waiting ALTER for at most what was left of
	// the timeout, and was answered the moment the ALTER gave up.
	if probeErr != nil {
		t.Errorf("a query on the table failed: %v", probeErr)
	}
	if probe > lockTimeout+2*time.Second {
		t.Errorf("a query on the table was blocked for %s behind the migration; its lock_timeout is %s", probe, lockTimeout)
	}
	// Once the migration is gone nothing waits at all, though the reader is
	// still there.
	if after := timed(func() {
		if err := openRecordDB(t, dsn).QueryRow("SELECT count(*) FROM items").Scan(new(int)); err != nil {
			t.Error(err)
		}
	}); after > 500*time.Millisecond {
		t.Errorf("a query after the failed migration took %s", after)
	}

	// Nothing was applied, and the schema is clean at the version before.
	if st := stateAt(t, dsn); st.Dirty || st.Version != 1 {
		t.Errorf("schema is %+v, want clean at version 1", st)
	}
	if hasColumn(t, dsn, "note") {
		t.Error("the failed migration's column exists")
	}
}

// TestMigrationRetriesUntilTheBlockerReleases: the same blocker, ended while
// the migrator is between attempts. The run succeeds, and says what it did.
func TestMigrationRetriesUntilTheBlockerReleases(t *testing.T) {
	dsn := baseSchema(t)
	logs := newReleaseOnRetry(holdReader(t, dsn), "migration lock wait timed out; retrying")
	res, err := upWith(release(merged(lockBase, map[string]string{"2_note.up.sql": addColumn})), dsn,
		RunOptions{Logger: logs.logger(), Limits: Limits{LockTimeout: 250 * time.Millisecond, LockAttempts: 10, RetryBackoff: 100 * time.Millisecond}})
	if err != nil {
		t.Fatalf("Up: %v\nlogs:\n%s", err, logs.String())
	}
	if !res.Changed || res.After.Version != 2 || res.After.Dirty {
		t.Errorf("result %+v, want changed, clean at 2", res)
	}
	if !hasColumn(t, dsn, "note") {
		t.Error("the migration's column is missing")
	}
	if n := strings.Count(logs.String(), "migration lock wait timed out; retrying"); n < 1 {
		t.Errorf("no retry was logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "migration=2_note.up.sql") || !strings.Contains(logs.String(), "attempt=1") {
		t.Errorf("a retry line does not name the migration and attempt:\n%s", logs.String())
	}
}

// TestMigrationGivesUpAfterItsAttempts: a blocker that never goes away fails
// the run after the bounded number of attempts, each one logged, and the next
// run — with the blocker gone — applies the migration.
func TestMigrationGivesUpAfterItsAttempts(t *testing.T) {
	dsn := baseSchema(t)
	releaseReader := holdReader(t, dsn)
	fsys := release(merged(lockBase, map[string]string{"2_note.up.sql": addColumn}))

	var logs bytes.Buffer
	_, err := upWith(fsys, dsn, RunOptions{Logger: logTo(&logs),
		Limits: Limits{LockTimeout: 150 * time.Millisecond, LockAttempts: 3, RetryBackoff: 50 * time.Millisecond}})
	var lerr *LockTimeoutError
	if !errors.As(err, &lerr) || lerr.Attempts != 3 {
		t.Fatalf("Up returned %v, want a *LockTimeoutError after 3 attempts", err)
	}
	if n := strings.Count(logs.String(), "retrying"); n != 2 {
		t.Errorf("%d retries logged for 3 attempts, want 2:\n%s", n, logs.String())
	}
	if st := stateAt(t, dsn); st.Dirty || st.Version != 1 {
		t.Fatalf("schema is %+v, want clean at version 1: a timed-out migration must not look half-applied", st)
	}

	releaseReader()
	res, err := upWith(fsys, dsn, RunOptions{})
	if err != nil || !res.Changed || res.After.Version != 2 {
		t.Fatalf("rerun after the blocker ended: %+v, %v", res, err)
	}
}

// TestFileLockTimeoutDirectiveOverridesTheRun: a file's `-- forge:lock-timeout`
// replaces the run's, in both directions.
func TestFileLockTimeoutDirectiveOverridesTheRun(t *testing.T) {
	t.Run("longer", func(t *testing.T) {
		dsn := baseSchema(t)
		time.AfterFunc(700*time.Millisecond, holdReader(t, dsn))
		var logs bytes.Buffer
		// The run would give up after 100ms and never retry; the file waits.
		file := "-- forge:lock-timeout 20s\n" + addColumn
		_, err := upWith(release(merged(lockBase, map[string]string{"2_note.up.sql": file})), dsn,
			RunOptions{Logger: logTo(&logs), Limits: Limits{LockTimeout: 100 * time.Millisecond, LockAttempts: 1}})
		if err != nil {
			t.Fatalf("Up: %v\n%s", err, logs.String())
		}
		if !hasColumn(t, dsn, "note") {
			t.Error("column missing")
		}
	})
	t.Run("shorter", func(t *testing.T) {
		dsn := baseSchema(t)
		holdReader(t, dsn)
		file := "-- forge:lock-timeout 200ms\n" + addColumn
		var err error
		took := timed(func() {
			_, err = upWith(release(merged(lockBase, map[string]string{"2_note.up.sql": file})), dsn,
				RunOptions{Limits: Limits{LockTimeout: time.Minute, LockAttempts: 1}})
		})
		var lerr *LockTimeoutError
		if !errors.As(err, &lerr) || lerr.Timeout != 200*time.Millisecond {
			t.Fatalf("Up returned %v, want a *LockTimeoutError at the file's 200ms", err)
		}
		if took > 10*time.Second {
			t.Errorf("took %s despite a 200ms file timeout", took)
		}
	})
	t.Run("off", func(t *testing.T) {
		dsn := baseSchema(t)
		time.AfterFunc(700*time.Millisecond, holdReader(t, dsn))
		file := "-- forge:lock-timeout off\n" + addColumn
		if _, err := upWith(release(merged(lockBase, map[string]string{"2_note.up.sql": file})), dsn,
			RunOptions{Limits: Limits{LockTimeout: 100 * time.Millisecond, LockAttempts: 1}}); err != nil {
			t.Fatalf("a file with the timeout off should wait for the lock: %v", err)
		}
	})
}

// TestStatementTimeoutAndItsOverride: the run's statement_timeout cuts off a
// runaway statement, and a file's directive lets a real backfill take longer.
func TestStatementTimeoutAndItsOverride(t *testing.T) {
	dsn := baseSchema(t)
	slow := "SELECT pg_sleep(1.5);"
	limits := Limits{StatementTimeout: 400 * time.Millisecond}

	_, err := upWith(release(merged(lockBase, map[string]string{"2_slow.up.sql": slow})), dsn, RunOptions{Limits: limits})
	if err == nil || !strings.Contains(err.Error(), "statement timeout") {
		t.Fatalf("a statement over the run's statement_timeout returned %v, want a statement timeout error", err)
	}
	if errors.As(err, new(*LockTimeoutError)) {
		t.Error("a statement timeout was reported as a lock timeout")
	}

	// The failed migration left the schema dirty at 2, as any failed
	// migration does (it is not a lock timeout, so nothing was rolled back
	// to a clean version). Start from a fresh database for the override.
	dsn = baseSchema(t)
	file := "-- forge:statement-timeout 30s — a backfill\n" + slow
	if _, err := upWith(release(merged(lockBase, map[string]string{"2_slow.up.sql": file})), dsn, RunOptions{Limits: limits}); err != nil {
		t.Fatalf("the file's statement-timeout should allow its statement: %v", err)
	}
}

// TestRunLimitsDoNotOutliveTheRun: Apply (and so AutoMigrate) borrows its
// session from the application's pool. A lock_timeout left on that connection
// would follow it into the application's queries.
func TestRunLimitsDoNotOutliveTheRun(t *testing.T) {
	dsn := baseSchema(t)
	pool := openRecordDB(t, dsn)
	pool.SetMaxOpenConns(1)

	read := func() (lock, statement string) {
		t.Helper()
		if err := pool.QueryRow("SHOW lock_timeout").Scan(&lock); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow("SHOW statement_timeout").Scan(&statement); err != nil {
			t.Fatal(err)
		}
		return lock, statement
	}
	wantLock, wantStatement := read()

	fsys := release(merged(lockBase, map[string]string{"2_note.up.sql": addColumn}))
	if _, err := ApplyWith(context.Background(), pool, fsys, "migrations",
		RunOptions{Limits: Limits{LockTimeout: 7 * time.Second, StatementTimeout: 9 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if lock, statement := read(); lock != wantLock || statement != wantStatement {
		t.Errorf("the pooled connection kept the run's limits: lock_timeout=%s statement_timeout=%s, were %s and %s", lock, statement, wantLock, wantStatement)
	}

	// And the failure path resets too.
	failing := release(merged(lockBase, map[string]string{"2_note.up.sql": addColumn, "3_bad.up.sql": "SELECT * FROM nowhere;"}))
	if _, err := ApplyWith(context.Background(), pool, failing, "migrations",
		RunOptions{Limits: Limits{LockTimeout: 7 * time.Second}}); err == nil {
		t.Fatal("a migration selecting from a missing table succeeded")
	}
	if lock, _ := read(); lock != wantLock {
		t.Errorf("a failed migration left lock_timeout=%s on the connection, was %s", lock, wantLock)
	}
}

// TestRunsUnderTheDefaultLockTimeout: with nothing configured, a migration
// still runs under a lock_timeout; the session setting is observable from
// inside the migration.
func TestRunsUnderTheDefaultLockTimeout(t *testing.T) {
	dsn := baseSchema(t)
	probe := "-- forge:backward-compatible\n" +
		"CREATE TABLE seen AS SELECT current_setting('lock_timeout') AS lock_timeout, current_setting('statement_timeout') AS statement_timeout;"
	if _, err := upWith(release(merged(lockBase, map[string]string{"2_seen.up.sql": probe})), dsn, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	var lock, statement string
	if err := openRecordDB(t, dsn).QueryRow("SELECT lock_timeout, statement_timeout FROM seen").Scan(&lock, &statement); err != nil {
		t.Fatal(err)
	}
	if lock != "5s" || statement != "30min" {
		t.Errorf("a migration ran under lock_timeout=%s statement_timeout=%s, want 5s and 30min", lock, statement)
	}
}

// indexState is whether items_v exists, and whether it is valid.
func indexState(t *testing.T, db *sql.DB) (exists, valid bool) {
	t.Helper()
	err := db.QueryRow(`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = 'items_v'`).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true, valid
}

const concurrentlyIndex = "-- forge:backward-compatible\nCREATE INDEX CONCURRENTLY items_v ON items (v);"

// TestConcurrentlyIndexRetriesAndLeavesNoInvalidIndex: CREATE INDEX
// CONCURRENTLY must be the only statement in its file, so it runs outside any
// transaction block, where SET LOCAL does nothing — and it waits for every
// older snapshot, so an idle-in-transaction session stalls it. It still gets
// the lock timeout; each timed-out attempt leaves the index INVALID, which the
// next attempt drops before building again. After the blocker goes, the index
// is valid.
func TestConcurrentlyIndexRetriesAndLeavesNoInvalidIndex(t *testing.T) {
	dsn := baseSchema(t)
	logs := newReleaseOnRetry(holdSnapshot(t, dsn), "migration lock wait timed out; retrying")
	res, err := upWith(release(merged(lockBase, map[string]string{"2_items_v.up.sql": concurrentlyIndex})), dsn,
		RunOptions{Logger: logs.logger(), Limits: Limits{LockTimeout: 300 * time.Millisecond, LockAttempts: 10, RetryBackoff: 100 * time.Millisecond}})
	if err != nil {
		t.Fatalf("Up: %v\nlogs:\n%s", err, logs.String())
	}
	if !res.Changed || res.After.Version != 2 || res.After.Dirty {
		t.Errorf("result %+v, want changed, clean at 2", res)
	}
	if exists, valid := indexState(t, openRecordDB(t, dsn)); !exists || !valid {
		t.Errorf("items_v exists=%v valid=%v, want a valid index", exists, valid)
	}
	if !strings.Contains(logs.String(), "migration lock wait timed out; retrying") {
		t.Errorf("the build never timed out, so the retry path was not exercised:\n%s", logs.String())
	}
}

// TestConcurrentlyIndexGivingUpCleansUpItsInvalidIndex: when the attempts run
// out, the INVALID index the last build left is dropped, the schema is clean
// at the version before, and a rerun builds the index.
func TestConcurrentlyIndexGivingUpCleansUpItsInvalidIndex(t *testing.T) {
	dsn := baseSchema(t)
	releaseSnapshot := holdSnapshot(t, dsn)
	fsys := release(merged(lockBase, map[string]string{"2_items_v.up.sql": concurrentlyIndex}))

	_, err := upWith(fsys, dsn, RunOptions{Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 2, RetryBackoff: 50 * time.Millisecond}})
	var lerr *LockTimeoutError
	if !errors.As(err, &lerr) {
		t.Fatalf("Up returned %v, want a *LockTimeoutError", err)
	}
	if len(lerr.Leftover) != 0 {
		t.Errorf("an invalid index could not be dropped although nothing blocked the drop: %v", lerr.Leftover)
	}
	if exists, _ := indexState(t, openRecordDB(t, dsn)); exists {
		t.Error("the timed-out build left items_v behind")
	}
	if st := stateAt(t, dsn); st.Dirty || st.Version != 1 {
		t.Fatalf("schema is %+v, want clean at version 1", st)
	}

	releaseSnapshot()
	if res, err := upWith(fsys, dsn, RunOptions{}); err != nil || !res.Changed {
		t.Fatalf("rerun after the blocker ended: %+v, %v", res, err)
	}
	if exists, valid := indexState(t, openRecordDB(t, dsn)); !exists || !valid {
		t.Errorf("items_v exists=%v valid=%v after the rerun", exists, valid)
	}
}

// TestConcurrentlyLeftoverIsReportedWhenItCannotBeDropped: dropping an index
// concurrently waits for readers of the table, so a reader that outlasts the
// attempts can keep the invalid index from being dropped. The error names it
// and says the retry drops it — and the retry does.
func TestConcurrentlyLeftoverIsReportedWhenItCannotBeDropped(t *testing.T) {
	dsn := baseSchema(t)
	releaseSnapshot := holdSnapshot(t, dsn)
	releaseReader := holdReader(t, dsn)
	fsys := release(merged(lockBase, map[string]string{"2_items_v.up.sql": concurrentlyIndex}))

	_, err := upWith(fsys, dsn, RunOptions{Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 1}})
	var lerr *LockTimeoutError
	if !errors.As(err, &lerr) {
		t.Fatalf("Up returned %v, want a *LockTimeoutError", err)
	}
	if len(lerr.Leftover) != 1 || !strings.HasSuffix(lerr.Leftover[0], ".items_v") {
		t.Fatalf("Leftover = %v, want the one invalid index", lerr.Leftover)
	}
	if !strings.Contains(err.Error(), "INVALID") {
		t.Errorf("the error does not mention the invalid index: %v", err)
	}
	if st := stateAt(t, dsn); st.Dirty || st.Version != 1 {
		t.Fatalf("schema is %+v, want clean at version 1", st)
	}

	releaseSnapshot()
	releaseReader()
	if _, err := upWith(fsys, dsn, RunOptions{}); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if exists, valid := indexState(t, openRecordDB(t, dsn)); !exists || !valid {
		t.Errorf("items_v exists=%v valid=%v after the rerun", exists, valid)
	}
}

// TestConcurrentlyKeepsAnInvalidIndexItDidNotBuild: the cleanup drops what the
// migration's attempts left, not every invalid index in the database.
func TestConcurrentlyKeepsAnInvalidIndexItDidNotBuild(t *testing.T) {
	dsn := baseSchema(t)
	db := openRecordDB(t, dsn)
	for _, q := range []string{
		"CREATE INDEX items_stranger ON items (id, v)",
		"UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'items_stranger'::regclass",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	releaseSnapshot := holdSnapshot(t, dsn)
	time.AfterFunc(700*time.Millisecond, releaseSnapshot)

	if _, err := upWith(release(merged(lockBase, map[string]string{"2_items_v.up.sql": concurrentlyIndex})), dsn,
		RunOptions{Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 10, RetryBackoff: 50 * time.Millisecond}}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pg_class WHERE relname = 'items_stranger'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("another migration's invalid index was dropped (count=%d, err=%v)", n, err)
	}
}

// TestOnlyLockTimeoutsAreRetried: any other failure is a failure on the first
// attempt.
func TestOnlyLockTimeoutsAreRetried(t *testing.T) {
	dsn := baseSchema(t)
	var logs bytes.Buffer
	_, err := upWith(release(merged(lockBase, map[string]string{"2_bad.up.sql": "ALTER TABLE items ADD COLUMN id INT;"})), dsn,
		RunOptions{Logger: logTo(&logs), Limits: Limits{RetryBackoff: time.Millisecond}})
	if err == nil || errors.As(err, new(*LockTimeoutError)) {
		t.Fatalf("Up returned %v, want the duplicate-column error", err)
	}
	if strings.Contains(logs.String(), "retrying") {
		t.Errorf("a non-lock failure was retried:\n%s", logs.String())
	}
}

// TestMalformedTimeoutDirectiveRefusesTheRun: a directive the migrator cannot
// read fails Open, the way a malformed adoption does, instead of being ignored.
func TestMalformedTimeoutDirectiveRefusesTheRun(t *testing.T) {
	dsn := requirePG(t)
	_, err := upWith(release(map[string]string{"1_a.up.sql": "-- forge:lock-timeout whenever\nCREATE TABLE a (id INT);"}), dsn, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "forge:lock-timeout") {
		t.Fatalf("got %v, want an error naming the directive", err)
	}
}

// grantsBlocker is a session with an open transaction that granted on the
// public schema: it holds the schema's catalog row, so the grants script's
// own GRANT USAGE ON SCHEMA public waits for it exactly as it would behind any
// session that touched what the script converges.
func grantsBlocker(t *testing.T, dsn string) (release func()) {
	t.Helper()
	admin := openRecordDB(t, dsn)
	name := fmt.Sprintf("forge_blk_t%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	if _, err := admin.Exec("CREATE ROLE " + name + " NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP OWNED BY " + name)
		_, _ = admin.Exec("DROP ROLE IF EXISTS " + name)
	})
	return holdLock(t, dsn, "READ COMMITTED", "GRANT USAGE ON SCHEMA public TO "+name)
}

func grantsFixture(t *testing.T) (dsn string, fsys fstest.MapFS) {
	t.Helper()
	dsn = requirePG(t)
	useRole(t)
	withRole(t, dsn)
	grants := mustRender(t, usersTable(true), notesTable)
	return dsn, grantsRelease(map[string]string{"1_users.up.sql": usersSQL, "2_notes.up.sql": notesSQL}, grants)
}

// TestGrantsFailFastBehindABlockerAndTheNextRunAppliesThem: the grants script
// is REVOKE/GRANT, and takes locks too. Behind a session holding what it
// converges it waits for lock_timeout, not for as long as that session lives;
// the run fails with a *LockTimeoutError that says it was the grants, the
// migrations stay committed, and the next run applies the grants.
func TestGrantsFailFastBehindABlockerAndTheNextRunAppliesThem(t *testing.T) {
	dsn, fsys := grantsFixture(t)
	releaseBlocker := grantsBlocker(t, dsn)

	var err error
	took := timed(func() {
		_, err = upWith(fsys, dsn, RunOptions{Limits: Limits{LockTimeout: 300 * time.Millisecond, LockAttempts: 2, RetryBackoff: 50 * time.Millisecond}})
	})
	var lerr *LockTimeoutError
	if !errors.As(err, &lerr) || !lerr.Grants || lerr.Attempts != 2 {
		t.Fatalf("Up returned %v, want a grants *LockTimeoutError after 2 attempts", err)
	}
	if !strings.Contains(err.Error(), "read-only grants") {
		t.Errorf("the error does not name the grants: %v", err)
	}
	if took > 15*time.Second {
		t.Errorf("Up took %s behind a blocker with a 300ms lock timeout", took)
	}
	if st := stateAt(t, dsn); st.Dirty || st.Version != 2 {
		t.Errorf("schema is %+v, want the run's migrations committed, clean at 2", st)
	}

	releaseBlocker()
	res, err := upWith(fsys, dsn, RunOptions{})
	if err != nil || res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("rerun: %+v, %v, want the grants applied", res.Grants, err)
	}
}

// TestGrantsRetryUntilTheBlockerReleases: the same blocker, ended between
// attempts. The run succeeds and the role has its grants.
func TestGrantsRetryUntilTheBlockerReleases(t *testing.T) {
	dsn, fsys := grantsFixture(t)
	logs := newReleaseOnRetry(grantsBlocker(t, dsn), "lock wait timed out; retrying", `statement="read-only grants"`)
	res, err := upWith(fsys, dsn, RunOptions{Logger: logs.logger(),
		Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 10, RetryBackoff: 100 * time.Millisecond}})
	if err != nil || res.Grants == nil || !res.Grants.Applied {
		t.Fatalf("Up: %+v, %v\n%s", res.Grants, err, logs.String())
	}
	if !strings.Contains(logs.String(), "statement=\"read-only grants\"") || !strings.Contains(logs.String(), "retrying") {
		t.Errorf("no retry of the grants was logged:\n%s", logs.String())
	}
	asRole(t, dsn, func(ctx context.Context, c *sql.Conn) {
		if err := readable(ctx, c, `SELECT id, email FROM users`); err != nil {
			t.Errorf("the role cannot read what the grants gave it: %v", err)
		}
	})
}

// TestGrantsRunUnderTheDefaultLimitsAndLeaveNoneBehind: with nothing
// configured the script runs under the defaults, and a connection borrowed
// from the application's pool goes back without them, on success and on
// failure.
func TestGrantsRunUnderTheDefaultLimitsAndLeaveNoneBehind(t *testing.T) {
	dsn, fsys := grantsFixture(t)
	pool := openRecordDB(t, dsn)
	pool.SetMaxOpenConns(1)
	show := func() string {
		var v string
		if err := pool.QueryRow("SHOW lock_timeout").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	was := show()
	if _, err := ApplyWith(context.Background(), pool, fsys, "migrations", RunOptions{Limits: Limits{LockTimeout: 7 * time.Second}}); err != nil {
		t.Fatal(err)
	}
	if got := show(); got != was {
		t.Errorf("the pooled connection kept lock_timeout=%s after the grants, was %s", got, was)
	}

	releaseBlocker := grantsBlocker(t, dsn)
	defer releaseBlocker()
	// A new table changes nothing the blocker holds, but the grants converge
	// the schema again, so the script runs and times out.
	more := release(map[string]string{"1_users.up.sql": usersSQL, "2_notes.up.sql": notesSQL, "3_t.up.sql": "CREATE TABLE extra (id INT);"})
	for name, f := range fsys {
		if strings.HasPrefix(name, grantsDir+"/") {
			more[name] = f
		}
	}
	_, err := ApplyWith(context.Background(), pool, more, "migrations",
		RunOptions{Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 1}})
	if !errors.As(err, new(*LockTimeoutError)) {
		t.Fatalf("Apply returned %v, want a grants *LockTimeoutError", err)
	}
	if got := show(); got != was {
		t.Errorf("a failed grants run left lock_timeout=%s on the connection, was %s", got, was)
	}
}

// TestAMigrationThatCommitsPartOfItselfIsNotRetried: `BEGIN; INSERT …; COMMIT;
// ALTER TABLE …` commits the INSERT before the ALTER can time out. Retrying
// would insert twice, and "clean at the previous version" would be a lie. The
// terminator is on the same line as other statements, where a line-anchored
// pattern misses it. The migration fails on its first attempt and the schema
// is dirty at its version, for a human.
func TestAMigrationThatCommitsPartOfItselfIsNotRetried(t *testing.T) {
	dsn := baseSchema(t)
	holdReader(t, dsn)
	file := "CREATE TABLE audit (n INT);\nBEGIN; INSERT INTO audit VALUES (1); COMMIT; ALTER TABLE items ADD COLUMN note TEXT;"
	var logs bytes.Buffer
	_, err := upWith(release(merged(lockBase, map[string]string{"2_audit.up.sql": file})), dsn,
		RunOptions{Logger: logTo(&logs), Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 5, RetryBackoff: 10 * time.Millisecond}})
	var lerr *LockTimeoutError
	if !errors.As(err, &lerr) || lerr.Attempts != 1 || !lerr.PartlyApplied {
		t.Fatalf("Up returned %v, want a *LockTimeoutError after ONE attempt, marked partly applied", err)
	}
	if strings.Contains(logs.String(), "retrying") {
		t.Errorf("a migration that commits part of itself was retried:\n%s", logs.String())
	}
	var n int
	if err := openRecordDB(t, dsn).QueryRow("SELECT count(*) FROM audit").Scan(&n); err != nil || n != 1 {
		t.Errorf("audit has %d rows (err %v), want the one the committed half wrote", n, err)
	}
	if st := stateAt(t, dsn); !st.Dirty || st.Version != 2 {
		t.Errorf("schema is %+v, want dirty at 2: part of the file is committed", st)
	}
}

// committingSetup creates the audit table and a procedure that inserts a row,
// COMMITs it, and then asks for a lock the held reader blocks: the lock
// timeout lands after work that is already committed.
const committingSetup = "CREATE TABLE audit (n INT);\n" +
	"CREATE PROCEDURE commit_then_lock() LANGUAGE plpgsql AS $$\n" +
	"BEGIN INSERT INTO audit VALUES (1); COMMIT; LOCK TABLE items IN ACCESS EXCLUSIVE MODE; END $$;"

// TestAProcedureThatCommitsBeforeTimingOutIsNotRetried: a top-level CALL runs
// outside a transaction block, so the procedure's COMMIT is real. Retrying
// would insert twice and "clean at the previous version" would hide the row.
func TestAProcedureThatCommitsBeforeTimingOutIsNotRetried(t *testing.T) {
	assertCommittedThenTimedOut(t, "CALL commit_then_lock();")
}

// TestADoBlockThatCommitsBeforeTimingOutIsNotRetried is the same for DO.
func TestADoBlockThatCommitsBeforeTimingOutIsNotRetried(t *testing.T) {
	assertCommittedThenTimedOut(t, "DO $$ BEGIN INSERT INTO audit VALUES (1); COMMIT; LOCK TABLE items IN ACCESS EXCLUSIVE MODE; END $$;")
}

func assertCommittedThenTimedOut(t *testing.T, file string) {
	t.Helper()
	dsn := baseSchema(t)
	mustUp(t, release(merged(lockBase, map[string]string{"2_audit.up.sql": committingSetup})), dsn)
	holdReader(t, dsn)
	var logs bytes.Buffer
	_, err := upWith(release(merged(lockBase, map[string]string{"2_audit.up.sql": committingSetup, "3_commit.up.sql": file})), dsn,
		RunOptions{Logger: logTo(&logs), Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 5, RetryBackoff: 10 * time.Millisecond}})
	var lerr *LockTimeoutError
	if !errors.As(err, &lerr) || lerr.Attempts != 1 || !lerr.PartlyApplied {
		t.Fatalf("Up returned %v, want a *LockTimeoutError after ONE attempt, marked partly applied", err)
	}
	if strings.Contains(logs.String(), "retrying") {
		t.Errorf("a migration that commits part of itself was retried:\n%s", logs.String())
	}
	var n int
	if err := openRecordDB(t, dsn).QueryRow("SELECT count(*) FROM audit").Scan(&n); err != nil || n != 1 {
		t.Errorf("audit has %d rows (err %v), want exactly the one committed row", n, err)
	}
	if st := stateAt(t, dsn); !st.Dirty || st.Version != 3 {
		t.Errorf("schema is %+v, want dirty at 3: part of the file is committed", st)
	}
}

// TestDropIndexConcurrentlyThatTimesOutIsNotRetriedAndStaysDirty: DROP INDEX
// CONCURRENTLY commits the index as INVALID and only then waits for readers.
// When that wait times out the index is already unusable, so "nothing was
// applied, clean at the version before" would be false. The run makes one
// attempt and leaves the schema dirty; the index is invalid, as postgres
// left it.
func TestDropIndexConcurrentlyThatTimesOutIsNotRetriedAndStaysDirty(t *testing.T) {
	dsn := baseSchema(t)
	files := map[string]string{"2_items_v.up.sql": concurrentlyIndex}
	mustUp(t, release(merged(lockBase, files)), dsn)
	if exists, valid := indexState(t, openRecordDB(t, dsn)); !exists || !valid {
		t.Fatalf("setup: items_v exists=%v valid=%v, want a valid index", exists, valid)
	}
	holdReader(t, dsn)
	files["3_drop.up.sql"] = "-- forge:backward-compatible\nDROP INDEX CONCURRENTLY items_v;"
	var logs bytes.Buffer
	_, err := upWith(release(merged(lockBase, files)), dsn,
		RunOptions{Logger: logTo(&logs), Limits: Limits{LockTimeout: 200 * time.Millisecond, LockAttempts: 5, RetryBackoff: 10 * time.Millisecond}})
	var lerr *LockTimeoutError
	if !errors.As(err, &lerr) || lerr.Attempts != 1 || !lerr.PartlyApplied {
		t.Fatalf("Up returned %v, want a *LockTimeoutError after ONE attempt, marked partly applied", err)
	}
	if strings.Contains(logs.String(), "retrying") {
		t.Errorf("DROP INDEX CONCURRENTLY was retried:\n%s", logs.String())
	}
	if exists, valid := indexState(t, openRecordDB(t, dsn)); !exists || valid {
		t.Errorf("items_v exists=%v valid=%v, want the invalid index postgres committed before it timed out", exists, valid)
	}
	if st := stateAt(t, dsn); !st.Dirty || st.Version != 3 {
		t.Errorf("schema is %+v, want dirty at 3: the index is already invalid", st)
	}
}

// TestConcurrentlyCleanupDoesNotFollowASessionSearchPath: with
// x-multi-statement a file may SET search_path and then build its index in
// that schema. The cleanup before the file resolves names in the session as it
// was, so it would drop public's invalid items_v for a build that targets
// schema_a. Cleanup only runs for a lone CREATE INDEX CONCURRENTLY, whose
// names mean the same thing before and during the statement.
func TestConcurrentlyCleanupDoesNotFollowASessionSearchPath(t *testing.T) {
	dsn := baseSchema(t)
	db := openRecordDB(t, dsn)
	files := map[string]string{
		"2_schema.up.sql": "CREATE SCHEMA schema_a; CREATE TABLE schema_a.items (id INT, v TEXT);",
	}
	mustUp(t, release(merged(lockBase, files)), dsn)
	for _, q := range []string{
		"CREATE INDEX items_v ON public.items (v)",
		"UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'public.items_v'::regclass",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	files["3_a.up.sql"] = "SET search_path TO schema_a;\nCREATE INDEX CONCURRENTLY items_v ON items (v);"
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	if _, err := upWith(release(merged(lockBase, files)), dsn+sep+"x-multi-statement=true", RunOptions{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for schema, want := range map[string]bool{"public": false, "schema_a": true} {
		var valid bool
		if err := db.QueryRow(`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname = 'items_v' AND n.nspname = $1`, schema).Scan(&valid); err != nil {
			t.Fatalf("%s.items_v: %v (it must exist)", schema, err)
		}
		if valid != want {
			t.Errorf("%s.items_v valid = %v, want %v", schema, valid, want)
		}
	}
}

// TestConcurrentlyCleanupStaysInItsOwnSchema: two schemas each hold an invalid
// items_v. A migration building schema_a.items_v drops schema_a's and leaves
// schema_b's, which it did not build and its file does not name.
func TestConcurrentlyCleanupStaysInItsOwnSchema(t *testing.T) {
	dsn := requirePG(t)
	files := map[string]string{
		"1_schemas.up.sql": "CREATE SCHEMA schema_a; CREATE SCHEMA schema_b;\n" +
			"CREATE TABLE schema_a.items (id INT, v TEXT); CREATE TABLE schema_b.items (id INT, v TEXT);",
	}
	mustUp(t, release(files), dsn)
	db := openRecordDB(t, dsn)
	for _, s := range []string{"schema_a", "schema_b"} {
		for _, q := range []string{
			"CREATE INDEX items_v ON " + s + ".items (v)",
			"UPDATE pg_index SET indisvalid = false WHERE indexrelid = '" + s + ".items_v'::regclass",
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	files["2_a.up.sql"] = "CREATE INDEX CONCURRENTLY items_v ON schema_a.items (v);"
	if _, err := upWith(release(files), dsn, RunOptions{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for schema, want := range map[string]bool{"schema_a": true, "schema_b": false} {
		var valid bool
		if err := db.QueryRow(`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname = 'items_v' AND n.nspname = $1`, schema).Scan(&valid); err != nil {
			t.Fatalf("%s.items_v: %v (it must still exist)", schema, err)
		}
		if valid != want {
			t.Errorf("%s.items_v valid = %v, want %v", schema, valid, want)
		}
	}
}

// TestSessionSettingsOfAPooledConnectionAreRestoredExactly: the application's
// connection had its own session-level statement_timeout before the run
// borrowed it. RESET would hand it back unlimited; the run restores the value
// it found — after success, after a failed migration, and after a grants
// script.
func TestSessionSettingsOfAPooledConnectionAreRestoredExactly(t *testing.T) {
	dsn, grantsFS := grantsFixture(t)
	pool := openRecordDB(t, dsn)
	pool.SetMaxOpenConns(1)
	if _, err := pool.Exec("SET statement_timeout = '2s'"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec("SET lock_timeout = '1500ms'"); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		var lock, statement string
		if err := pool.QueryRow("SELECT current_setting('lock_timeout'), current_setting('statement_timeout')").Scan(&lock, &statement); err != nil {
			t.Fatal(err)
		}
		if lock != "1500ms" || statement != "2s" {
			t.Errorf("%s: the connection has lock_timeout=%s statement_timeout=%s, want 1500ms and 2s", when, lock, statement)
		}
	}
	limits := RunOptions{Limits: Limits{LockTimeout: 7 * time.Second, StatementTimeout: 9 * time.Minute}}

	if _, err := ApplyWith(context.Background(), pool, grantsFS, "migrations", limits); err != nil {
		t.Fatal(err)
	}
	check("after migrations and grants")

	failing := release(merged(lockBase, map[string]string{"2_bad.up.sql": "SELECT * FROM nowhere;"}))
	if _, err := ApplyWith(context.Background(), pool, failing, "migrations", limits); err == nil {
		t.Fatal("a migration selecting from a missing table succeeded")
	}
	check("after a failed migration")
}
