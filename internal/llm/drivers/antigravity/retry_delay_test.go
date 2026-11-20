// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"net/http"
	"testing"
	"time"
)

// TestRetryDelay_Ladder pins the wait before each retry: 2s doubling plus a
// fixed 20%, no cap, and a positive integer Retry-After replacing it.
func TestRetryDelay_Ladder(t *testing.T) {
	want := []time.Duration{
		2400 * time.Millisecond, 4800 * time.Millisecond, 9600 * time.Millisecond,
		19200 * time.Millisecond, 38400 * time.Millisecond, 76800 * time.Millisecond,
		153600 * time.Millisecond, 307200 * time.Millisecond, 614400 * time.Millisecond,
	}
	for i, w := range want {
		if got := retryDelay(i+1, nil); got != w {
			t.Errorf("retryDelay(%d) = %v, want %v", i+1, got, w)
		}
	}

	resp := &http.Response{Header: http.Header{"Retry-After": []string{"7"}}}
	if got := retryDelay(3, resp); got != 7*time.Second {
		t.Errorf("retryDelay with Retry-After: 7 = %v, want 7s", got)
	}
	resp.Header.Set("Retry-After", "0")
	if got := retryDelay(3, resp); got != 9600*time.Millisecond {
		t.Errorf("retryDelay with Retry-After: 0 = %v, want the ladder's 9.6s", got)
	}
}
