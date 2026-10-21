// Copyright (c) 2025 Reliant Labs
package storetest

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db"
)

// TxProbe is a second pool on an Env's database that records what its
// transactions put on the wire: the isolation level of every BEGIN, and the
// SQLSTATE of every statement that failed. Both data layers run over it, as
// they do over Env's pool.
//
// It exists for isolation tests (internal/db/SCHEMA.md rule 7). A transaction
// whose correctness comes from row or advisory locks must be READ COMMITTED:
// under SERIALIZABLE the loser of a lock race fails with 40001 instead of
// reading the winner's write, and a retry ladder then hides that from the
// caller. The probe sees both — the level it began at and the 40001 it was
// retried past — so a test of the race fails at the wrong level even when the
// retried call eventually succeeds.
type TxProbe struct {
	ORM  *orm.Client
	Repo *db.Repo

	mu       sync.Mutex
	levels   []string
	sqlState []string
}

// Probe opens a TxProbe on e's database. It is closed when the test ends.
func (e *Env) Probe(tb testing.TB) *TxProbe {
	tb.Helper()
	cfg, err := pgx.ParseConfig(e.DSN)
	if err != nil {
		tb.Fatalf("storetest: probe config: %v", err)
	}
	p := &TxProbe{}
	cfg.Tracer = probeTracer{p}
	// forge:lint-disable-next-line forge-tracing-raw-sql-open: test-only probe pool; its tracer IS the point, and dbtrace.Open would replace it
	pool := stdlib.OpenDB(*cfg)
	tb.Cleanup(func() { _ = pool.Close() })
	repo, err := db.NewRepoWithDriver(pool, db.DriverPostgres)
	if err != nil {
		tb.Fatalf("storetest: probe repo: %v", err)
	}
	enableSealing(tb, repo)
	p.ORM, p.Repo = repo.ORM(), repo
	p.Reset()
	return p
}

// Isolations is the isolation level of each transaction begun since the last
// Reset, in order, as postgres spells it ("read committed", "serializable");
// "default" for a BEGIN that names none, which is the server's default.
func (p *TxProbe) Isolations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.levels...)
}

// SQLStates is the SQLSTATE of each statement that failed since the last
// Reset, in order.
func (p *TxProbe) SQLStates() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sqlState...)
}

// Reset forgets everything recorded so far.
func (p *TxProbe) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.levels, p.sqlState = nil, nil
}

type probeTracer struct{ p *TxProbe }

func (t probeTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	q := strings.ToLower(strings.TrimSpace(data.SQL))
	if q != "begin" && !strings.HasPrefix(q, "begin ") {
		return ctx
	}
	level := "default"
	if _, rest, ok := strings.Cut(q, "isolation level "); ok {
		level = rest
		for _, mode := range []string{" read write", " read only", " deferrable", " not deferrable"} {
			level, _, _ = strings.Cut(level, mode)
		}
	}
	t.p.mu.Lock()
	t.p.levels = append(t.p.levels, level)
	t.p.mu.Unlock()
	return ctx
}

func (t probeTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	var pgErr *pgconn.PgError
	if !errors.As(data.Err, &pgErr) {
		return
	}
	t.p.mu.Lock()
	t.p.sqlState = append(t.p.sqlState, pgErr.Code)
	t.p.mu.Unlock()
}

// LockHolder is a transaction on its own connection that holds row locks for
// a race test: it locks and writes, waits until the transaction under test is
// queued behind it, then commits — so the waiter's next read is the first
// moment it can see the write.
type LockHolder struct {
	tb  testing.TB
	sql *sql.DB
	tx  *sql.Tx
	pid int
}

// HoldLocks begins a READ COMMITTED transaction on e's pool and runs stmts in
// it (typically a SELECT … FOR UPDATE and the UPDATE that wins the race). It
// is rolled back when the test ends unless Commit ran.
func (e *Env) HoldLocks(tb testing.TB, stmts ...string) *LockHolder {
	tb.Helper()
	ctx := context.Background()
	tx, err := e.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		tb.Fatalf("storetest: lock holder begin: %v", err)
	}
	tb.Cleanup(func() { _ = tx.Rollback() })
	h := &LockHolder{tb: tb, sql: e.SQL, tx: tx}
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&h.pid); err != nil {
		tb.Fatalf("storetest: lock holder pid: %v", err)
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			tb.Fatalf("storetest: lock holder %q: %v", s, err)
		}
	}
	return h
}

// WaitForWaiter blocks until some other backend is waiting on a lock h holds,
// failing the test after 10s.
func (h *LockHolder) WaitForWaiter() {
	h.tb.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := h.sql.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, h.pid).Scan(&n); err != nil {
			h.tb.Fatalf("storetest: poll lock waiters: %v", err)
		}
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			h.tb.Fatal("storetest: nothing queued behind the lock holder within 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Commit commits the holder's writes and releases its locks.
func (h *LockHolder) Commit() {
	h.tb.Helper()
	if err := h.tx.Commit(); err != nil {
		h.tb.Fatalf("storetest: lock holder commit: %v", err)
	}
}
