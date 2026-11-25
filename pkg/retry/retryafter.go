package retry

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseRetryAfter reads how long a server asked the client to wait. It
// checks, in order, Retry-After-Ms (milliseconds, as OpenAI and Anthropic
// send it) and Retry-After (seconds, or an HTTP date measured from now).
// Fractional values are honoured; a negative value or a date already past is
// a wait of zero. It reports false when neither header holds a value it can
// parse.
func ParseRetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("Retry-After-Ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(ms) {
			return clampDuration(ms * float64(time.Millisecond)), true
		}
	}
	return ParseRetryAfterValue(h.Get("Retry-After"), now)
}

// ParseRetryAfterValue parses one Retry-After value: seconds (fractional
// allowed) or an HTTP date measured from now. A negative value or a date
// already past is a wait of zero.
func ParseRetryAfterValue(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if math.IsNaN(secs) {
			return 0, false
		}
		return clampDuration(secs * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

func clampDuration(ns float64) time.Duration {
	switch {
	case ns <= 0:
		return 0
	case ns >= float64(math.MaxInt64):
		return time.Duration(math.MaxInt64)
	default:
		return time.Duration(ns)
	}
}
