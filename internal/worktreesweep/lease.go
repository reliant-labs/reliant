// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"sync"
	"time"
)

// inFlightHold is how long a row stays leased after a request for it timed out.
// The daemon may still be working on it (an older daemon has no deadline of its
// own, and a wedged one has none that holds), and sending it the same rows
// again only stacks a second pass of git work behind the first. The daemon now
// replies at its own deadline, so a timeout means it was not heard from at all.
const inFlightHold = 5 * time.Minute

// leaseKey names one row on one daemon: the same row asked of two daemons (an
// unowned row is offered to every connected one) is two independent requests.
type leaseKey struct{ daemonID, worktreeID string }

// leases is the set of rows with a request in flight, so a pass, a settle and a
// retry never put the same row on one daemon twice at once.
type leases struct {
	mu    sync.Mutex
	until map[leaseKey]time.Time // zero: held by a request still running
}

// acquire takes the lease of every key not already leased, and returns those.
// A lease past its deadline is free.
func (l *leases) acquire(now time.Time, keys []leaseKey) []leaseKey {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.until == nil {
		l.until = map[leaseKey]time.Time{}
	}
	var got []leaseKey
	for _, k := range keys {
		if until, ok := l.until[k]; ok && (until.IsZero() || now.Before(until)) {
			continue
		}
		l.until[k] = time.Time{}
		got = append(got, k)
	}
	return got
}

func (l *leases) release(keys []leaseKey) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.until, k)
	}
}

// holdUntil keeps the keys leased until the given time, for a request that
// timed out with its rows possibly still being worked on.
func (l *leases) holdUntil(keys []leaseKey, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		l.until[k] = until
	}
}
