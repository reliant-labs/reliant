package cli

import (
	"context"
	"time"
)

// holdPoll is how often a supervising `forge env up` checks whether anything
// it started is still running. A var so tests do not wait a real second.
var holdPoll = time.Second

// awaitHoldEnd blocks a supervising run's hold until ctx is cancelled (a
// signal: Ctrl-C, a harness's SIGTERM) or every process the run started has
// exited. It reports which: stackGone is true for the second.
//
// The second is how `forge env down <env>` from another terminal reaches a
// `forge env up --watch` it did not start: down signals the stack's process
// trees, and the hold used to keep waiting on a signal that never came — a
// supervisor holding nothing, its exited children's siblings left as zombies
// until someone killed it. A supervisor with nothing left to supervise exits.
func awaitHoldEnd(ctx context.Context, procs *procRegistry) (stackGone bool) {
	tick := time.NewTicker(holdPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
			if procs.allExited() {
				return true
			}
		}
	}
}

// allExited reports whether this registry started at least one process and
// every one has been observed to exit. A process whose exit is not observed
// (no Wait) counts as running.
func (p *procRegistry) allExited() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.processes) == 0 {
		return false
	}
	for _, mp := range p.processes {
		if mp.done == nil {
			return false
		}
		select {
		case <-mp.done:
		default:
			return false
		}
	}
	return true
}
