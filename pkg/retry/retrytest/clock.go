// Package retrytest is the test double for forge/pkg/retry: a Clock that
// never sleeps, so a test of a retry loop runs instantly and asserts the
// exact waits.
package retrytest

import (
	"context"
	"sync"
	"time"
)

// Clock is a retry.Clock whose Sleep records the wait and advances the
// clock's own time instead of sleeping. It is safe for concurrent use.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

// NewClock returns a Clock that starts at start.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start}
}

// Now returns the clock's time: start plus every wait and Advance so far.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep records d and advances the clock by it, without waiting. It returns
// ctx's error, recording nothing, when ctx has already ended.
func (c *Clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

// Advance moves the clock forward by d, as time spent inside an attempt.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Sleeps returns every wait Sleep recorded, in order.
func (c *Clock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}
