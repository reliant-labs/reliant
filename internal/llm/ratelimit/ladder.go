// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// THE REQUEST RETRIES IN THE DRIVER, NOT IN THE SDK.
//
// An SDK retries a failed request twice by default and honors the provider's
// Retry-After with no ceiling, all inside the call, emitting nothing. On
// 2026-09-27 ten chats sat for minutes in that sleep after Anthropic answered
// 429 with a long Retry-After: no log line, no provider_backoff row, and a UI
// that showed every chat active and none moving. A driver that uses Retryer
// turns its SDK's own retries off and publishes each wait as
// llm.EventRetryWait BEFORE sleeping, which is what CallLLM records durably.

const (
	// DefaultMaxHonoredRetryAfter is the longest provider-requested wait a
	// driver sleeps out. A longer one (a subscription window measured in hours)
	// becomes a UsageLimitError instead: parking an activity for that long looks
	// exactly like a hang, and the error says when to come back.
	DefaultMaxHonoredRetryAfter = 5 * time.Minute
	// DefaultMaxBackoff caps the ladder's own exponential backoff when the
	// provider gives no Retry-After.
	DefaultMaxBackoff  = time.Minute
	defaultBaseBackoff = 2 * time.Second
)

// Retryer is one provider's retry ladder: 2s doubling to MaxBackoff with
// jitter, Retry-After honored up to MaxHonored, and a usage window surfaced as
// a UsageLimitError the moment it is recognized. The zero value is usable
// apart from Provider.
type Retryer struct {
	// Provider is the display name used in UsageLimitError ("Anthropic").
	Provider string
	// Log labels this ladder's log lines ("Anthropic stream request").
	Log string
	// MaxAttempts is the ladder's ceiling; 0 means models.MaxRetries.
	MaxAttempts int
	// MaxHonored is the longest Retry-After slept out; 0 means DefaultMaxHonoredRetryAfter.
	MaxHonored time.Duration
	// MaxBackoff caps own backoff; 0 means DefaultMaxBackoff.
	MaxBackoff time.Duration

	// Now, Jitter and Sleep are test seams. Nil means time.Now, up to 20%
	// random jitter, and a context-aware timer.
	Now    func() time.Time
	Jitter func(d time.Duration) time.Duration
	Sleep  func(ctx context.Context, d time.Duration) error
}

// Failure is one failed attempt as a driver describes it to the ladder.
type Failure struct {
	Err error
	// Status is the HTTP status; 0 means no response at all (dial / TLS error).
	Status int
	Header http.Header
	// Limit holds the usage-window facts read off the response (reset, window,
	// overage reason), whether or not the window is known to be exhausted.
	Limit LimitInfo
	// UsageLimit is true when the response EXPLICITLY says a usage window is
	// exhausted (Anthropic unified-status rejected, Codex usage_limit_reached).
	// It ends the ladder at once; the Retry-After length heuristic is only the
	// fallback for providers that say nothing explicit.
	UsageLimit bool
	// Fatal marks a failure retrying cannot fix although its status looks
	// retryable (OpenAI insufficient_quota).
	Fatal bool
}

func (r Retryer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Retryer) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return models.MaxRetries
}

// Decide reports whether failure f of the given 1-based attempt should be
// retried, and describes the wait if so. A non-nil error means give up with
// that error.
func (r Retryer) Decide(attempt int, f Failure) (llm.RetryWait, bool, error) {
	err := f.Err
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || f.Fatal {
		return llm.RetryWait{}, false, err
	}
	after, hasAfter := RetryAfter(f.Header, r.now())
	if f.Status > 0 && f.UsageLimit {
		return llm.RetryWait{}, false, r.usageLimit(f, after, hasAfter)
	}
	if attempt > r.maxAttempts() {
		return llm.RetryWait{}, false, err
	}
	if f.Status == 0 {
		// No HTTP response: a dial, TLS or reset error. The request never
		// reached the model, so trying again is safe.
		return llm.RetryWait{Attempt: attempt, MaxAttempts: r.maxAttempts(), Delay: r.backoff(attempt), Reason: "connection_error"}, true, nil
	}
	if !RetryableStatus(f.Status) {
		return llm.RetryWait{}, false, err
	}
	wait := llm.RetryWait{Attempt: attempt, MaxAttempts: r.maxAttempts(), StatusCode: f.Status, Reason: "http_" + strconv.Itoa(f.Status)}
	if hasAfter {
		maxHonored := r.MaxHonored
		if maxHonored <= 0 {
			maxHonored = DefaultMaxHonoredRetryAfter
		}
		if after > maxHonored {
			return llm.RetryWait{}, false, r.usageLimit(f, after, hasAfter)
		}
		wait.Delay = max(after, 0)
		return wait, true, nil
	}
	wait.Delay = r.backoff(attempt)
	return wait, true, nil
}

func (r Retryer) usageLimit(f Failure, after time.Duration, hasAfter bool) error {
	e := &UsageLimitError{
		Provider: r.Provider, StatusCode: f.Status, Err: f.Err,
		ResetAt: f.Limit.ResetAt, Window: f.Limit.Window, OverageReason: f.Limit.OverageReason,
	}
	if hasAfter {
		e.RetryAfter = after
		if e.ResetAt.IsZero() {
			e.ResetAt = r.now().Add(after).UTC()
		}
	}
	return e
}

// Run drives try until it succeeds or the ladder gives up. try makes attempt n
// (1-based) and returns (_, true) on success or the Failure to classify. emit,
// when non-nil, receives each wait immediately BEFORE it is slept — the
// driver's channel send of llm.EventRetryWait; an error from it (the consumer
// went away) ends the run. Run returns nil on success, otherwise the final
// error (a *UsageLimitError, or the failure's own).
func (r Retryer) Run(ctx context.Context, try func(attempt int) (Failure, bool), emit func(llm.RetryWait) error) error {
	for attempt := 1; ; attempt++ {
		f, ok := try(attempt)
		if ok {
			return nil
		}
		wait, retry, err := r.Decide(attempt, f)
		if !retry {
			return err
		}
		logging.WarnContext(ctx, r.Log+" failed; retrying", "attempt", wait.Attempt, "delay", wait.Delay,
			"status", wait.StatusCode, "reason", wait.Reason, "error", f.Err)
		if emit != nil {
			if err := emit(wait); err != nil {
				return err
			}
		}
		if err := r.sleep(ctx, wait.Delay); err != nil {
			return err
		}
	}
}

func (r Retryer) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoff is 2s doubling per attempt, capped at MaxBackoff, plus up to 20%
// jitter so parked chats do not retry in lockstep.
func (r Retryer) backoff(attempt int) time.Duration {
	maxB := r.MaxBackoff
	if maxB <= 0 {
		maxB = DefaultMaxBackoff
	}
	d := maxB
	if attempt < 6 {
		d = min(maxB, defaultBaseBackoff<<(attempt-1))
	}
	if r.Jitter != nil {
		return d + r.Jitter(d)
	}
	return d + time.Duration(rand.Int64N(int64(d/5)+1))
}

// RetryableStatus is the retry set shared by every ladder: timeout, conflict,
// rate limit, and server errors (529 is Anthropic's "overloaded").
func RetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return code >= http.StatusInternalServerError
}

// RetryAfter reads the provider's requested wait: retry-after-ms, then
// Retry-After as seconds or an HTTP date.
func RetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if h == nil {
		return 0, false
	}
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(s * float64(time.Second)), true
		}
		if t, err := http.ParseTime(v); err == nil {
			return t.Sub(now), true
		}
	}
	return 0, false
}
