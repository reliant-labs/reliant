package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Context-carried transactions.
//
// RunTx opens a transaction and hands fn a context.Context that CARRIES it.
// Every query issued with that ctx through the same database — a generated
// delegate (db.GetJobByID(ctx, s.deps.DB, id)), a pkg/crud.Repo method, a
// store method, raw SQL through Exec/Query/QueryRow or db.Bun() — runs inside
// the transaction, although the handle it was given is still the plain
// *Client. Nothing threads a tx handle through signatures, so a service is
// written once and composes into any caller's transaction.
//
// # Isolation is the caller's choice, and there is no default
//
// Every writable transaction names its isolation level and its retry budget
// at the call site: [Serializable] or [ReadCommitted] (or [RepeatableRead]),
// refined with WithMaxAttempts / WithReadOnly into a [TxOptions]. An
// unset level is rejected with ErrInvalidTxOptions naming the call site. The
// two levels make opposite correctness arguments, so neither is a safe thing
// to inherit:
//
//   - [Serializable] — SERIALIZABLE, retried on serialization failure and
//     deadlock (SQLSTATE 40001 / 40P01) up to DefaultTxMaxAttempts. The
//     database proves the transaction equivalent to some serial order, so
//     read-check-write is correct WITHOUT locks. A lock taken inside it is
//     the trap: the writer that waited on it wakes into a stale snapshot and
//     fails 40001, surviving only by retrying (forgeconv-tx-lock-under-
//     serializable flags it).
//   - [ReadCommitted] — READ COMMITTED, one attempt. Each statement sees what
//     was committed before it began, so read-check-write is correct only
//     when a lock (SELECT … FOR UPDATE, pg_advisory_xact_lock) or a single
//     statement makes it so. It never raises 40001.
//
// Serializable is what makes the ordinary read-check-write shape correct
// with no explicit locking:
//
//	err := s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
//	    inv, err := db.GetInvoiceByID(ctx, s.deps.DB, id)
//	    if err != nil {
//	        return err
//	    }
//	    if inv.BalanceCents < amount {
//	        return svcerr.FailedPrecondition("payment exceeds the balance")
//	    }
//	    inv.PaidCents += amount
//	    return db.UpdateInvoiceMasked(ctx, s.deps.DB, inv, []string{"paid_cents"})
//	})
//
// Two concurrent payments both read the same balance; postgres detects that
// the two transactions cannot be ordered serially, aborts one with 40001, and
// RunTx re-runs it from the top — where it reads the COMMITTED balance and
// takes the branch that is now correct. No SELECT … FOR UPDATE, no per-entity
// lock helper, and no way to forget one on a new code path.
//
// # The contract fn must honor
//
// fn MAY RUN MORE THAN ONCE. A retry rolls back everything the failed attempt
// did in the database and calls fn again from the start, so fn must have no
// effect outside the database: no email, no publish, no HTTP call, no
// mutation of state that outlives the call. Register such effects with
// [AfterCommit]; they run once, after the outermost commit, and a rolled-back
// or retried attempt discards them.
//
// The transaction is one database connection. Do not use the ctx handed to fn
// from several goroutines at once.

const (
	txBaseRetryDelay = 50 * time.Millisecond
	txMaxRetryDelay  = 1 * time.Second
)

// ErrNoTransaction is returned by AfterCommit when ctx carries no active
// transaction to defer the callback to.
var ErrNoTransaction = errors.New("orm: no active transaction in context")

// txState is one transaction attempt: the open transaction, the identity of
// the pool it belongs to, and the callbacks to run if — and only if — THIS
// attempt commits. A retried attempt gets a fresh txState, so a callback
// registered by an attempt that was rolled back dies with it.
type txState struct {
	// db is the pool the transaction was opened on. It is the identity the
	// executor resolution checks: a transaction carried in ctx is used only
	// for queries against the SAME pool, so a transaction on database A can
	// never be handed a statement meant for database B.
	db *sql.DB
	tx bun.Tx
	// conn is tx as the connection bun runs this transaction's statements
	// on, their values bound (bind_conn.go).
	conn *bindConn

	// outer is the context RunTx/BeginTx was called with — the caller's
	// context, without this transaction in it. After-commit callbacks
	// receive it, so a callback that queries the database runs against the
	// pool rather than a transaction that has already ended.
	outer context.Context

	mu          sync.Mutex
	afterCommit []func(context.Context)

	// done flips once the transaction commits or rolls back. A ctx that
	// still carries a finished transaction — captured by a closure, or
	// leaked past RunTx — resolves to the plain pool again instead of
	// failing every query with sql.ErrTxDone.
	done atomic.Bool
}

