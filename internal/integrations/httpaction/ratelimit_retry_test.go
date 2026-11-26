package httpaction

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

// The wait before a rate-limited call's retry: the provider's when it said,
// else a minute doubled per attempt; never longer than the cap or shorter
// than the floor; jitter only ever adds, within a tenth (at least a second).
func TestRateLimitDelay(t *testing.T) {
	for name, tc := range map[string]struct {
		wait     time.Duration
		known    bool
		attempt  int32
		jitter   float64
		min, max time.Duration
	}{
		"the provider's wait":                     {wait: 90 * time.Second, known: true, attempt: 3, min: 90 * time.Second, max: 90 * time.Second},
		"jitter adds at most a tenth":             {wait: 100 * time.Second, known: true, jitter: 0.999, min: 100 * time.Second, max: 110 * time.Second},
		"jitter on a short wait is up to 1s":      {wait: 2 * time.Second, known: true, jitter: 0.5, min: 2500 * time.Millisecond, max: 2500 * time.Millisecond},
		"unknown wait, first attempt: a minute":   {attempt: 1, min: time.Minute, max: time.Minute},
		"unknown wait doubles per attempt":        {attempt: 3, min: 4 * time.Minute, max: 4 * time.Minute},
		"attempt 0 counts as the first":           {attempt: 0, min: time.Minute, max: time.Minute},
		"unknown wait is capped":                  {attempt: 9, jitter: 0.9, min: RateLimitMaxDelay, max: RateLimitMaxDelay},
		"a reset hours away retries at the cap":   {wait: 3 * time.Hour, known: true, jitter: 0.5, min: RateLimitMaxDelay, max: RateLimitMaxDelay},
		"a zero wait is floored, never immediate": {wait: 0, known: true, min: RateLimitMinDelay, max: RateLimitMinDelay},
		"a negative wait is floored":              {wait: -time.Minute, known: true, min: RateLimitMinDelay, max: RateLimitMinDelay},
		"out-of-range jitter is clamped":          {wait: time.Minute, known: true, jitter: 7, min: time.Minute, max: 66 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			got := RateLimitDelay(tc.wait, tc.known, tc.attempt, tc.jitter)
			if got < tc.min || got > tc.max {
				t.Fatalf("RateLimitDelay(%s, %v, %d, %v) = %s, want within [%s, %s]", tc.wait, tc.known, tc.attempt, tc.jitter, got, tc.min, tc.max)
			}
		})
	}
}

