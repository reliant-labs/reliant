package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// SubjectLocker is the optional capability of a [Store] to serialize work on
// one subject's account across every process that shares the store: a
// read-check-write against the identity provider ("is this the account's
// last credential? then do not remove it") that two replicas must not
// interleave. [MemoryStore] and [PostgresStore] implement it; a Store that
// does not makes [Manager.LockSubject] fail closed.
type SubjectLocker interface {
	// LockSubject runs fn while holding subject's lock, and returns fn's
	// error. Another LockSubject of the same subject, in any process,
	// waits until fn returns.
	LockSubject(ctx context.Context, subject string, fn func(context.Context) error) error
}

// ErrNoSubjectLock is what [Manager.LockSubject] returns when the store
// cannot lock a subject (it does not implement [SubjectLocker]).
var ErrNoSubjectLock = errors.New("session: the store cannot lock an account (it does not implement SubjectLocker)")

// LockSubject runs fn while holding subject's lock in the store, so a
// read-check-write on the account (counting its credentials before removing
// one, recording an address change) is not interleaved with another one,
// from this replica or any other. A store that cannot lock refuses with an
// error wrapping ErrUnavailable and ErrNoSubjectLock, and fn does not run.
func (m *Manager) LockSubject(ctx context.Context, subject string, fn func(context.Context) error) error {
	if subject == "" {
		return errors.New("session: a subject is required")
	}
	locker, ok := m.cfg.Store.(SubjectLocker)
	if !ok {
		return fmt.Errorf("%w: %w", ErrUnavailable, ErrNoSubjectLock)
	}
	var fnErr error
	ran := false
	err := locker.LockSubject(ctx, subject, func(ctx context.Context) error {
		ran = true
		fnErr = fn(ctx)
		return fnErr
	})
	if ran {
		// fn's own error, unwrapped: the caller branches on it.
		if fnErr != nil {
			return fnErr
		}
		if err != nil {
			return fmt.Errorf("%w: release the account lock: %w", ErrUnavailable, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: take the account lock: %w", ErrUnavailable, err)
	}
	return nil
}

// LockSubject implements [SubjectLocker] for one process.
func (s *MemoryStore) LockSubject(ctx context.Context, subject string, fn func(context.Context) error) error {
	v, _ := s.subjectLocks.LoadOrStore(subject, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn(ctx)
}

// subjectLockSeed separates this package's advisory locks from any other
// user of pg_advisory_xact_lock in the same database.
const subjectLockSeed = "forge/pkg/session subject lock v1:"

// LockSubject implements [SubjectLocker] with a transaction-scoped Postgres
// advisory lock keyed by a hash of subject, so every replica on the database
// serializes on it. The lock (and its connection) is held while fn runs and
// released when the transaction ends; nothing is written.
func (s *PostgresStore) LockSubject(ctx context.Context, subject string, fn func(context.Context) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, subjectLockSeed+subject); err != nil {
		return err
	}
	if err := fn(ctx); err != nil {
		return err
	}
	return tx.Commit()
}