func (s *txState) active() bool { return s != nil && !s.done.Load() }

// addAfterCommit queues fn to run if this attempt commits.
func (s *txState) addAfterCommit(fn func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterCommit = append(s.afterCommit, fn)
}

// finish marks the transaction ended and hands back the callbacks that
// should run: all of them on commit, none on rollback.
func (s *txState) finish(committed bool) []func(context.Context) {
	s.done.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	callbacks := s.afterCommit
	s.afterCommit = nil
	if !committed {
		return nil
	}
	return callbacks
}

// txKey is the context key for the transaction carried for ONE pool. Keying
// by pool rather than using a single slot is what lets transactions on two
// databases nest without the inner one shadowing the outer: each database
// finds its own.
type txKey struct{ db *sql.DB }

// innermostTxKey names the most recently opened transaction in ctx, whatever
// its pool — the one AfterCommit attaches to.
type innermostTxKey struct{}

// withTx returns ctx carrying st.
func withTx(ctx context.Context, st *txState) context.Context {
	ctx = context.WithValue(ctx, txKey{db: st.db}, st)
	return context.WithValue(ctx, innermostTxKey{}, st)
}

// ambientTx returns the still-active transaction ctx carries for pool db, or
// nil when there is none.
func ambientTx(ctx context.Context, db *sql.DB) *txState {
	if ctx == nil || db == nil {
		return nil
	}
	st, _ := ctx.Value(txKey{db: db}).(*txState)
	if !st.active() {
		return nil
	}
	return st
}

// AfterCommit defers fn until the transaction carried by ctx commits.
//
// fn runs exactly once, after the OUTERMOST commit: a RunTx that joined an
// enclosing transaction does not commit anything itself, so callbacks it
// registers wait for the owner. If the attempt rolls back — fn returned an
// error, or postgres aborted it and RunTx is retrying — the callbacks
// registered by that attempt are discarded; the retry registers its own.
//
// This is the place for every effect fn must not perform itself because fn
// can run more than once: sending an email, publishing an event, enqueueing
// a job, calling another service.
//
// fn receives the context RunTx was called with, which does not carry the
// finished transaction. It runs synchronously, in registration order, before
// RunTx returns. It cannot fail the transaction — that has already committed
// — so it returns nothing; handle or log its own errors.
//
// With nested transactions on DIFFERENT databases, fn attaches to the
// innermost one and runs when that one commits.
//
// Returns ErrNoTransaction when ctx carries no active transaction.
func AfterCommit(ctx context.Context, fn func(context.Context)) error {
	st := innermostTx(ctx)
	if !st.active() {
		return ErrNoTransaction
	}
	st.addAfterCommit(fn)
	return nil
}

// InTx reports whether ctx carries an active transaction — one opened by
// RunTx (or joined through Tx.RunTx) whose attempt has not yet committed or
// rolled back. A ctx captured inside RunTx and used after it returned
// reports false, exactly as its queries then run on the pool.
//
// It answers for the innermost transaction, whatever its database — the
// same one AfterCommit attaches to. Use it where code must behave
// differently inside a caller's transaction: refuse to take a lock that
// needs its own transaction (BeginDetached would run on a SECOND
// connection, and a row the caller's transaction holds would deadlock it
// against itself), or decide whether a side effect has to be deferred.
func InTx(ctx context.Context) bool {
	return innermostTx(ctx).active()
}

// ─── Detached: one statement outside the caller's transaction ─────────────