// What the provider asked to wait: Retry-After (seconds or a date) first,
// else the reset of a spent window, else nothing.
func TestProviderWait(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		headers map[string]string
		wait    time.Duration
		known   bool
	}{
		"missing headers":         {headers: map[string]string{}},
		"Retry-After in seconds":  {headers: map[string]string{"Retry-After": "120"}, wait: 120 * time.Second, known: true},
		"Retry-After as a date":   {headers: map[string]string{"Retry-After": now.Add(45 * time.Second).UTC().Format(http.TimeFormat)}, wait: 45 * time.Second, known: true},
		"Retry-After beats reset": {headers: map[string]string{"Retry-After": "5", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1800000600"}, wait: 5 * time.Second, known: true},
		"a spent window waits for its reset epoch": {
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1800000600"},
			wait:    600*time.Second + rateLimitResetMargin, known: true,
		},
		"a reset already past waits only the margin": {
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1799999000"},
			wait:    rateLimitResetMargin, known: true,
		},
		"a relative reset with nothing said of what is left": {
			headers: map[string]string{"RateLimit-Reset": "30"}, wait: 30*time.Second + rateLimitResetMargin, known: true,
		},
		"a window with requests left says nothing (a secondary limit)": {
			headers: map[string]string{"X-RateLimit-Remaining": "4000", "X-RateLimit-Reset": "1800000600"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			wait, known := providerWait(rateLimitOf(h, now), now)
			if wait != tc.wait || known != tc.known {
				t.Fatalf("providerWait = (%s, %v), want (%s, %v)", wait, known, tc.wait, tc.known)
			}
		})
	}
}

// Which responses are a rate limit: a 429; a 403 whose quota is spent; a 403
// with Retry-After or a rate-limit message (GitHub's secondary limit). Any
// other 403 is a permission refusal and stays one.
func TestRateLimitReasonOf(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		headers map[string]string
		body    string
		want    RateLimitReason
	}{
		"429":                        {status: 429, want: RateLimitTooManyRequests},
		"primary 403, quota spent":   {status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1800000600"}, body: `{"message":"API rate limit exceeded for user ID 1."}`, want: RateLimitPrimary},
		"secondary 403, Retry-After": {status: 403, headers: map[string]string{"Retry-After": "60", "X-RateLimit-Remaining": "4000"}, want: RateLimitSecondary},
		"secondary 403, by its message": {
			status: 403, headers: map[string]string{"X-RateLimit-Remaining": "4000"},
			body: `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`, want: RateLimitSecondary,
		},
		"a permission 403 is not a rate limit": {
			status: 403, headers: map[string]string{"X-RateLimit-Remaining": "4000", "X-RateLimit-Reset": "1800000600"},
			body: `{"message":"Resource not accessible by integration"}`,
		},
		"a 403 with no headers or body":     {status: 403},
		"a 404 is never one":                {status: 404, headers: map[string]string{"X-RateLimit-Remaining": "0"}},
		"a 503 with Retry-After is not one": {status: 503, headers: map[string]string{"Retry-After": "30"}},
		"only the message field counts in JSON": {
			status: 403, body: `{"message":"forbidden","documentation_url":"https://docs.github.com/rest/rate-limit"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			p := &page{status: tc.status, headers: h, raw: []byte(tc.body)}
			if tc.body != "" {
				if err := json.Unmarshal([]byte(tc.body), &p.parsed); err != nil {
					t.Fatal(err)
				}
			}
			if got := rateLimitReasonOf(p); got != tc.want {
				t.Fatalf("rateLimitReasonOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// The error a step retries with says why and how long, and nothing of the
// response's headers: a header can carry anything.
func TestRateLimitedErrorCarriesOnlyReasonsAndDelays(t *testing.T) {
	e := NewRateLimitedError(403, RateLimitPrimary, 2*time.Minute, true, nil).ForAttempt(2)
	delay, retry := e.RetryAfter()
	if !retry {
		t.Fatal("a rate limit always retries; the step's retry budget bounds it")
	}
	again, _ := e.RetryAfter()
	if delay != again {
		t.Fatalf("RetryAfter must agree with itself: %s then %s", delay, again)
	}
	if delay < 2*time.Minute || delay > 2*time.Minute+12*time.Second {
		t.Fatalf("delay = %s, want the provider's 2m plus at most a tenth", delay)
	}
	msg := e.Error()
	for _, want := range []string{"HTTP 403", "primary", "retrying in"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}
	if e.TemporalErrorType() != RateLimitedErrorType {
		t.Errorf("TemporalErrorType = %q", e.TemporalErrorType())
	}
	cause := errors.New("control-plane said so")
	if !errors.Is(NewRateLimitedError(0, RateLimitTokenMint, 0, false, cause), cause) {
		t.Error("the cause must stay reachable, so its class still holds")
	}
}

// End to end through the runner: GitHub's rules (a rate-limit 403 retryable,
// any other 403 permanent) and the defaults (a 429) mark a rate limit; a
// manifest that calls a 429 permanent, or the defaults' permanent 403, keeps
// the last word; and a permission 403 is exactly what it was.
func TestRunnerMarksRateLimitedFailures(t *testing.T) {
	type reply struct {
		status  int
		headers map[string]string
		body    string
	}
	var current atomic.Pointer[reply]
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r := current.Load()
		for k, v := range r.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(r.status)
		_, _ = w.Write([]byte(r.body))
	}))
	githubRules := `      errors:
        - status: 403
          when: >-
            headers[?'x-ratelimit-remaining'].orValue('') == '0' ||
            headers[?'retry-after'].hasValue() ||
            (type(response) == map && type(response.?message.orValue(null)) == string &&
             response.message.matches('(?i)rate limit'))
          retryable: true
          message: "rate limited"
        - { status: 429, retryable: true, message: "rate limited" }
        - { status: 403, retryable: false, message: "forbidden" }
`
	run := func(t *testing.T, rules string, r reply) *Result {
		t.Helper()
		current.Store(&r)
		m, a := mustParse(t, manifestFor(srv.URL, "    params: { type: object }\n    request:\n      method: POST\n      path: /x\n"+rules))
		runner := newRunner()
		runner.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
		res, err := runner.Run(context.Background(), m, a, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	now := time.Now()
	reset := now.Add(10 * time.Minute).Unix()
	primary := reply{status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset, 10)}, body: `{"message":"API rate limit exceeded"}`}
	secondary := reply{status: 403, headers: map[string]string{"Retry-After": "60", "X-RateLimit-Remaining": "10"}, body: `{"message":"You have exceeded a secondary rate limit."}`}
	permission := reply{status: 403, headers: map[string]string{"X-RateLimit-Remaining": "10"}, body: `{"message":"Resource not accessible by integration"}`}

	for name, tc := range map[string]struct {
		rules     string
		reply     reply
		retryable bool
		want      RateLimitReason
		minWait   time.Duration
		maxWait   time.Duration
	}{
		"GitHub rules, primary 403":    {rules: githubRules, reply: primary, retryable: true, want: RateLimitPrimary, minWait: 9 * time.Minute, maxWait: 11*time.Minute + 6*time.Second},
		"GitHub rules, secondary 403":  {rules: githubRules, reply: secondary, retryable: true, want: RateLimitSecondary, minWait: time.Minute, maxWait: 66 * time.Second},
		"GitHub rules, 429":            {rules: githubRules, reply: reply{status: 429, headers: map[string]string{"Retry-After": "30"}}, retryable: true, want: RateLimitTooManyRequests, minWait: 30 * time.Second, maxWait: 33 * time.Second},
		"GitHub rules, permission 403": {rules: githubRules, reply: permission},
		"defaults, 429 with no wait":   {reply: reply{status: 429}, retryable: true, want: RateLimitTooManyRequests, minWait: time.Minute, maxWait: 66 * time.Second},
		// The defaults call a 403 permanent, and that verdict stands: only a
		// failure already judged retryable is waited out.
		"defaults, primary 403 stays permanent": {reply: primary},
		"defaults, permission 403":              {reply: permission},
		"a manifest's permanent 429 wins":       {rules: "      errors:\n        - { status: 429, retryable: false, message: quota }\n", reply: reply{status: 429, headers: map[string]string{"Retry-After": "30"}}},
		"a 503 keeps the defaults":              {reply: reply{status: 503, headers: map[string]string{"Retry-After": "30"}}, retryable: true},
	} {
		t.Run(name, func(t *testing.T) {
			res := run(t, tc.rules, tc.reply)
			if !res.IsError || res.Retryable != tc.retryable || res.RateLimited != tc.want || res.StatusCode != tc.reply.status {
				t.Fatalf("got is_error=%v retryable=%v rate_limited=%q status=%d, want retryable=%v rate_limited=%q",
					res.IsError, res.Retryable, res.RateLimited, res.StatusCode, tc.retryable, tc.want)
			}
			limited := res.RateLimitedError(time.Now())
			if tc.want == "" {
				if limited != nil {
					t.Fatalf("not a rate limit, but RateLimitedError = %v", limited)
				}
				return
			}
			if limited == nil {
				t.Fatal("a rate limit must yield a RateLimitedError")
			}
			delay, retry := limited.ForAttempt(1).RetryAfter()
			if !retry || delay < tc.minWait || delay > tc.maxWait {
				t.Fatalf("RetryAfter = (%s, %v), want a retry within [%s, %s]", delay, retry, tc.minWait, tc.maxWait)
			}
			for _, v := range tc.reply.headers {
				if strings.Contains(limited.Error(), v) && len(v) > 2 {
					t.Errorf("Error() %q carries the header value %q", limited.Error(), v)
				}
			}
		})
	}
}
