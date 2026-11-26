// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"errors"
	"sync"

	"github.com/reliant-labs/reliant/internal/dblock"
)

// LeaderLock elects the one worker that drives a repository's queue.
type LeaderLock interface {
	// TryLead takes the repository's lead without waiting. ok is false,
	// with a no-op release, when another worker holds it. A held lead
	// lasts until release is called (or its holder dies).
	TryLead(ctx context.Context, repoID string) (release func(), ok bool, err error)
}

// NamedLocker takes session-level named advisory locks: dblock.Service.
type NamedLocker interface {
	TryLockNamed(ctx context.Context, name string) (release func(), ok bool, err error)
}

var _ NamedLocker = dblock.Service(nil)

// AdvisoryLeader is the [LeaderLock] over a session-level Postgres advisory
// lock keyed by the repository's id ([LeaderKey]), taken through
// internal/dblock on a dedicated session (db.Repo.SessionPool). A crashed
// worker never strands it: the database drops it with the session.
type AdvisoryLeader struct {
	Locks NamedLocker
}

// LeaderKey names the lock of a repository's lead.
func LeaderKey(repoID string) string { return "mergequeue:" + repoID }

// TryLead implements LeaderLock.
func (l AdvisoryLeader) TryLead(ctx context.Context, repoID string) (func(), bool, error) {
	if l.Locks == nil {
		return nil, false, errors.New("mergequeue: AdvisoryLeader needs Locks")
	}
	release, ok, err := l.Locks.TryLockNamed(ctx, LeaderKey(repoID))
	if err != nil || !ok {
		return release, ok, err
	}
	var once sync.Once
	return func() { once.Do(release) }, true, nil
}
