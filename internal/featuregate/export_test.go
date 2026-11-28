// Copyright (c) 2026 Reliant Labs

package featuregate

import (
	"testing"
	"time"
)

// SetTimeoutsForTest shortens the membership timeouts for the length of a
// test. It swaps package state, so the test must not run in parallel.
func SetTimeoutsForTest(tb testing.TB, check, allowedForUser time.Duration) {
	tb.Helper()
	prevCheck, prevAll := checkTimeout, allowedForUserTimeout
	checkTimeout, allowedForUserTimeout = check, allowedForUser
	tb.Cleanup(func() { checkTimeout, allowedForUserTimeout = prevCheck, prevAll })
}
