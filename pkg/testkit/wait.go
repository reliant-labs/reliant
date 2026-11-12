package testkit

// Waiting for an event: the replacement for a fixed sleep.
//
// A test that sleeps 100ms and then asserts is guessing how long the work
// takes. On an idle laptop the guess holds; on a machine running ten gates
// a goroutine can wait seconds for a CPU, the assertion reads state that is
// not there yet, and the test fails for no reason in the code. That shape —
// a fixed sleep, a one-second deadline, an upper bound on elapsed time — is
// the largest single source of flaky tests forge has measured, and `forge
// lint` flags it (forge-test-fixed-sleep, forge-test-tight-deadline,
// forge-test-wall-clock-bound).
//
// Eventually and Receive wait for the event itself. They return the moment
// it happens, so a passing test pays only for the work, and they fail only
// after DefaultWaitTimeout: a deadline long enough that a busy machine never
// reaches it, which costs a passing test nothing.
//
//	testkit.Eventually(t, "the worker drains the queue", func() bool {
//		return q.Len() == 0
//	})
//	resp := testkit.Receive(t, "the reply", replies)
//
// A test with a `go test -timeout` deadline nearer than DefaultWaitTimeout
// waits only until shortly before it, so a hang is reported as this wait,
// by name, rather than as the test binary's panic dump.

import (
	"testing"
	"time"
)

// DefaultWaitTimeout is how long Eventually and Receive wait before they
// fail the test. It is deliberately generous: they return as soon as the
// event happens, so only a test that is already failing ever waits it out.
const DefaultWaitTimeout = 30 * time.Second

// maxPollInterval caps Eventually's backoff between checks.
const maxPollInterval = 100 * time.Millisecond

// deadlineGrace is how long before the test binary's own -timeout a wait
// gives up, so its failure, not the binary's panic, is what gets reported.
const deadlineGrace = 2 * time.Second

// WaitOption configures Eventually and Receive.
type WaitOption func(*waitConfig)

type waitConfig struct {
	timeout time.Duration
}

// Within sets how long the wait lasts before it fails the test, in place of
// DefaultWaitTimeout. Raise it for work that is slow by nature (a container
// boot); lowering it only makes the test flakier, since a passing wait
// returns as soon as the event happens either way.
func Within(d time.Duration) WaitOption {
	return func(c *waitConfig) { c.timeout = d }
}

// Eventually polls cond until it returns true, and fails the test with
// what in the message if it has not by the deadline. cond runs on the
// calling goroutine, first immediately, then with a backoff from 1ms up to
// 100ms between checks.
func Eventually(t testing.TB, what string, cond func() bool, opts ...WaitOption) {
	t.Helper()
	timeout := waitTimeout(t, opts)
	deadline := time.Now().Add(timeout)
	interval := time.Millisecond
	for !cond() {
		if !time.Now().Before(deadline) {
			t.Fatalf("testkit.Eventually: %s did not happen within %s", what, timeout)
			return
		}
		time.Sleep(min(interval, time.Until(deadline)))
		interval = min(interval*2, maxPollInterval)
	}
}

// Receive waits for a value on ch and returns it, failing the test with
// what in the message if none arrives by the deadline. A closed channel
// delivers its zero value, as a bare <-ch does, so a `done` channel that
// signals by closing works too.
func Receive[T any](t testing.TB, what string, ch <-chan T, opts ...WaitOption) T {
	t.Helper()
	timeout := waitTimeout(t, opts)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
		t.Fatalf("testkit.Receive: %s did not arrive within %s", what, timeout)
		var zero T
		return zero
	}
}

// waitTimeout resolves the options, then shortens the wait to end before
// the test binary's -timeout deadline when that comes first.
func waitTimeout(t testing.TB, opts []WaitOption) time.Duration {
	cfg := waitConfig{timeout: DefaultWaitTimeout}
	for _, o := range opts {
		o(&cfg)
	}
	if d, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if end, has := d.Deadline(); has {
			if left := time.Until(end) - deadlineGrace; left < cfg.timeout {
				cfg.timeout = max(left, 0)
			}
		}
	}
	return cfg.timeout
}
