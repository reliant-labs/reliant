package devidp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"
)

// ── One proof of an account at a time, across every replica ─────────────
//
// An address proof (proveAddress) and the credential installs that follow
// one — HandleVerify's pending password and social link, a passkey
// enrolment — must not interleave for one account. Inside one process the
// in-process keyedMutex has always kept proofs apart. Across replicas the
// broker relied on the #2098 time fence alone, which holds only while every
// request is served within proofFenceSlack of its deadline (forge#2121). A
// ProofLocker that every replica shares removes that bound for every request
// the broker is still waiting on: a stale proof holds the lock until its
// calls have returned, so the owner's proof and install cannot start until
// it is done, however slow the issuer, the network or the replica's clock.
//
// What a lock cannot cover is a request the broker has GIVEN UP on (its
// deadline passed) and the issuer serves later. No broker-side lock can,
// because the issuer offers no compare-and-swap to check a fencing token
// against. The fence stays for that case, and under a shared lock the
// window that bounds those calls is long (lockedPreProofWindow), which costs
// nothing in the common case: a competing proof waits for the lock, not for
// the window.

// ProofLocker serialises, per account, an address proof and the credential
// installs that follow one. Set SignInRoutesConfig.ProofLocker only to a
// lock every replica of the broker shares (NewPostgresProofLocker); nil keeps
// the in-process lock, which is enough for a single replica.
type ProofLocker interface {
	// LockAccount waits for userID's lock and returns its release, which
	// may be called more than once. An error means the lock is NOT held,
	// and the caller refuses the work: a proof or install that cannot be
	// serialised is not run (fail closed).
	LockAccount(ctx context.Context, userID string) (release func(), err error)
}

// LockAccount makes the in-process keyedMutex the default ProofLocker.
func (k *keyedMutex) LockAccount(ctx context.Context, userID string) (func(), error) {
	return k.lock(ctx, userID)
}

// UnavailableProofLocker returns a ProofLocker that refuses every
// acquisition with err. A broker configured for a shared lock whose store
// cannot even be set up (a malformed DSN) uses it rather than nil: nil would
// fall back to the in-process lock, and every replica would accept proofs
// without the serialisation the configuration asked for. With it, proofs,
// verification and passkey enrolment answer "unavailable" (fail closed), as
// during a lock-store outage; signing in with an existing credential does
// not take the lock and keeps working.
func UnavailableProofLocker(err error) ProofLocker {
	if err == nil {
		err = errors.New("the shared proof lock is not configured")
	}
	return unavailableProofLocker{err: err}
}

type unavailableProofLocker struct{ err error }

func (u unavailableProofLocker) LockAccount(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("the proof lock is unavailable: %w", u.err)
}

// ErrProofLockBusy is returned by a PostgresProofLocker that waited its
// MaxWait for another proof of the account and gave up.
var ErrProofLockBusy = errors.New("another proof of this account holds its lock")

// The PostgresProofLocker's defaults.
const (
	// DefaultProofLockWait bounds how long a proof waits for another proof
	// of the same account. A proof normally holds the lock for about a
	// second; a longer hold means a stalled issuer, and the waiting caller
	// is better told "try again" than kept on the line.
	DefaultProofLockWait = 30 * time.Second
	// DefaultProofLockHold bounds how long one holder keeps the lock: the
	// longest proof (lockedPreProofWindow, the proof, its cleanup and the
	// fence's wait) fits well inside it. Past it the transaction is rolled
	// back and the lock released, so a leaked release cannot block an
	// account forever.
	DefaultProofLockHold = 10 * time.Minute
)

// PostgresProofLockerOptions tunes a PostgresProofLocker. The zero value is
// production-ready.
type PostgresProofLockerOptions struct {
	// MaxWait bounds the wait for another holder (DefaultProofLockWait).
	MaxWait time.Duration
	// MaxHold bounds one hold (DefaultProofLockHold).
	MaxHold time.Duration
	// Logger receives contention and lost-lock events (slog.Default()).
	Logger *slog.Logger
}

