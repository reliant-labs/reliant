package controller

import (
	"testing"
	"time"
)

func TestBackoffNext_HappyPath(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: time.Minute, Factor: 2.0}

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, time.Minute},  // capped
		{20, time.Minute}, // capped
	}
	for _, tc := range cases {
		got := b.Next(tc.attempt)
		if got != tc.want {
			t.Errorf("attempt=%d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestBackoffNext_NegativeAttempt(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: time.Minute, Factor: 2.0}
	if got := b.Next(-1); got != time.Second {
		t.Errorf("Next(-1) = %v, want 1s", got)
	}
}

func TestBackoffNext_FactorOneOrLess(t *testing.T) {
	// Factor <= 1 should collapse to Initial.
	b := Backoff{Initial: 5 * time.Second, Max: time.Minute, Factor: 1.0}
	for i := 0; i < 5; i++ {
		if got := b.Next(i); got != 5*time.Second {
			t.Errorf("attempt=%d Factor=1.0: got %v, want 5s", i, got)
		}
	}

	b = Backoff{Initial: 5 * time.Second, Max: time.Minute, Factor: 0.5}
	if got := b.Next(3); got != 5*time.Second {
		t.Errorf("Factor=0.5 attempt=3: got %v, want 5s", got)
	}
}

func TestBackoffNext_OverflowGuard(t *testing.T) {
	// A huge attempt count should cap at Max rather than overflow.
	b := Backoff{Initial: time.Second, Max: time.Minute, Factor: 2.0}
	if got := b.Next(10000); got != time.Minute {
		t.Errorf("attempt=10000: got %v, want 1m (capped)", got)
	}
}
