// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"
)

// A response the provider reported as malformed model output
// (drivererrors.ErrMalformedModelOutput: Gemini's MALFORMED_FUNCTION_CALL and
// kin) gets ONE fresh request. Sampling again usually produces a well-formed
// call, which is why opencode retries these; a model that produces the same
// malformed call twice is unlikely to stop on the third, and the step's
// blanket policy (five attempts) would spend all five on it.
const (
	malformedOutputMaxAttempts = 2
	malformedOutputBackoff     = time.Second
)

// malformedOutputErrorType is the Temporal failure type of a turn that ended
// on malformed model output.
const malformedOutputErrorType = "ProviderMalformedOutput"

// malformedOutputError is CallLLM's failure for a stream that ended with
// drivererrors.ErrMalformedModelOutput. It schedules its own retry (the
// runtime's selfSchedulingRetry): one more attempt, then the turn fails with
// this error, which names the model and carries the provider's own account
// of the call it could not parse.
type malformedOutputError struct {
	cause    error
	provider string
	model    string
	// attempt is this activity attempt, 1-based.
	attempt int32
}

func newMalformedOutputError(ctx context.Context, cause error, provider, model string) *malformedOutputError {
	return &malformedOutputError{cause: cause, provider: provider, model: model, attempt: activityAttempt(ctx)}
}

func (e *malformedOutputError) Error() string {
	return fmt.Sprintf("%s on %s returned a malformed response: %v", e.model, e.provider, e.cause)
}

// Unwrap keeps errors.Is(err, drivererrors.ErrMalformedModelOutput) true for
// the turn's failure.
func (e *malformedOutputError) Unwrap() error {
	return e.cause
}

// RetryAfter allows one retry: the first attempt that fails this way is
// retried shortly, any later one ends the turn. Keyed on the activity
// attempt, so a turn whose first attempt failed for another reason gets no
// retry of its own; that keeps the bound simple and strict.
func (e *malformedOutputError) RetryAfter() (time.Duration, bool) {
	if max(e.attempt, 1) >= malformedOutputMaxAttempts {
		return 0, false
	}
	return malformedOutputBackoff, true
}

// TemporalErrorType names this failure on the workflow side.
func (e *malformedOutputError) TemporalErrorType() string {
	return malformedOutputErrorType
}