// PostgresProofLocker is a ProofLocker over Postgres transaction-scoped
// advisory locks (pg_advisory_xact_lock), so every replica pointed at the
// same database shares it. The deployment already has the database (the
// app's DATABASE_URL); the lock needs no table and no migration.
//
// Each hold is one transaction on its own connection. The lock dies with
// that transaction, so a replica that crashes, or whose connection drops,
// releases it at once: there is no lease to expire. Transaction-scoped locks
// also work through a transaction-pooling PgBouncer, where a session lock
// would not.
//
// A hold uses a connection for as long as the proof runs, so a pool shared
// with the app's queries should leave room for it; one of its own, opened
// for the broker, is simplest.
type PostgresProofLocker struct {
	db  *sql.DB
	opt PostgresProofLockerOptions
}

// NewPostgresProofLocker returns a ProofLocker on db.
func NewPostgresProofLocker(db *sql.DB, opt PostgresProofLockerOptions) *PostgresProofLocker {
	if opt.MaxWait <= 0 {
		opt.MaxWait = DefaultProofLockWait
	}
	if opt.MaxHold <= 0 {
		opt.MaxHold = DefaultProofLockHold
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	return &PostgresProofLocker{db: db, opt: opt}
}

// proofLockKey is the advisory-lock key of an account's proofs, namespaced so
// another user of advisory locks on the same database collides only by a
// 64-bit coincidence.
func proofLockKey(userID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("forge/devidp/proof/" + userID))
	return int64(h.Sum64())
}

// LockAccount takes userID's lock, waiting at most MaxWait (or until ctx
// ends). A wait is logged when it starts and when it ends, with how long it
// took; a wait that gives up returns ErrProofLockBusy.
//
// The hold itself is detached from ctx: proveAddress finishes a proof
// detached from a client that hung up, and its lock must last as long.
func (l *PostgresProofLocker) LockAccount(ctx context.Context, userID string) (func(), error) {
	start := time.Now()
	wctx, wcancel := context.WithTimeout(ctx, l.opt.MaxWait)
	defer wcancel()
	conn, err := l.db.Conn(wctx)
	if err != nil {
		return nil, fmt.Errorf("the proof lock's database: %w", err)
	}
	hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), l.opt.MaxHold)
	tx, err := conn.BeginTx(hctx, nil)
	if err != nil {
		hcancel()
		_ = conn.Close()
		return nil, fmt.Errorf("the proof lock's database: %w", err)
	}
	fail := func(err error) (func(), error) {
		_ = tx.Rollback()
		hcancel()
		_ = conn.Close()
		return nil, err
	}
	key := proofLockKey(userID)
	var got bool
	if err := tx.QueryRowContext(wctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&got); err != nil {
		return fail(fmt.Errorf("take the account's proof lock: %w", err))
	}
	if !got {
		l.opt.Logger.InfoContext(ctx, "waiting for another proof of this account to finish", "user_id", userID)
		// The server stops waiting too, not only this client: a wait the
		// client abandons must not take the lock a moment later.
		left := time.Until(start.Add(l.opt.MaxWait))
		if d, ok := wctx.Deadline(); ok {
			left = time.Until(d)
		}
		if left < time.Millisecond {
			left = time.Millisecond
		}
		if _, err := tx.ExecContext(wctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", left.Milliseconds())); err != nil {
			return fail(fmt.Errorf("bound the wait for the account's proof lock: %w", err))
		}
		if _, err := tx.ExecContext(wctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
			waited := time.Since(start)
			l.opt.Logger.WarnContext(ctx, "gave up waiting for another proof of this account", "user_id", userID, "wait", waited, "error", err)
			return fail(fmt.Errorf("%w (waited %s): %v", ErrProofLockBusy, waited.Round(time.Millisecond), err))
		}
		l.opt.Logger.InfoContext(ctx, "took the account's proof lock after another proof finished", "user_id", userID, "wait", time.Since(start))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Ending the transaction releases the lock. An error here means
			// it was already gone — the connection dropped or MaxHold passed —
			// and another replica may have run a proof meanwhile: the fence
			// is what covered that.
			err := tx.Rollback()
			hcancel()
			_ = conn.Close()
			if err != nil {
				l.opt.Logger.WarnContext(context.WithoutCancel(ctx), "the account's proof lock was lost before its release", "user_id", userID, "held", time.Since(start), "error", err)
			}
		})
	}, nil
}
