package workflowcheck

import (
	"testing"
	"time"
)

// TestFailureBackoffSchedule pins the wait after each failed write to the
// ladder it had before it moved onto forge/pkg/retry: 5s·2^min(attempts, 10),
// at most maxBackoff, with attempts counted after the failure.
func TestFailureBackoffSchedule(t *testing.T) {
	t.Parallel()
	for attempts := 1; attempts <= 40; attempts++ {
		want := min(time.Duration(1<<min(attempts, 10))*5*time.Second, maxBackoff)
		if got := failureBackoff.Delay(attempts); got != want {
			t.Errorf("attempts %d: %v, want %v", attempts, got, want)
		}
	}
}
