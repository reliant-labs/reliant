// Copyright (c) 2025 Reliant Labs
package local

import (
	"testing"
	"time"
)

// TestRetryBackoff_Ladder pins the wait before each retry when the server
// sent no Retry-After: 2s doubling plus a fixed 20%, no cap.
func TestRetryBackoff_Ladder(t *testing.T) {
	want := []time.Duration{
		2400 * time.Millisecond, 4800 * time.Millisecond, 9600 * time.Millisecond,
		19200 * time.Millisecond, 38400 * time.Millisecond, 76800 * time.Millisecond,
		153600 * time.Millisecond, 307200 * time.Millisecond, 614400 * time.Millisecond,
	}
	for i, w := range want {
		if got := retryBackoff.Delay(i + 1); got != w {
			t.Errorf("retryBackoff.Delay(%d) = %v, want %v", i+1, got, w)
		}
	}
}
