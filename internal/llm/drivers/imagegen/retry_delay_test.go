// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"net/http"
	"testing"
	"time"
)

// TestRetryDelay_Ladder pins the wait before each retry: base doubling with
// no jitter for a transient status, Retry-After replacing it, the constant
// base for an empty result, and the attempt caps for both.
func TestRetryDelay_Ladder(t *testing.T) {
	base := 2 * time.Second
	unavailable := &APIError{StatusCode: http.StatusServiceUnavailable}
	cases := []struct {
		name    string
		attempt int
		err     error
		want    time.Duration
		retry   bool
	}{
		{"503 attempt 1", 1, unavailable, 2 * time.Second, true},
		{"503 attempt 2", 2, unavailable, 4 * time.Second, true},
		{"503 attempt 3 exhausted", 3, unavailable, 0, false},
		{"retry-after wins", 2, &APIError{StatusCode: http.StatusBadGateway, RetryAfter: 7 * time.Second}, 7 * time.Second, true},
		{"empty result attempt 1", 1, &APIError{EmptyResult: true}, 2 * time.Second, true},
		{"empty result attempt 2 exhausted", 2, &APIError{EmptyResult: true}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, retry := retryDelay(tc.attempt, tc.err, base)
			if got != tc.want || retry != tc.retry {
				t.Fatalf("retryDelay = (%v, %v), want (%v, %v)", got, retry, tc.want, tc.retry)
			}
		})
	}
}