// Detached returns a ctx whose ORM and database/sql calls run on the POOL,
// outside any transaction ctx carries — on every database. It keeps ctx's
// deadline, cancellation and values; it only hides the transactions.
//
//	// The ledger row commits on its own: it records the attempt even when
//	// the caller's transaction rolls back.
//	err := db.CreateLedgerEntry(orm.Detached(ctx), s.deps.DB, entry)
//
// Every resolution the carried transaction drives honours it: generated
// delegates, pkg/crud, db.Bun() builders and its ExecContext/QueryContext,
// the raw Exec/Query/QueryRow passthroughs, SQLConn and DBTX, InTx (false),
// Client.InTx (false) and AfterCommit (ErrNoTransaction). Each statement is
// its own autocommit transaction on a pooled connection. RunTx with the
// detached ctx opens a NEW transaction, which the ctx it hands fn carries as
// usual, so a detached multi-statement write is
// `client.RunTx(orm.Detached(ctx), fn)`.
//
// It is the statement-sized sibling of BeginDetached: the same "never joins"
// rule, without a transaction for the caller to end. An explicit handle is
// not affected — a statement run on a *Tx (tx.Bun(), tx.Exec) is that
// transaction's whatever ctx it is given.
//
// # When to use it
//
// For a write that must survive the caller's rollback: an audit record, a
// write or idempotency ledger, an attempt counter, a failure log. Those are
// facts about the attempt, and the attempt happened whether or not its work
// commits.
//
// # When not to
//
//   - Anything that must roll back with the caller. A detached write is
//     committed before the caller's transaction ends, and stays committed if
//     it rolls back or RunTx retries it — so under RunTx it runs once PER
//     ATTEMPT. Make it idempotent, or record it with AfterCommit if it should
//     exist only when the work committed.
//   - Reading the caller's uncommitted writes. A detached read sees only
//     committed rows.
//   - Touching a row the caller's transaction has written or locked. The
//     detached statement runs on a second connection and waits for the
//     caller's lock, while the caller waits for it to return: a deadlock
//     postgres cannot see. The same holds when the pool has no connection
//     to spare — a pool of one deadlocks on the first detached call inside
//     a transaction.
func Detached(ctx context.Context) context.Context {
	return detachedCtx{ctx}
}

// detachedCtx hides every transaction its parent carries. Transactions
// opened under it are added above it with withTx and found as usual.
type detachedCtx struct{ context.Context }

func (d detachedCtx) Value(key any) any {
	switch key.(type) {
	case txKey, innermostTxKey:
		return nil
	}
	return d.Context.Value(key)
}

// innermostTx returns the most recently opened transaction ctx carries,
// active or not, or nil.
func innermostTx(ctx context.Context) *txState {
	if ctx == nil {
		return nil
	}
	st, _ := ctx.Value(innermostTxKey{}).(*txState)
	return st
}

// ─── BeginDetached: a transaction that outlives one call ────────────────────

