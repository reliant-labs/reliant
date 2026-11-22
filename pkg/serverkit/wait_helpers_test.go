package serverkit_test

import (
	"testing"
	"time"
)

// waitTimeout bounds every wait in these tests for an event. It is long
// on purpose: a wait returns the moment its event happens, so only a test
// that is already failing pays it, and a loaded machine never reaches it.
// (testkit.Eventually / testkit.Receive will replace these helpers.)
const waitTimeout = 30 * time.Second

// receive returns the next value from ch, failing the test if none arrives
// within waitTimeout.
func receive[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitTimeout):
		t.Fatalf("timed out after %s waiting for %s", waitTimeout, what)
	}
	var zero T
	return zero
}

// eventually polls cond until it holds, failing the test if it does not
// within waitTimeout.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", waitTimeout, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
