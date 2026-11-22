package httpaction

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }

func TestRateLimitOf(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		headers map[string]string
		want    *RateLimit
	}{
		"no headers": {headers: map[string]string{"Content-Type": "application/json"}},
		"GitHub's X-RateLimit-* (epoch reset, resource)": {
			headers: map[string]string{
				"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "4990", "X-RateLimit-Used": "10",
				"X-RateLimit-Reset": "1800003600", "X-RateLimit-Resource": "core",
			},
			want: &RateLimit{Limit: i64(5000), Remaining: i64(4990), Used: i64(10), Reset: tp(time.Unix(1_800_003_600, 0)), Resource: "core"},
		},
		"the IETF RateLimit-* fields (delta reset)": {
			headers: map[string]string{"RateLimit-Limit": "100", "RateLimit-Remaining": "0", "RateLimit-Reset": "30"},
			want:    &RateLimit{Limit: i64(100), Remaining: i64(0), Reset: tp(now.Add(30 * time.Second))},
		},
		"a policy list keeps its first count": {
			headers: map[string]string{"RateLimit-Limit": "100, 100;w=60", "RateLimit-Remaining": "42;w=60"},
			want:    &RateLimit{Limit: i64(100), Remaining: i64(42)},
		},
		"X-RateLimit-* wins over RateLimit-*": {
			headers: map[string]string{"X-RateLimit-Limit": "5000", "RateLimit-Limit": "60"},
			want:    &RateLimit{Limit: i64(5000)},
		},
		"Retry-After in seconds": {
			headers: map[string]string{"Retry-After": "120"},
			want:    &RateLimit{RetryAfter: dp(120 * time.Second)},
		},
		"Retry-After as a date": {
			headers: map[string]string{"Retry-After": now.Add(90 * time.Second).UTC().Format(http.TimeFormat)},
			want:    &RateLimit{RetryAfter: dp(90 * time.Second)},
		},
		"Retry-After as a date already past is a wait of zero": {
			headers: map[string]string{"Retry-After": now.Add(-time.Hour).UTC().Format(http.TimeFormat)},
			want:    &RateLimit{RetryAfter: dp(0)},
		},
		"a float count is truncated": {
			headers: map[string]string{"X-RateLimit-Remaining": "12.7"},
			want:    &RateLimit{Remaining: i64(12)},
		},
		"garbage, negatives and free text are dropped": {
			headers: map[string]string{
				"X-RateLimit-Limit": "lots", "X-RateLimit-Remaining": "-1", "X-RateLimit-Reset": "NaN",
				"X-RateLimit-Resource": "core; see https://example.com/docs", "Retry-After": "soon",
			},
		},
		"an over-long resource is dropped, the counts kept": {
			headers: map[string]string{"X-RateLimit-Used": "3", "X-RateLimit-Resource": "a234567890123456789012345678901234567890123456789012345678901234567890"},
			want:    &RateLimit{Used: i64(3)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			got := rateLimitOf(h, now)
			if !sameRateLimit(got, tc.want) {
				t.Fatalf("rateLimitOf = %s, want %s", describeRateLimit(got), describeRateLimit(tc.want))
			}
		})
	}
	if rateLimitOf(nil, now) != nil {
		t.Error("nil headers must read as no rate limit")
	}
}

func tp(v time.Time) *time.Time         { return &v }
func dp(v time.Duration) *time.Duration { return &v }

func sameRateLimit(a, b *RateLimit) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	eqI := func(x, y *int64) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
	eqT := func(x, y *time.Time) bool { return (x == nil && y == nil) || (x != nil && y != nil && x.Equal(*y)) }
	eqD := func(x, y *time.Duration) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
	return eqI(a.Limit, b.Limit) && eqI(a.Remaining, b.Remaining) && eqI(a.Used, b.Used) &&
		eqT(a.Reset, b.Reset) && eqD(a.RetryAfter, b.RetryAfter) && a.Resource == b.Resource
}

func describeRateLimit(rl *RateLimit) string {
	if rl == nil {
		return "<nil>"
	}
	s := "{"
	if rl.Limit != nil {
		s += fmt.Sprintf(" limit=%d", *rl.Limit)
	}
	if rl.Remaining != nil {
		s += fmt.Sprintf(" remaining=%d", *rl.Remaining)
	}
	if rl.Used != nil {
		s += fmt.Sprintf(" used=%d", *rl.Used)
	}
	if rl.Reset != nil {
		s += fmt.Sprintf(" reset=%d", rl.Reset.Unix())
	}
	if rl.RetryAfter != nil {
		s += fmt.Sprintf(" retry_after=%s", *rl.RetryAfter)
	}
	if rl.Resource != "" {
		s += " resource=" + rl.Resource
	}
	return s + " }"
}