// BeginDetached opens a transaction that the CALLER owns and ends: it stays
// open across function returns until Commit or Rollback, which is what a
// closure-scoped RunTx cannot express. The canonical use is a
// transaction-scoped lock held for the length of a unit of work whose shape
// is not a single closure:
//
//	func LockRun(ctx context.Context, db orm.Context, key int64) (release func(), err error) {
//	    if orm.InTx(ctx) {
//	        return nil, errors.New("LockRun needs its own transaction; call it outside RunTx")
//	    }
//	    tx, err := db.BeginDetached(ctx, orm.ReadCommitted)
//	    if err != nil {
//	        return nil, err
//	    }
//	    if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
//	        _ = tx.Rollback()
//	        return nil, err
//	    }
//	    return func() { _ = tx.Rollback() }, nil // releasing the lock = ending the tx
//	}
//
// It is "detached" because it never joins the transaction ctx carries: it
// always begins a NEW one on its own pooled connection, at opts' isolation
// (READ ONLY, and DEFERRABLE under SERIALIZABLE, when opts.ReadOnly).
// Inside a caller's RunTx that is a second, independent transaction — its
// commit survives the caller's rollback, and it blocks on any row the
// caller's transaction has locked. Check InTx first when that is not what
// you mean. For a statement (or a closure) that only needs to run outside
// the caller's transaction, with no transaction of its own to hold open,
// use Detached(ctx) instead.
//
// What the caller takes on, compared with RunTx:
//
//   - Ending it. Every path must Commit or Rollback; a leaked transaction
//     pins a pooled connection and its locks until ctx ends. Rollback after
//     Commit is a harmless sql.ErrTxDone, so `defer tx.Rollback()` is safe.
//   - Its lifetime is bounded by ctx. database/sql rolls the transaction
//     back when ctx is cancelled, which is usually the right release for a
//     request-scoped lock. Pass context.WithoutCancel(ctx) to outlive it.
//   - No retry. opts.MaxAttempts must be 1 — orm.ReadCommitted, or
//     orm.Serializable.WithMaxAttempts(1) — and anything else is rejected
//     with ErrInvalidTxOptions: a serialization failure is returned to the
//     caller, who decides whether to run the unit again, and a call site
//     that asked for retries would be promising something nobody performs.
//
// Statements run through the returned *Tx (its Exec/Query/QueryRow and
// Bun()); tx.Join(ctx, fn) hands fn a ctx that carries it, so generated
// stores and pkg/crud join it like any RunTx. AfterCommit callbacks
// registered in that ctx run on Commit.
//
// It is also the low-level primitive: the one way to begin a transaction
// the caller ends. (Client.BeginTx, which took *sql.TxOptions and so could
// silently inherit the database's default level, is gone.)
func (c *Client) BeginDetached(ctx context.Context, policy TxPolicy) (*Tx, error) {
	opts, err := resolveTxPolicy(policy, "BeginDetached", true)
	if err != nil {
		return nil, err
	}
	tx, err := c.beginTx(ctx, opts)
	if err != nil {
		return nil, NewTransactionError("begin", err)
	}
	return tx, nil
}

// BeginDetached on a *Tx opens a NEW transaction on the same database, not
// a nested one: see Client.BeginDetached. It never joins t.
func (t *Tx) BeginDetached(ctx context.Context, policy TxPolicy) (*Tx, error) {
	if t.client == nil {
		return nil, NewTransactionError("begin", errors.New("orm: Tx has no originating Client"))
	}
	return t.client.BeginDetached(ctx, policy)
}

// ─── RunTx on *Client ─────────────────────────────────────────────────────

// RunTx runs fn in a transaction at opts' isolation, carried by the ctx fn
// receives, running it up to opts.MaxAttempts times: a 40001 / 40P01 failure
// rolls the attempt back and re-runs fn from the top. With orm.Serializable
// fn MAY RUN MORE THAN ONCE; see the package documentation in tx.go and
// AfterCommit. opts has no default — see TxOptions — and an unset level is
// rejected with ErrInvalidTxOptions before anything begins.
//
// If ctx already carries an active transaction on this database, RunTx JOINS
// it: fn runs inside the enclosing transaction, and committing, rolling back
// and retrying stay with whoever opened it. That is what lets a service wrap
// its own work in RunTx and still compose into a caller's larger one. opts
// are still validated — a call site that names no level is wrong whether or
// not it happens to join — and otherwise IGNORED: the enclosing transaction
// exists and its mode cannot change mid-flight. That is safe in the direction
// that matters — a read-only call joining a read-write transaction just reads
// — and the reverse, a write joining a read-only transaction, is rejected by
// postgres at the write, which is exactly where the mistake is.
//
// fn's error is returned verbatim, so svcerr sentinels and domain errors
// survive. A transaction still conflicting after its last attempt returns a
// svcerr.Aborted error (the driver error is kept as its cause).
func (c *Client) RunTx(ctx context.Context, policy TxPolicy, fn func(ctx context.Context) error) error {
	opts, err := resolveTxPolicy(policy, "RunTx", false)
	if err != nil {
		return err
	}
	if ambientTx(ctx, c.bun.DB) != nil {
		return fn(ctx)
	}
	return runAttempts(ctx, opts, func() error { return c.runTxAttempt(ctx, opts, fn) })
}

// RunTxReadOnly is RunTx for a transaction that only reads: SERIALIZABLE
// READ ONLY DEFERRABLE, orm.Serializable.WithReadOnly(). See
// TxOptions.ReadOnly for why that matters.
//
// It needs no level argument because a read-only transaction has one right
// answer: postgres rejects every write in it, so the lost update the level
// choice guards against cannot happen, and SERIALIZABLE READ ONLY DEFERRABLE
// is the strongest snapshot at no conflict cost.
func (c *Client) RunTxReadOnly(ctx context.Context, fn func(ctx context.Context) error) error {
	return c.RunTx(ctx, Serializable.WithReadOnly(), fn)
}

