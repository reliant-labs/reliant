// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"time"
)

const (
	lockPollMin = 2 * time.Millisecond
	lockPollMax = 100 * time.Millisecond
)

// pollLock calls try until it reports the lock taken, waiting between attempts
// (doubling, up to lockPollMax) and giving up with ctx's error once ctx is
// done. The first attempt is made even when ctx is already done, so an
// uncontended lock is never refused for want of time.
func pollLock(ctx context.Context, try func() (bool, error)) error {
	wait := lockPollMin
	for {
		ok, err := try()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		wait = min(wait*2, lockPollMax)
	}
}

// pathLock is a mutex for one worktree path that a waiter can give up on: a
// reconcile that cannot get it before its deadline reports the row deferred
// instead of blocking past the reply it owes.
type pathLock chan struct{}

func newPathLock() pathLock { return make(pathLock, 1) }

func (l pathLock) lock(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	default:
	}
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l pathLock) tryLock() bool {
	select {
	case l <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l pathLock) unlock() { <-l }
