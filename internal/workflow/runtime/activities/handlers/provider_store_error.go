// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

const (
	providerStoreErrorType  = "ProviderStoreUnavailable"
	providerStoreFirstDelay = 10 * time.Second
	providerStoreMaxDelay   = time.Minute
)

// providerStoreReadBackoff is the pause before each in-activity re-read of the
// user's providers, so one blip costs neither an activity attempt nor a turn.
var providerStoreReadBackoff = []time.Duration{250 * time.Millisecond, time.Second}

// lookupAvailableDriversWithRetry reads the user's providers, re-reading a few
// times on a store failure. A persistent failure comes back as a retryable
// transientStoreError.
func lookupAvailableDriversWithRetry(ctx context.Context, userID string) (models.AvailableDrivers, error) {
	avail, err := lookupAvailableDrivers(ctx, userID)
	for _, wait := range providerStoreReadBackoff {
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return models.AvailableDrivers{}, ctx.Err()
		case <-time.After(wait):
		}
		avail, err = lookupAvailableDrivers(ctx, userID)
	}
	if err != nil {
		return models.AvailableDrivers{}, newTransientStoreError(ctx, err)
	}
	return avail, nil
}

// transientStoreError is a failure to READ the user's providers (the credential
// store was unreachable). It says nothing about what the user has connected, so
// it must retry, never pause the run with "connect a provider". It schedules its
// own retry (the runtime's selfSchedulingRetry), bounded by the step's retry
// ladder, which also keeps its text, whatever the store's error said, away from
// the terminal-pattern match.
type transientStoreError struct {
	cause   error
	attempt int32
}

func newTransientStoreError(ctx context.Context, cause error) *transientStoreError {
	attempt := int32(1)
	if activity.IsActivity(ctx) {
		attempt = activity.GetInfo(ctx).Attempt
	}
	return &transientStoreError{cause: cause, attempt: attempt}
}

func (e *transientStoreError) Error() string {
	return fmt.Sprintf("could not read your connected providers (temporary, retrying): %v", e.cause)
}

func (e *transientStoreError) Unwrap() error { return e.cause }

func (e *transientStoreError) RetryAfter() (time.Duration, bool) {
	return min(providerStoreFirstDelay<<(max(e.attempt, 1)-1), providerStoreMaxDelay), true
}

func (e *transientStoreError) TemporalErrorType() string { return providerStoreErrorType }
