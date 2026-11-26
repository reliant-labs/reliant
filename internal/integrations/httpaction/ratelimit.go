package httpaction

import (
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"regexp"
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

// RateLimitReason names why a failed response was a rate limit. The zero value
// means it was not one.
type RateLimitReason string

const (
	// RateLimitTooManyRequests is an HTTP 429.
	RateLimitTooManyRequests RateLimitReason = "too_many_requests"
	// RateLimitPrimary is a 403 whose X-RateLimit-Remaining is 0: GitHub's
	// primary limit, spent until X-RateLimit-Reset.
	RateLimitPrimary RateLimitReason = "primary"
	// RateLimitSecondary is a 403 with Retry-After, or one whose message says
	// a rate limit was hit: GitHub's secondary (abuse) limit.
	RateLimitSecondary RateLimitReason = "secondary"
	// RateLimitTokenMint is a credential the token authority could not mint
	// because GitHub's rate limit on the token it checks with was spent
	// (ghdelegated). Nothing was sent to the action's provider.
	RateLimitTokenMint RateLimitReason = "token_mint"
)

// Rate-limit retry bounds. A rate-limited call is retried after the wait the
// provider asked for, clamped to [RateLimitMinDelay, RateLimitMaxDelay]; with
// no wait stated, after rateLimitDefaultDelay doubled per attempt.
const (
	// RateLimitMaxDelay caps one wait. A reset further away than this is
	// retried at the cap (and may be refused again), never made permanent:
	// the activity's retry budget is what bounds the whole wait.
	RateLimitMaxDelay = 15 * time.Minute
	// RateLimitMinDelay is the shortest wait, so a reset already past (clock
	// skew) or a Retry-After of 0 does not retry in a tight loop.
	RateLimitMinDelay = time.Second
	// rateLimitDefaultDelay is the first wait when the provider named none:
	// GitHub asks for at least a minute after a secondary limit that sent no
	// Retry-After, then exponentially longer.
	rateLimitDefaultDelay = time.Minute
	// rateLimitResetMargin is added to a wait derived from a reset epoch,
	// which has one-second resolution and is read off another clock.
	rateLimitResetMargin = time.Second
	// rateLimitJitterShare is the most jitter adds to a wait (a tenth of it,
	// and at least up to rateLimitMinJitter), so runs limited by the same
	// window do not all retry in the same second.
	rateLimitJitterShare = 10
	rateLimitMinJitter   = time.Second
)

// rateLimitMessage matches a provider's own words for a rate limit: GitHub's
// "You have exceeded a secondary rate limit" and "API rate limit exceeded".
var rateLimitMessage = regexp.MustCompile(`(?i)rate limit`)

// rateLimitReasonOf classifies a non-2xx response as a rate limit, or returns
// "". It reads only the status, the rate-limit headers and the body's
// message, never a credential.
func rateLimitReasonOf(p *page) RateLimitReason {
	if p == nil {
		return ""
	}
	switch p.status {
	case http.StatusTooManyRequests:
		return RateLimitTooManyRequests
	case http.StatusForbidden:
	default:
		return ""
	}
	if remaining := headerCount(p.headers, "X-RateLimit-Remaining", "RateLimit-Remaining"); remaining != nil && *remaining == 0 {
		return RateLimitPrimary
	}
	if _, ok := retryAfter(p.headers.Get("Retry-After"), time.Now()); ok {
		return RateLimitSecondary
	}
	if rateLimitMessage.MatchString(bodyMessage(p)) {
		return RateLimitSecondary
	}
	return ""
}

// bodyMessage is a JSON error body's top-level "message" (GitHub's shape),
// else the start of the raw body.
func bodyMessage(p *page) string {
	if obj, ok := p.parsed.(map[string]any); ok {
		msg, _ := obj["message"].(string)
		return msg
	}
	raw := p.raw
	if len(raw) > 512 {
		raw = raw[:512]
	}
	return string(raw)
}

// providerWait is how long rl asks the caller to wait, as of now: Retry-After
// when sent, else the time to the window's reset when the window is spent (or
// says nothing of what is left). known is false when it says neither.
func providerWait(rl *RateLimit, now time.Time) (wait time.Duration, known bool) {
	if rl == nil {
		return 0, false
	}
	if rl.RetryAfter != nil {
		return *rl.RetryAfter, true
	}
	// GitHub sends X-RateLimit-Reset on every response. Only a spent window
	// says to wait for it: a secondary limit with requests remaining does
	// not end at the primary window's reset.
	if rl.Reset != nil && (rl.Remaining == nil || *rl.Remaining == 0) {
		return max(rl.Reset.Sub(now), 0) + rateLimitResetMargin, true
	}
	return 0, false
}

// RateLimitDelay is the wait before retrying a rate-limited call: wait when
// known, else rateLimitDefaultDelay doubled for each attempt after the first;
// plus jitter (0 ≤ jitter < 1 picks a point in the jitter window); clamped to
// [RateLimitMinDelay, RateLimitMaxDelay].
func RateLimitDelay(wait time.Duration, known bool, attempt int32, jitter float64) time.Duration {
	d := wait
	if !known {
		d = rateLimitDefaultDelay << min(max(attempt, 1)-1, 10)
	}
	d = min(max(d, 0), RateLimitMaxDelay)
	window := max(d/rateLimitJitterShare, rateLimitMinJitter)
	d += time.Duration(min(max(jitter, 0), 1) * float64(window))
	return min(max(d, RateLimitMinDelay), RateLimitMaxDelay)
}

// RateLimitedErrorType is the Temporal failure type of a rate-limited call
// waiting for its retry.
const RateLimitedErrorType = "IntegrationRateLimited"

// RateLimitedError is a call the provider refused for a rate limit. It decides
// its own retry (the workflow runtime's selfSchedulingRetry): always retry,
// after RateLimitDelay. It carries only a status, a reason and waits, never a
// header value or a credential, so its text is safe to log and show.
//
// A rate-limited response is one the provider REJECTED, so retrying the call
// cannot repeat a write: nothing was written. The write ledger records the
// refused attempt as failed and not ambiguous (a 4xx), and the retry records
// its own intent.
type RateLimitedError struct {
	// Status is the HTTP status refused with; 0 for RateLimitTokenMint.
	Status int
	Reason RateLimitReason
	// Wait is what the provider asked for, when WaitKnown.
	Wait      time.Duration
	WaitKnown bool
	// Attempt is the 1-based activity attempt that was refused, which the
	// default backoff is keyed to.
	Attempt int32
	// jitter is drawn once, so every read of RetryAfter agrees.
	jitter float64
	// Err is the cause, when there is one (a token authority's refusal).
	Err error
}

// NewRateLimitedError is a RateLimitedError with its jitter drawn.
func NewRateLimitedError(status int, reason RateLimitReason, wait time.Duration, waitKnown bool, err error) *RateLimitedError {
	return &RateLimitedError{Status: status, Reason: reason, Wait: wait, WaitKnown: waitKnown, jitter: rand.Float64(), Err: err} //nolint:gosec // jitter, not a secret
}

// ForAttempt is a copy of e keyed to attempt.
func (e *RateLimitedError) ForAttempt(attempt int32) *RateLimitedError {
	c := *e
	c.Attempt = attempt
	return &c
}

func (e *RateLimitedError) Error() string {
	delay, _ := e.RetryAfter()
	what := "the provider's rate limit"
	if e.Status != 0 {
		what = fmt.Sprintf("the provider's rate limit (HTTP %d, %s)", e.Status, e.Reason)
	} else if e.Reason == RateLimitTokenMint {
		what = "GitHub's rate limit while minting a token"
	}
	return fmt.Sprintf("integration call refused by %s; retrying in %s", what, delay)
}

func (e *RateLimitedError) Unwrap() error { return e.Err }

// RetryAfter always retries, after RateLimitDelay.
func (e *RateLimitedError) RetryAfter() (time.Duration, bool) {
	return RateLimitDelay(e.Wait, e.WaitKnown, e.Attempt, e.jitter), true
}

// TemporalErrorType names this failure on the workflow side.
func (e *RateLimitedError) TemporalErrorType() string { return RateLimitedErrorType }

// RateLimitedError is res as a RateLimitedError, as of now, or nil when res
// is not a rate-limited failure.
func (res *Result) RateLimitedError(now time.Time) *RateLimitedError {
	if res == nil || !res.IsError || res.RateLimited == "" {
		return nil
	}
	wait, known := providerWait(res.RateLimit, now)
	return NewRateLimitedError(res.StatusCode, res.RateLimited, wait, known, nil)
}
