package httpaction

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimit is what a response's headers said about the caller's rate limit.
// It is read from the headers providers commonly send, not from any one
// provider's: X-RateLimit-Limit / -Remaining / -Used / -Reset / -Resource
// (GitHub, and most REST APIs), the IETF RateLimit-Limit / -Remaining /
// -Reset, and Retry-After. A field the response did not carry is nil ("").
type RateLimit struct {
	Limit     *int64
	Remaining *int64
	Used      *int64
	// Reset is when the window resets.
	Reset *time.Time
	// Resource names the budget the call counted against (GitHub's "core",
	// "graphql", "search"); only a short token is kept.
	Resource string
	// RetryAfter is how long the provider asked the caller to wait.
	RetryAfter *time.Duration
}

// epochFloor tells an absolute reset (unix seconds, as GitHub sends) from a
// relative one (seconds from now, as the IETF fields and some APIs send): no
// window is a billion seconds (31 years) long.
const epochFloor = 1_000_000_000

// rateLimitOf reads h's rate-limit headers, as of now. nil when there are none.
func rateLimitOf(h http.Header, now time.Time) *RateLimit {
	if h == nil {
		return nil
	}
	rl := &RateLimit{
		Limit:     headerCount(h, "X-RateLimit-Limit", "RateLimit-Limit"),
		Remaining: headerCount(h, "X-RateLimit-Remaining", "RateLimit-Remaining"),
		Used:      headerCount(h, "X-RateLimit-Used", "RateLimit-Used"),
		Resource:  rateLimitResource(h.Get("X-RateLimit-Resource")),
	}
	if v := headerCount(h, "X-RateLimit-Reset", "RateLimit-Reset"); v != nil {
		reset := now.Add(time.Duration(*v) * time.Second)
		if *v >= epochFloor {
			reset = time.Unix(*v, 0)
		}
		rl.Reset = &reset
	}
	if wait, ok := retryAfter(h.Get("Retry-After"), now); ok {
		rl.RetryAfter = &wait
	}
	if rl.Limit == nil && rl.Remaining == nil && rl.Used == nil && rl.Reset == nil && rl.Resource == "" && rl.RetryAfter == nil {
		return nil
	}
	return rl
}

// headerCount is the first of names h carries that holds a count: a
// non-negative integer, or the first item of a list ("100, 100;w=60", as an
// IETF draft spells a policy).
func headerCount(h http.Header, names ...string) *int64 {
	for _, name := range names {
		raw := h.Get(name)
		if raw == "" {
			continue
		}
		if i := strings.IndexAny(raw, ",;"); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			f, ferr := strconv.ParseFloat(raw, 64)
			if ferr != nil || math.IsNaN(f) || f > 1<<62 {
				continue
			}
			n = int64(f)
		}
		if n < 0 {
			continue
		}
		return &n
	}
	return nil
}

// rateLimitResource keeps a resource name only when it is a short token, so a
// header can never put free text on a span.
func rateLimitResource(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 {
		return ""
	}
	for _, c := range raw {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '.' {
			return ""
		}
	}
	return raw
}

// retryAfter reads a Retry-After header: delay-seconds or an HTTP date. A
// date already past is a wait of zero.
func retryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	at, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	if wait := at.Sub(now); wait > 0 {
		return wait.Truncate(time.Second), true
	}
	return 0, true
}
