package inbox

import (
	"context"
	"fmt"
	"sync"
)

// freshFlight coalesces concurrent calls of one key into shared runs, like
// singleflight, but a call only ever joins a run that STARTS after the call
// arrived: what it returns is never older than the call. singleflight hands a
// call the run already in flight, which may have started before the caller's
// own write committed — answering a question in the inbox, then refetching
// the list, got the list from before the answer, with the question still in
// it (the row disappeared, came back, and went again on the next refetch).
//
// Per key there is at most one run in flight (current) and one queued behind
// it (next). A call with nothing in flight starts a run; any other call joins
// next, which starts when current finishes. So a burst during one run costs
// one more run, not one per call, and a key never has more than two.
//
// A run executes in a goroutine of its own, so a caller that gives up returns
// at once (on its ctx.Done) without failing the run for the others waiting on
// it. A queued run whose every caller gave up before it started is skipped.
// The zero value is ready to use.
type freshFlight[T any] struct {
	mu   sync.Mutex
	keys map[string]*flightSlot[T]
}

// flightSlot is one key's runs: the one in flight and the one queued behind it.
type flightSlot[T any] struct {
	current, next *flightRun[T]
}

// flightRun is one shared execution of fn. done closes once val and err are set.
type flightRun[T any] struct {
	fn      func() (T, error)
	done    chan struct{}
	val     T
	err     error
	waiters int // callers still waiting; guarded by freshFlight.mu
}

// Do returns the result of a run of fn for key that started after this call
// arrived: a new run when none is in flight, otherwise the run queued behind
// the one in flight (whose fn is the first queued caller's — callers of one key
// must be interchangeable). It returns ctx.Err() if ctx is canceled before
// registration or when the result is received.
func (g *freshFlight[T]) Do(ctx context.Context, key string, fn func() (T, error)) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	g.mu.Lock()
	// Cancellation may have happened while waiting for the lock.
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		var zero T
		return zero, err
	}
	if g.keys == nil {
		g.keys = map[string]*flightSlot[T]{}
	}
	slot := g.keys[key]
	var run *flightRun[T]
	switch {
	case slot == nil:
		run = &flightRun[T]{fn: fn, done: make(chan struct{})}
		g.keys[key] = &flightSlot[T]{current: run}
		go g.execute(key, run)
	case slot.next == nil:
		run = &flightRun[T]{fn: fn, done: make(chan struct{})}
		slot.next = run
	default:
		run = slot.next
	}
	run.waiters++
	g.mu.Unlock()

	select {
	case <-run.done:
		// Prefer cancellation when both channels are ready.
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
		return run.val, run.err
	case <-ctx.Done():
		g.mu.Lock()
		run.waiters--
		g.mu.Unlock()
		var zero T
		return zero, ctx.Err()
	}
}

// execute runs run.fn, publishes its result, then starts the queued run if any
// caller still waits on it, or forgets the key.
func (g *freshFlight[T]) execute(key string, run *flightRun[T]) {
	for run != nil {
		run.val, run.err = callRecovering(run.fn)
		close(run.done)

		g.mu.Lock()
		slot := g.keys[key]
		next := slot.next
		slot.next = nil
		if next != nil && next.waiters > 0 {
			slot.current = next
			run = next
		} else {
			delete(g.keys, key)
			run = nil
		}
		g.mu.Unlock()
	}
}

// callRecovering is fn() with a panic returned as an error: the run executes
// in a goroutine of its own, where an unrecovered panic would end the process
// rather than the one request.
func callRecovering[T any](fn func() (T, error)) (val T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("shared read panicked: %v", r)
		}
	}()
	return fn()
}

// queued is how many callers wait on key's queued run (next): for tests, to
// know a call has joined before releasing the run in flight.
func (g *freshFlight[T]) queued(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if slot := g.keys[key]; slot != nil && slot.next != nil {
		return slot.next.waiters
	}
	return 0
}
