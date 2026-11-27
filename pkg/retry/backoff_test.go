package retry

import (
	"math"
	"testing"
	"time"
)

// Rand seams that pin jitter to either end of its range, or the middle.
var (
	randLow  = func(int64) int64 { return 0 }
	randHigh = func(n int64) int64 { return n - 1 }
	randMid  = func(n int64) int64 { return n / 2 }
)

func TestBaseSchedules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		b    Backoff
		want []time.Duration // retries 1..len(want)
	}{
		{"doubling by default", Backoff{Initial: time.Second}, secs(1, 2, 4, 8, 16)},
		{"cap binds", Backoff{Initial: time.Second, Max: 5 * time.Second}, secs(1, 2, 4, 5, 5)},
		{"multiplier 3", Backoff{Initial: time.Second, Multiplier: 3}, secs(1, 3, 9, 27)},
		{"multiplier 1 is constant", Backoff{Initial: time.Second, Multiplier: 1}, secs(1, 1, 1)},
		{"multiplier below 1 is constant", Backoff{Initial: time.Second, Multiplier: 0.5}, secs(1, 1, 1)},
		{"NaN multiplier uses default", Backoff{Initial: time.Second, Multiplier: math.NaN()}, secs(1, 2, 4)},
		{"first delay is exact", Backoff{Initial: math.MaxInt64 - 20}, []time.Duration{math.MaxInt64 - 20}},
		{"steps repeat the last", Backoff{Steps: secs(10, 20, 30, 60)}, secs(10, 20, 30, 60, 60, 60)},
		{"max caps a step", Backoff{Steps: secs(1, 10), Max: 5 * time.Second}, secs(1, 5, 5)},
		{"zero value retries at once", Backoff{}, secs(0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for i, want := range tc.want {
				if got := tc.b.Base(i + 1); got != want {
					t.Errorf("Base(%d) = %v, want %v", i+1, got, want)
				}
			}
		})
	}
}

func TestBaseClampsRetryNumberAndOverflow(t *testing.T) {
	t.Parallel()
	b := Backoff{Initial: time.Second}
	if got := b.Base(0); got != time.Second {
		t.Errorf("Base(0) = %v, want the first retry's %v", got, time.Second)
	}
	if got := b.Base(-3); got != time.Second {
		t.Errorf("Base(-3) = %v, want %v", got, time.Second)
	}
	if got := b.Base(200); got != time.Duration(math.MaxInt64) {
		t.Errorf("uncapped Base(200) = %v, want the largest Duration, not an overflow", got)
	}
	capped := Backoff{Initial: time.Second, Max: time.Minute}
	if got := capped.Base(200); got != time.Minute {
		t.Errorf("capped Base(200) = %v, want %v", got, time.Minute)
	}
}

func TestJitterRanges(t *testing.T) {
	t.Parallel()
	const d = 8 * time.Second
	cases := []struct {
		name      string
		j         Jitter
		low, high time.Duration // the result at randLow and at randHigh
	}{
		{"none", Jitter{}, d, d},
		{"full", FullJitter(), 0, d},
		{"equal", EqualJitter(), d / 2, d},
		{"proportional ±25%", ProportionalJitter(0.25), 6 * time.Second, 10*time.Second - 1},
		{"proportional clamps above 1", ProportionalJitter(3), 0, 2*d - 1},
		{"upward 20%", UpwardJitter(0.2), d, d + d/5},
		{"fixed 20%", FixedJitter(0.2), d + d/5, d + d/5},
		{"negative fractions are zero", UpwardJitter(-1), d, d},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lo := Backoff{Initial: d, Multiplier: 1, Jitter: tc.j, Rand: randLow}.Delay(1)
			hi := Backoff{Initial: d, Multiplier: 1, Jitter: tc.j, Rand: randHigh}.Delay(1)
			if lo != tc.low || hi != tc.high {
				t.Errorf("Delay range = [%v, %v], want [%v, %v]", lo, hi, tc.low, tc.high)
			}
		})
	}
}

func TestJitterNeverOverflows(t *testing.T) {
	t.Parallel()
	const limit = time.Duration(math.MaxInt64)
	for _, tc := range []struct {
		name      string
		d         time.Duration
		j         Jitter
		low, high time.Duration
	}{
		{"full at limit", limit, FullJitter(), 0, limit},
		{"equal at limit", limit, EqualJitter(), limit - limit/2, limit},
		{"proportional at limit", limit, ProportionalJitter(1), 0, limit},
		{"proportional upper bound overflow", limit - 20, ProportionalJitter(0.5), limit - 20 - (1 << 62), limit},
		{"proportional positive infinity", 10, ProportionalJitter(math.Inf(1)), 0, 19},
		{"proportional NaN", 10, ProportionalJitter(math.NaN()), 10, 10},
		{"upward at limit", limit, UpwardJitter(1), limit, limit},
		{"upward positive infinity", 10, UpwardJitter(math.Inf(1)), 10, limit},
		{"upward finite fraction overflowing float64", time.Second, UpwardJitter(math.MaxFloat64), time.Second, limit},
		{"upward NaN", 10, UpwardJitter(math.NaN()), 10, 10},
		{"fixed at limit", limit, FixedJitter(1), limit, limit},
		{"fixed positive infinity", 10, FixedJitter(math.Inf(1)), limit, limit},
		{"fixed negative infinity", 10, FixedJitter(math.Inf(-1)), 10, 10},
		{"fixed NaN", 10, FixedJitter(math.NaN()), 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := Backoff{Initial: tc.d, Jitter: tc.j, Rand: randLow}
			low := b.Delay(1)
			b.Rand = randHigh
			high := b.Delay(1)
			if low != tc.low || high != tc.high {
				t.Errorf("Delay range = [%v, %v], want [%v, %v]", low, high, tc.low, tc.high)
			}
		})
	}
}

