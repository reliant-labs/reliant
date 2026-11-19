package retry

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff is a retry schedule: the wait before each retry, as a function of
// the retry number alone. The zero value retries immediately; set Initial.
type Backoff struct {
	// Initial is the wait before the first retry.
	Initial time.Duration

	// Multiplier grows the wait between retries: retry n waits
	// Initial·Multiplier^(n-1). Zero means 2 (doubling); 1 is a constant
	// wait; values below 1 are treated as 1.
	Multiplier float64

	// Max caps one wait before jitter is applied. Zero means no cap.
	Max time.Duration

	// Steps, when set, replaces the exponential: retry n waits
	// Steps[n-1], and every retry past the end waits the last step. Max
	// still caps a step. Use it for a ladder that is not geometric
	// (linear, or a hand-tuned table).
	Steps []time.Duration

	// Jitter randomizes each wait after the cap. The zero value adds none.
	Jitter Jitter

	// Rand returns a uniform integer in [0, n) for jitter. Nil means
	// math/rand/v2's Int64N. Tests set it to make jitter deterministic.
	Rand func(n int64) int64
}

// Base returns the wait before retry n (1-based) with the cap applied and no
// jitter. n below 1 is treated as 1.
func (b Backoff) Base(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	var d time.Duration
	if len(b.Steps) > 0 {
		d = b.Steps[min(n, len(b.Steps))-1]
	} else {
		d = b.exponential(n)
	}
	if b.Max > 0 && d > b.Max {
		d = b.Max
	}
	if d < 0 {
		d = 0
	}
	return d
}

func (b Backoff) exponential(n int) time.Duration {
	m := b.Multiplier
	if m == 0 {
		m = 2
	}
	if m <= 1 || b.Initial <= 0 {
		return b.Initial
	}
	f := float64(b.Initial) * math.Pow(m, float64(n-1))
	if math.IsInf(f, 1) || f >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(f)
}

// Delay returns the wait before retry n (1-based): [Backoff.Base] with the
// jitter applied.
func (b Backoff) Delay(n int) time.Duration {
	return b.Jitter.apply(b.Base(n), b.randN)
}

func (b Backoff) randN(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if b.Rand != nil {
		return b.Rand(n)
	}
	return rand.Int64N(n)
}

// Schedule returns a stateful walk over b, for a loop that owns its own
// waiting: a reconnect loop calls Next after each failure and Reset after a
// healthy session.
func (b Backoff) Schedule() *Schedule {
	return &Schedule{backoff: b}
}

// Schedule walks a [Backoff] one retry at a time. It is not safe for
// concurrent use.
type Schedule struct {
	backoff Backoff
	retries int
}

// Next counts one more retry and returns its wait.
func (s *Schedule) Next() time.Duration {
	s.retries++
	return s.backoff.Delay(s.retries)
}

// Reset starts the walk over: the next Next returns the first retry's wait.
func (s *Schedule) Reset() { s.retries = 0 }

// Retries reports how many retries Next has counted since the last Reset.
func (s *Schedule) Retries() int { return s.retries }

// Jitter randomizes a wait so that clients that failed together do not retry
// together. Build one with [FullJitter], [EqualJitter], [ProportionalJitter],
// [UpwardJitter] or [FixedJitter]; the zero value leaves the wait unchanged.
type Jitter struct {
	kind     jitterKind
	fraction float64
}

type jitterKind int

const (
	jitterNone jitterKind = iota
	jitterFull
	jitterEqual
	jitterProportional
	jitterUpward
	jitterFixed
)

// FullJitter waits a uniform random time in [0, d] — the spread that best
// de-synchronizes a crowd of clients, at the cost of sometimes retrying at
// once.
func FullJitter() Jitter { return Jitter{kind: jitterFull} }

// EqualJitter waits d/2 plus a uniform random time in [0, d/2]: never less
// than half the computed wait.
func EqualJitter() Jitter { return Jitter{kind: jitterEqual} }

// ProportionalJitter waits a uniform random time in [d - f·d, d + f·d): f
// either side of the computed wait. f of 0.25 is ±25%; f is clamped to [0, 1].
func ProportionalJitter(f float64) Jitter {
	return Jitter{kind: jitterProportional, fraction: min(max(f, 0), 1)}
}

// UpwardJitter waits d plus a uniform random time in [0, f·d]: never less
// than the computed wait. f of 0.2 adds up to 20%; a negative f is 0.
func UpwardJitter(f float64) Jitter { return Jitter{kind: jitterUpward, fraction: max(f, 0)} }

// FixedJitter waits exactly d + f·d. It is not random; it exists so a
// schedule that pads every wait by a constant fraction can be expressed
// exactly.
func FixedJitter(f float64) Jitter { return Jitter{kind: jitterFixed, fraction: max(f, 0)} }

func (j Jitter) apply(d time.Duration, randN func(int64) int64) time.Duration {
	if d <= 0 {
		return d
	}
	span := time.Duration(float64(d) * j.fraction)
	var out time.Duration
	switch j.kind {
	case jitterFull:
		out = time.Duration(randInclusive(randN, int64(d)))
	case jitterEqual:
		out = d - d/2 + time.Duration(randInclusive(randN, int64(d/2)))
	case jitterProportional:
		out = d - span + time.Duration(randN(int64(2*span)))
	case jitterUpward:
		out = d + time.Duration(randInclusive(randN, int64(span)))
	case jitterFixed:
		out = d + span
	default:
		return d
	}
	if out < 0 {
		return time.Duration(math.MaxInt64) // overflowed
	}
	return out
}

// randInclusive returns a uniform integer in [0, x].
func randInclusive(randN func(int64) int64, x int64) int64 {
	if x == math.MaxInt64 {
		return randN(x)
	}
	return randN(x + 1)
}
