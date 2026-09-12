// Copyright (c) 2025 Reliant Labs
package imagegen

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// generateWithRetry runs one generation attempt and repeats it while the error
// is a transient *APIError.
//
// Shared by both clients so the two wire formats cannot drift on retry policy.
// The clients differ only in what one attempt does; whether a 503 is worth
// repeating and how long to wait is the same question either way, and the
// answer is decided entirely by the normalized *APIError they both produce.
func generateWithRetry(
	ctx context.Context,
	modelID string,
	driver string,
	retryBaseDelay time.Duration,
	attempt func(context.Context) (*Response, error),
) (*Response, error) {
	if retryBaseDelay <= 0 {
		retryBaseDelay = defaultRetryBaseDelay
	}

	for attemptNumber := 1; ; attemptNumber++ {
		response, err := attempt(ctx)
		if err == nil {
			return response, nil
		}

		delay, retryable := retryDelay(attemptNumber, err, retryBaseDelay)
		if !retryable {
			return nil, err
		}

		logging.Warn("Retrying image generation request",
			"attempt", attemptNumber,
			"max_attempts", defaultMaxAttempts,
			"after_ms", delay.Milliseconds(),
			"model", modelID,
			"driver", driver,
		)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// retryDelay decides whether a failed attempt is worth repeating and how long
// to wait. Quota exhaustion is deliberately excluded: it arrives as a 429 but
// the budget is hard-capped, so retrying only delays the real error.
func retryDelay(attempt int, err error, baseDelay time.Duration) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || attempt >= defaultMaxAttempts {
		return 0, false
	}

	// An empty result is retryable, but on a TIGHTER ladder than a transport
	// failure. A 503 costs nothing to repeat — the provider never ran the
	// generation. An empty result means the provider DID generate and bill for
	// an image (~1120 output tokens observed) and then failed to return it, so
	// every retry is real money for an image nobody receives. One extra attempt
	// buys most of the recovery: the drop is intermittent, and a live
	// investigation saw the identical prompt succeed on 3 of 3 retries.
	if apiErr.EmptyResult {
		if attempt >= emptyResultMaxAttempts {
			return 0, false
		}
		return baseDelay, true
	}

	switch apiErr.StatusCode {
	case http.StatusTooManyRequests:
		if isQuotaExhausted(apiErr) {
			return 0, false
		}
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return 0, false
	}

	if apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter, true
	}
	return baseDelay * time.Duration(1<<(attempt-1)), true
}