func TestDefaultRandStaysInRange(t *testing.T) {
	t.Parallel()
	b := Backoff{Initial: time.Second, Multiplier: 1, Jitter: ProportionalJitter(0.5)}
	for range 1000 {
		if got := b.Delay(1); got < 500*time.Millisecond || got >= 1500*time.Millisecond {
			t.Fatalf("Delay = %v, want within [500ms, 1.5s)", got)
		}
	}
}

func TestScheduleWalksAndResets(t *testing.T) {
	t.Parallel()
	s := Backoff{Initial: time.Second, Max: 15 * time.Second}.Schedule()
	var got []time.Duration
	for range 6 {
		got = append(got, s.Next())
	}
	assertDurations(t, got, secs(1, 2, 4, 8, 15, 15))
	if s.Retries() != 6 {
		t.Errorf("Retries() = %d, want 6", s.Retries())
	}
	s.Reset()
	if got := s.Next(); got != time.Second {
		t.Errorf("Next after Reset = %v, want %v", got, time.Second)
	}
}

// The tests below hold the package to the hand-rolled schedules it replaces:
// each reference function is one copied from a real call site, and the
// Backoff that stands in for it must agree at every retry and every jitter
// draw.

func TestExpressesTxSerializationRetry(t *testing.T) {
	t.Parallel()
	// base 50ms, doubled per retry (retry index from 0), capped at 1s, then ±25%.
	reference := func(attempt int, randN func(int64) int64) time.Duration {
		delay := 50 * time.Millisecond * time.Duration(1<<uint(attempt))
		if delay > time.Second {
			delay = time.Second
		}
		jitter := time.Duration(randN(int64(delay / 2)))
		return delay + jitter - delay/4
	}
	for _, rnd := range []func(int64) int64{randLow, randMid, randHigh} {
		b := Backoff{Initial: 50 * time.Millisecond, Max: time.Second, Jitter: ProportionalJitter(0.25), Rand: rnd}
		for attempt := 0; attempt < 10; attempt++ {
			if got, want := b.Delay(attempt+1), reference(attempt, rnd); got != want {
				t.Errorf("retry %d: %v, want %v", attempt+1, got, want)
			}
		}
	}
}

func TestExpressesRateLimitLadder(t *testing.T) {
	t.Parallel()
	// min(60s, 2s<<(n-1)), plus a uniform 0..20%, inclusive.
	reference := func(attempt int, randN func(int64) int64) time.Duration {
		d := time.Minute
		if attempt < 6 {
			d = min(time.Minute, 2*time.Second<<(attempt-1))
		}
		return d + time.Duration(randN(int64(d/5)+1))
	}
	for _, rnd := range []func(int64) int64{randLow, randMid, randHigh} {
		b := Backoff{Initial: 2 * time.Second, Max: time.Minute, Jitter: UpwardJitter(0.2), Rand: rnd}
		for n := 1; n <= 10; n++ {
			if got, want := b.Delay(n), reference(n, rnd); got != want {
				t.Errorf("retry %d: %v, want %v", n, got, want)
			}
		}
	}
}

func TestExpressesFixedPaddedLadder(t *testing.T) {
	t.Parallel()
	// 2000ms·2^(n-1) plus a constant 20%, no cap.
	reference := func(attempt int) time.Duration {
		backoffMs := 2000 * (1 << (attempt - 1))
		jitterMs := int(float64(backoffMs) * 0.2)
		return time.Duration(backoffMs+jitterMs) * time.Millisecond
	}
	b := Backoff{Initial: 2 * time.Second, Jitter: FixedJitter(0.2)}
	for n := 1; n <= 8; n++ {
		if got, want := b.Delay(n), reference(n); got != want {
			t.Errorf("retry %d: %v, want %v", n, got, want)
		}
	}
}

func TestExpressesShiftedDoublingWithCap(t *testing.T) {
	t.Parallel()
	// interval << min(attempts-1, 5), capped at 10m.
	reference := func(attempts int) time.Duration {
		backoff := 30 * time.Second << min(attempts-1, 5)
		return min(backoff, 10*time.Minute)
	}
	// 5s·2^min(attempts, 10), capped at 5m, counted after attempts++.
	referenceFromTwo := func(attempts int) time.Duration {
		wait := time.Duration(1<<min(attempts, 10)) * 5 * time.Second
		return min(wait, 5*time.Minute)
	}
	watch := Backoff{Initial: 30 * time.Second, Max: 10 * time.Minute}
	check := Backoff{Initial: 10 * time.Second, Max: 5 * time.Minute}
	for n := 1; n <= 15; n++ {
		if got, want := watch.Delay(n), reference(n); got != want {
			t.Errorf("watch retry %d: %v, want %v", n, got, want)
		}
		if got, want := check.Delay(n), referenceFromTwo(n); got != want {
			t.Errorf("check retry %d: %v, want %v", n, got, want)
		}
	}
}

func secs(vs ...float64) []time.Duration {
	out := make([]time.Duration, len(vs))
	for i, v := range vs {
		out[i] = time.Duration(v * float64(time.Second))
	}
	return out
}

func assertDurations(t *testing.T, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d waits %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("waits = %v, want %v", got, want)
		}
	}
}
