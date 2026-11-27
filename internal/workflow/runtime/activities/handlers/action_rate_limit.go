// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// An action the provider refused for a rate limit (a 429, a GitHub 403 whose
// quota is spent or that hit the secondary limit, or a GitHub token the
// control plane could not mint for the same reason) waits for the limit to
// lift before its retry, instead of answering the graph at once.
//
// It fails the attempt with a httpaction.RateLimitedError, which schedules
// its own retry (the runtime's selfSchedulingRetry): Temporal retries the step
// after the provider's Retry-After or reset, capped at
// httpaction.RateLimitMaxDelay. The step's retry ladder still bounds the
// whole wait: on its LAST attempt the action answers exactly as it did before
// this existed (is_error, retryable, the provider's status), so the graph
// decides, as it always did, and the run never pauses for a rate limit.
//
// Retrying cannot repeat a write. A rate-limited response is one the provider
// rejected, so nothing was written; a refused mint never reached the provider
// at all. The write ledger settles the refused attempt as failed and not
// ambiguous, and the retry records its own intent before it sends.

// rateLimitClock is when a rate-limit wait is measured from.
var rateLimitClock = time.Now

// rateLimitRetry is the error that makes Temporal retry a rate-limited call
// after its wait, or nil when the call answers the graph now: it was not rate
// limited, it is not running as an activity, or this is the step's last
// attempt.
func rateLimitRetry(ctx context.Context, res *httpaction.Result, runErr error) *httpaction.RateLimitedError {
	var limited *httpaction.RateLimitedError
	switch {
	case runErr != nil:
		if !errors.As(runErr, &limited) {
			return nil
		}
	default:
		if limited = res.RateLimitedError(rateLimitClock()); limited == nil {
			return nil
		}
	}
	if !activity.IsActivity(ctx) {
		return nil
	}
	info := activity.GetInfo(ctx)
	attempt := max(info.Attempt, 1)
	if attempt >= stepMaxAttempts(info) {
		return nil
	}
	return limited.ForAttempt(attempt)
}

// stepMaxAttempts is the step's retry ladder: Temporal's word when it sends
// the policy, else the ladder every graph step is dispatched with.
func stepMaxAttempts(info activity.Info) int32 {
	if info.RetryPolicy != nil && info.RetryPolicy.MaximumAttempts > 0 {
		return info.RetryPolicy.MaximumAttempts
	}
	return activitytypes.StepActivityMaxAttempts
}
