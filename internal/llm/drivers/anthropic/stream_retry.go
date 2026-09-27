// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
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
			return llm.RetryWait{}, false, &RetryAfterTooLongError{StatusCode: apiErr.StatusCode, RetryAfter: after, Err: err}
		}
		wait.Delay = max(after, 0)
		return wait, true, nil
	}
	wait.Delay = backoff(attempt)
	return wait, true, nil
}

// RetryAfterTooLongError is a retryable provider error whose requested wait is
// longer than the driver will sleep inside one request.
type RetryAfterTooLongError struct {
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

func (e *RetryAfterTooLongError) Error() string {
	return "provider asked to retry after " + e.RetryAfter.Round(time.Second).String() +
		" (HTTP " + strconv.Itoa(e.StatusCode) + ") — longer than the driver waits in-request: " + e.Err.Error()
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
