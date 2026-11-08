// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"path/filepath"
	"sync"
)

// checkoutTeardowns serializes the commands that delete a project checkout
// (git.remove, git.reclone) per path, and git.await_teardown waits on it.
//
// The server stops waiting for a delete after its request budget, but the
// machine does not stop deleting: git.remove ignores cancellation, and the
// daemon runs every command on its own goroutine. Before the server lets a
// branch workspace of the checkout in again after a delete whose outcome it
// never heard, it needs to know THAT delete is over, which a later command's
// answer says only if the later command could not have run beside it. Each
// destructive command holds the path's lock for as long as it runs, so a
// git.await_teardown that got the lock was answered after every one sent
// before it finished.
var checkoutTeardowns = newPathLocks()

// pathLocks is a set of mutexes keyed by a cleaned path whose acquisition can
// be abandoned with a context. An entry exists only while someone holds or
// waits for it.
type pathLocks struct {
	mu   sync.Mutex
	held map[string]*pathLock
}

type pathLock struct {
	slot chan struct{}
	refs int
}

func newPathLocks() *pathLocks {
	return &pathLocks{held: map[string]*pathLock{}}
}

// lock takes path's lock, waiting until it is free or ctx ends. The returned
// func releases it.
func (l *pathLocks) lock(ctx context.Context, path string) (func(), error) {
	key := filepath.Clean(path)
	l.mu.Lock()
	pl := l.held[key]
	if pl == nil {
		pl = &pathLock{slot: make(chan struct{}, 1)}
		l.held[key] = pl
	}
	pl.refs++
	l.mu.Unlock()

	forget := func() {
		l.mu.Lock()
		pl.refs--
		if pl.refs == 0 {
			delete(l.held, key)
		}
		l.mu.Unlock()
	}
	select {
	case pl.slot <- struct{}{}:
		return func() {
			<-pl.slot
			forget()
		}, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}
