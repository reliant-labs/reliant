// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// THE STREAMING REQUEST RETRIES HERE, NOT IN THE SDK.
//
// The SDK retries a failed request twice by default and honors the provider's
// Retry-After with no ceiling, all inside NewStreaming, emitting nothing. On
// 2026-09-27 ten chats sat for minutes in that sleep after Anthropic answered
// 429 with a long Retry-After: no log line, no provider_backoff row, and a UI
// that showed every chat active and none moving. The reliant driver already
// publishes each wait as llm.EventRetryWait so CallLLM records it durably;
// this is the same ladder for the Anthropic drivers, with the SDK's own
// retries turned off for the streaming request so nothing waits invisibly.

const (
	// maxHonoredRetryAfter is the longest provider-requested wait the driver
	// sleeps out. A longer one (a subscription window measured in hours) is
	// surfaced as an error instead: parking an activity for that long looks
	// exactly like a hang, and the error says when to come back.
	maxHonoredRetryAfter = 5 * time.Minute
	// maxBackoff caps the driver's own exponential backoff when the provider
	// gives no Retry-After.
	maxBackoff = time.Minute
)

// streamRetryDecision reports whether a streaming request that failed before
// producing any event should be retried, and describes the wait if so.
// A non-nil error means give up with that error.
func streamRetryDecision(attempt int, err error) (llm.RetryWait, bool, error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return llm.RetryWait{}, false, err
	}
	if attempt > models.MaxRetries {
		return llm.RetryWait{}, false, err
	}

	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		// No HTTP response at all: a dial, TLS or reset error. The request
		// never reached the model, so trying again is safe.
		return llm.RetryWait{Attempt: attempt, MaxAttempts: models.MaxRetries, Delay: backoff(attempt), Reason: "connection_error"}, true, nil
	}
	if !retryableStatus(apiErr.StatusCode) {
		return llm.RetryWait{}, false, err
	}
	wait := llm.RetryWait{Attempt: attempt, MaxAttempts: models.MaxRetries, StatusCode: apiErr.StatusCode, Reason: "http_" + strconv.Itoa(apiErr.StatusCode)}
	if after, ok := retryAfter(apiErr.Response); ok {
		if after > maxHonoredRetryAfter {
			return llm.RetryWait{}, false, newRetryAfterTooLongError(apiErr.StatusCode, after, apiErr.Response, err)
		}
		wait.Delay = max(after, 0)
		return wait, true, nil
	}
	wait.Delay = backoff(attempt)
	return wait, true, nil
}

// RetryAfterTooLongError is a retryable provider error whose requested wait is
// longer than the driver will sleep inside one request — in practice a
// subscription usage window. Its message carries a
// chatmarkers.KindProviderUsageLimit marker, which is what makes the workflow
// fail the turn at once (no retries against a window measured in hours) and
// show this sentence in the chat instead of a generic "rate limited".
type RetryAfterTooLongError struct {
	StatusCode int
	RetryAfter time.Duration
	// ResetAt is when the provider says the limit lifts: its unified reset
	// header when sent, otherwise now + RetryAfter.
	ResetAt time.Time
	// Window is the limit that was hit ("seven_day", "five_hour"), from
	// anthropic-ratelimit-unified-representative-claim; "" when not sent.
	Window string
	// OverageReason is why paid overage could not absorb the request
	// ("out_of_credits"), from anthropic-ratelimit-unified-overage-disabled-reason.
	OverageReason string
	Err           error
}

func newRetryAfterTooLongError(status int, after time.Duration, res *http.Response, err error) *RetryAfterTooLongError {
	e := &RetryAfterTooLongError{StatusCode: status, RetryAfter: after, ResetAt: time.Now().Add(after).UTC(), Err: err}
	if res == nil {
		return e
	}
	if v := res.Header.Get("anthropic-ratelimit-unified-reset"); v != "" {
		if unix, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			e.ResetAt = time.Unix(unix, 0).UTC()
		}
	}
	e.Window = res.Header.Get("anthropic-ratelimit-unified-representative-claim")
	e.OverageReason = res.Header.Get("anthropic-ratelimit-unified-overage-disabled-reason")
	return e
}

func (e *RetryAfterTooLongError) Error() string {
	detail := "Anthropic"
	switch e.Window {
	case "":
	case "seven_day":
		detail += ", 7-day window"
	case "five_hour":
		detail += ", 5-hour window"
	default:
		detail += ", " + strings.ReplaceAll(e.Window, "_", " ") + " window"
	}
	if e.OverageReason != "" {
		detail += ", overage unavailable: " + strings.ReplaceAll(e.OverageReason, "_", " ")
	}
	msg := fmt.Sprintf("%s (%s) — resets %s (in %s). Switch to another provider, or send a message after it resets",
		chatmarkers.ProviderUsageLimitLead, detail,
		e.ResetAt.Format("Mon Jan 2 15:04 UTC"), time.Until(e.ResetAt).Round(time.Minute))
	return chatmarkers.Wrap(chatmarkers.KindProviderUsageLimit, e.ResetAt.Format(time.RFC3339), msg) + ": " + e.Err.Error()
}

func (e *RetryAfterTooLongError) Unwrap() error { return e.Err }

// retryableStatus is the SDK's own retry set: timeout, conflict, rate limit,
// and server errors (529 is Anthropic's "overloaded").
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return code >= http.StatusInternalServerError
}

// retryAfter reads the provider's requested wait: retry-after-ms, then
// Retry-After as seconds or an HTTP date.
func retryAfter(res *http.Response) (time.Duration, bool) {
	if res == nil {
		return 0, false
	}
	if v := res.Header.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	if v := res.Header.Get("Retry-After"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil {
			return time.Duration(s * float64(time.Second)), true
		}
		if t, err := http.ParseTime(v); err == nil {
			return time.Until(t), true
		}
	}
	return 0, false
}

// backoff is 2s doubling per attempt, capped at maxBackoff, with up to 20%
// jitter so parked chats do not retry in lockstep.
func backoff(attempt int) time.Duration {
	d := maxBackoff
	if attempt < 6 {
		d = min(maxBackoff, 2*time.Second<<(attempt-1))
	}
	return d + time.Duration(rand.Int64N(int64(d/5)+1))
}