// runAttempts runs attempt up to opts.MaxAttempts times, sleeping between
// tries, for as long as it fails retryably (40001 / 40P01).
func runAttempts(ctx context.Context, opts TxOptions, attempt func() error) error {
	for n := 1; ; n++ {
		err := attempt()
		if err == nil {
			return nil
		}
		if !isRetryableTxError(err) {
			return err
		}
		if n >= opts.MaxAttempts {
			return svcerr.WithCause(
				svcerr.Aborted("the operation conflicted with concurrent changes; retry it"),
				fmt.Errorf("orm: transaction still conflicting after %d attempts: %w", n, err))
		}
		if err := sleepCtx(ctx, txRetryDelay(n)); err != nil {
			return err
		}
	}
}

// runTxAttempt runs ONE attempt: begin, fn, commit — rolling back if fn fails
// or panics.
func (c *Client) runTxAttempt(ctx context.Context, opts TxOptions, fn func(ctx context.Context) error) (err error) {
	tx, err := c.beginTx(ctx, opts)
	if err != nil {
		return NewTransactionError("begin", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(withTx(ctx, tx.state)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return NewTransactionError("commit", err)
	}
	committed = true
	return nil
}

// beginTx opens a transaction at level, READ ONLY when asked — and
// DEFERRABLE when it is both read-only and SERIALIZABLE, which is the
// combination where DEFERRABLE buys "no predicate locks, cannot be aborted"
// (see TxOptions.ReadOnly). database/sql has no DEFERRABLE flag, so it is set
// as the transaction's first statement.
func (c *Client) beginTx(ctx context.Context, opts TxOptions) (*Tx, error) {
	tx, err := c.begin(ctx, &sql.TxOptions{Isolation: opts.Isolation.sqlLevel(), ReadOnly: opts.ReadOnly})
	if err != nil {
		return nil, err
	}
	if opts.ReadOnly && opts.Isolation == Serializable {
		if _, err := tx.tx.ExecContext(ctx, "SET TRANSACTION READ ONLY DEFERRABLE"); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("set transaction read only deferrable: %w", err)
		}
	}
	return tx, nil
}

// ─── RunTx on *Tx: always a join ───────────────────────────────────────────

// Join runs fn inside THIS transaction, with a ctx that carries it, so code
// written against the plain client (generated delegates, stores, pkg/crud)
// participates. It never begins, commits, retries or rolls back: the owner of
// the *Tx does, and the transaction keeps the level it was begun at. This is
// how code holding an explicit handle — from BeginDetached, or
// RunTransaction — calls a service written with RunTx.
//
// It takes no policy because it decides none: a level spelled at a join
// would claim an isolation the transaction may not have.
func (t *Tx) Join(ctx context.Context, fn func(ctx context.Context) error) error {
	if t.state == nil {
		return fn(ctx)
	}
	return fn(withTx(ctx, t.state))
}

// RunTx on a *Tx is a join: see Join. It exists so *Tx satisfies Context —
// code written against Context (a service's RunTx(ctx, orm.Serializable, …))
// composes into a transaction it was handed. The policy is validated, as on
// Client.RunTx, and otherwise ignored. Holding a *Tx, call Join.
func (t *Tx) RunTx(ctx context.Context, policy TxPolicy, fn func(ctx context.Context) error) error {
	if _, err := resolveTxPolicy(policy, "RunTx", false); err != nil {
		return err
	}
	return t.Join(ctx, fn)
}

// RunTxReadOnly on a *Tx is a join; see Join. The read-only request is
// ignored because the transaction's mode is already fixed.
func (t *Tx) RunTxReadOnly(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.Join(ctx, fn)
}

// ─── retry policy ──────────────────────────────────────────────────────────

// isRetryableTxError reports whether err — anywhere in its %w chain — is a
// postgres failure that re-running the whole transaction can cure:
//
//   - 40001 serialization_failure: SERIALIZABLE / REPEATABLE READ found the
//     transaction could not be ordered against a concurrent one.
//   - 40P01 deadlock_detected: postgres broke a lock cycle by aborting this
//     transaction.
//
// Deliberately NOT retried: 23505 unique_violation, which is a real answer
// (pkg/crud maps it to AlreadyExists) — and postgres already reports a
// read-then-insert race under SERIALIZABLE as 40001 rather than 23505.
func isRetryableTxError(err error) bool {
	state, _, ok := pgDiagnostic(err)
	return ok && (state == "40001" || state == "40P01")
}

// txRetryDelay is exponential backoff from txBaseRetryDelay, capped at
// txMaxRetryDelay, with ±25% jitter so writers that collided once do not
// collide again in lockstep. attempt is the 1-based attempt that just failed.
func txRetryDelay(attempt int) time.Duration {
	delay := txBaseRetryDelay << min(attempt-1, 10)
	if delay > txMaxRetryDelay {
		delay = txMaxRetryDelay
	}
	return delay - delay/4 + rand.N(delay/2)
}

// sleepCtx waits d, or returns ctx's error if ctx ends first — a request
// that was cancelled must not keep retrying.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ─── executor resolution ───────────────────────────────────────────────────

// ambientResolver is the bun.ConnResolver every Client installs on its
// *bun.DB. Bun consults it for each query built from the *bun.DB (NewSelect,
// NewInsert, NewRaw, …) whose connection was not fixed explicitly, passing
// the ctx the query runs with. It answers with the transaction ctx carries
// for THIS pool — so every generated delegate and pkg/crud.Repo method,
// handed the plain *Client, joins the caller's RunTx without being told.
//
// It returns the *sql.Tx rather than the bun.Tx: bun runs its query hooks
// (tracing) around the statement already, and bun.Tx's own methods would run
// them a second time — the same unwrapping bun applies in baseQuery.setConn.
//
// A query built off an explicit *Tx handle (tx.Bun()) never reaches here:
// bun pins its connection, so an explicit handle always wins.
//
// What it resolves is a bindConn — the transaction's, or the pool's — which
// sends the statement with its values bound (bind_conn.go).
type ambientResolver struct {
	db   *sql.DB
	pool *bindConn
}

func (r *ambientResolver) ResolveConn(ctx context.Context, _ bun.Query) bun.IConn {
	if st := ambientTx(ctx, r.db); st != nil && st.conn != nil {
		return st.conn
	}
	return r.pool
}

func (*ambientResolver) Close() error { return nil }

// ambientDB is the bun.IDB Client.Bun() returns: the *bun.DB, with its three
// direct-execution methods made transaction-aware.
//
// Bun's query builders consult ambientResolver on their own, but
// (*bun.DB).ExecContext / QueryContext / QueryRowContext go straight to the
// pool. Left alone, `db.Bun().ExecContext(ctx, …)` inside RunTx would escape
// the transaction — losing atomicity, and blocking forever if it touched a
// row the transaction had locked (two connections, one goroutine: a deadlock
// postgres cannot see). These overrides route them through the bun.Tx so
// hooks and query formatting are unchanged.
type ambientDB struct{ *bun.DB }

func (d ambientDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if st := ambientTx(ctx, d.DB.DB); st != nil {
		return st.tx.ExecContext(ctx, query, args...)
	}
	return d.DB.ExecContext(ctx, query, args...)
}

func (d ambientDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if st := ambientTx(ctx, d.DB.DB); st != nil {
		return st.tx.QueryContext(ctx, query, args...)
	}
	return d.DB.QueryContext(ctx, query, args...)
}

func (d ambientDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if st := ambientTx(ctx, d.DB.DB); st != nil {
		return st.tx.QueryRowContext(ctx, query, args...)
	}
	return d.DB.QueryRowContext(ctx, query, args...)
}

var _ bun.IDB = ambientDB{}

// conn returns the executor for raw SQL on c: the transaction ctx carries for
// c's pool, or the pool itself.
func (c *Client) conn(ctx context.Context) bun.IConn {
	if st := ambientTx(ctx, c.bun.DB); st != nil {
		return st.tx.Tx
	}
	return c.bun.DB
}
