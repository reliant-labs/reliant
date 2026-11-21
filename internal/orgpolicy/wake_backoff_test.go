// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"testing"
	"time"
)

// TestWakeBackoffSchedule pins the wait after each failed wake to the ladder
// it had before it moved onto forge/pkg/retry: WatchInterval << min(n-1, 5),
// at most wakeBackoffMax.
func TestWakeBackoffSchedule(t *testing.T) {
	t.Parallel()
	for attempts := 1; attempts <= 40; attempts++ {
		want := min(WatchInterval<<min(attempts-1, 5), wakeBackoffMax)
		if got := wakeBackoff.Delay(attempts); got != want {
			t.Errorf("attempt %d: %v, want %v", attempts, got, want)
		}
	}
	if got := wakeBackoff.Delay(1); got != 30*time.Second {
		t.Errorf("first wait = %v, want 30s", got)
	}
}
