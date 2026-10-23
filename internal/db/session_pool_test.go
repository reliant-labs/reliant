// Copyright (c) 2025 Reliant Labs
package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// recordingDriver is a database/sql driver whose connections record every
// statement they run, keyed by the DSN they were opened with. It answers any
// query with a single `true`, which is all TryAdvisoryLock reads.
type recordingDriver struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (d *recordingDriver) Open(dsn string) (driver.Conn, error) {
	return &recordingConn{driver: d, dsn: dsn}, nil
}

func (d *recordingDriver) record(dsn, query string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[dsn] = append(d.seen[dsn], query)
}

func (d *recordingDriver) statements(dsn string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen[dsn]...)
}

type recordingConn struct {
	driver *recordingDriver
	dsn    string
}

func (c *recordingConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *recordingConn) Close() error                        { return nil }
func (c *recordingConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c *recordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.record(c.dsn, query)
	return &oneTrueRow{}, nil
}

func (c *recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.record(c.dsn, query)
	return driver.RowsAffected(0), nil
}

type oneTrueRow struct{ done bool }

func (r *oneTrueRow) Columns() []string { return []string{"locked"} }
func (r *oneTrueRow) Close() error      { return nil }
func (r *oneTrueRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = true
	return nil
}

var (
	recorder     = &recordingDriver{seen: map[string][]string{}}
	registerOnce sync.Once
	recorderRuns atomic.Uint64
)

// recorderKey names one test RUN's pools in the process-wide recorder. Keyed
// by t.Name() alone, a second run of the same test (-count=N) would read the
// statements its earlier runs recorded.
func recorderKey(t *testing.T, pool string) string {
	return fmt.Sprintf("%s#%d/%s", t.Name(), recorderRuns.Add(1), pool)
}

func openRecording(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	registerOnce.Do(func() { sql.Register("reliant-session-recorder", recorder) })
	pool, err := sql.Open("reliant-session-recorder", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	return pool
}

func newRecordingRepo(t *testing.T, pool *sql.DB) *db.Repo {
	t.Helper()
	repo, err := db.NewRepo(pool)
	if err != nil {
		t.Fatalf("NewRepo: %v", err)
	}
	return repo
}

func countContaining(statements []string, needle string) int {
	n := 0
	for _, s := range statements {
		if strings.Contains(s, needle) {
			n++
		}
	}
	return n
}

// TryAdvisoryLock takes a SESSION-level lock, so behind a transaction-pooling
// PgBouncer it must not run on the main pool: the lock would land on whichever
// server backend served that one statement, the unlock on another, and a
// second replica handed the first backend would re-enter the lock and think it
// won. With a session pool configured (DatabaseConfig.DirectURL), the lock AND
// its release must both go there, and nothing about it may touch the main pool.
func TestTryAdvisoryLockTakesItsSessionFromTheSessionPool(t *testing.T) {
	t.Parallel()

	pooledKey, sessionKey := recorderKey(t, "pooled"), recorderKey(t, "session")
	pooled := openRecording(t, pooledKey)
	session := openRecording(t, sessionKey)

	repo := newRecordingRepo(t, pooled).WithSessionPool(session)
	t.Cleanup(func() { _ = repo.Close() })

	release, ok, err := repo.TryAdvisoryLock(context.Background(), 42)
	if err != nil {
		t.Fatalf("TryAdvisoryLock: %v", err)
	}
	if !ok {
		t.Fatal("TryAdvisoryLock reported the lock as not taken; the fake answers true")
	}
	release()

	sessionStatements := recorder.statements(sessionKey)
	pooledStatements := recorder.statements(pooledKey)
	if got := countContaining(sessionStatements, "pg_try_advisory_lock"); got != 1 {
		t.Errorf("pg_try_advisory_lock on the session pool: got %d, want 1 (session pool saw %q, main pool saw %q)",
			got, sessionStatements, pooledStatements)
	}
	if got := countContaining(sessionStatements, "pg_advisory_unlock"); got != 1 {
		t.Errorf("pg_advisory_unlock on the session pool: got %d, want 1 — the release must reach the session that holds the lock (session pool saw %q)",
			got, sessionStatements)
	}
	if len(pooledStatements) != 0 {
		t.Errorf("the main (possibly transaction-pooled) pool ran %q; the advisory lock must never touch it", pooledStatements)
	}
}

// Without a session pool the main pool is the session pool: every deployment
// that does not run a transaction pooler keeps behaving exactly as before.
func TestTryAdvisoryLockUsesTheMainPoolWhenNoSessionPoolIsConfigured(t *testing.T) {
	t.Parallel()

	pooledKey := recorderKey(t, "pooled")
	pooled := openRecording(t, pooledKey)

	repo := newRecordingRepo(t, pooled)
	t.Cleanup(func() { _ = repo.Close() })

	release, ok, err := repo.TryAdvisoryLock(context.Background(), 42)
	if err != nil || !ok {
		t.Fatalf("TryAdvisoryLock: ok=%v err=%v", ok, err)
	}
	release()

	statements := recorder.statements(pooledKey)
	if countContaining(statements, "pg_try_advisory_lock") != 1 || countContaining(statements, "pg_advisory_unlock") != 1 {
		t.Errorf("main pool statements = %q, want one lock and one unlock", statements)
	}
}
