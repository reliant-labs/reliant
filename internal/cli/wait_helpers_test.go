package cli

import (
	"testing"
	"time"
)

// testWaitTimeout bounds a test's wait for an event. It is long on purpose:
// the wait returns the moment the event happens, so only a test that is
// already failing pays it, and a loaded machine never reaches it.
// (testkit.Eventually / testkit.Receive will replace these helpers.)
const testWaitTimeout = 30 * time.Second

// awaitRecv returns the next value from ch, failing the test if none
// arrives within testWaitTimeout.
func awaitRecv[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(testWaitTimeout):
		t.Fatalf("timed out after %s waiting for %s", testWaitTimeout, what)
	}
	var zero T
	return zero
}

// awaitCond polls cond until it holds, failing the test if it does not
// within testWaitTimeout.
func awaitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", testWaitTimeout, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
