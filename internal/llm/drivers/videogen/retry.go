// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"net/http"
	"time"
)

const (
	defaultSubmitAttempts   = 3
	defaultSubmitRetryDelay = 2 * time.Second
)

// submitWithRetry repeats a Submit while it fails with a transient status.
//
// Only Submit goes through here. Once the provider has accepted a job, nothing
// repeats it: a second Submit is a second billed render.
func submitWithRetry(ctx context.Context, baseDelay time.Duration, submit func(context.Context) (Job, int, error)) (Job, error) {
	if baseDelay <= 0 {
		baseDelay = defaultSubmitRetryDelay
	}
	for attempt := 1; ; attempt++ {
		job, status, err := submit(ctx)
		if err == nil {
			return job, nil
		}
		if !transientStatus(status) || attempt >= defaultSubmitAttempts {
			return Job{}, err
		}
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-time.After(baseDelay * time.Duration(1<<(attempt-1))):
		}
	}
}

func transientStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}
