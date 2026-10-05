// Copyright (c) 2025 Reliant Labs
package videogen

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Wait polls job until it finishes, the deadline passes, or ctx is cancelled.
//
// It never Submits. A caller that resumes a persisted job and a caller that
// just submitted one go through this same loop, which is the point of the
// Submit/Poll split.
//
// Transient Poll failures (anything that is not a classified *Error) are
// tolerated up to MaxConsecutiveErrors in a row: the provider job is running
// regardless, and abandoning a billed render over one dropped connection is the
// worse outcome. A classified *Error is final and returned as is.
func Wait(ctx context.Context, client Client, job Job, opts WaitOptions) (*Response, error) {
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	deadline := opts.Deadline
	if deadline <= 0 {
		deadline = DefaultPollDeadline
	}
	maxErrors := opts.MaxConsecutiveErrors
	if maxErrors <= 0 {
		maxErrors = 5
	}

	timeout := time.NewTimer(deadline)
	defer timeout.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	consecutiveErrors := 0
	for {
		response, done, err := client.Poll(ctx, job)
		switch {
		case err == nil && done:
			return response, nil
		case err == nil:
			consecutiveErrors = 0
		default:
			var classified *Error
			if errors.As(err, &classified) {
				if classified.Job.ID == "" {
					classified.Job = job
				}
				return nil, classified
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			consecutiveErrors++
			if consecutiveErrors >= maxErrors {
				return nil, fmt.Errorf("polling video job %s failed %d times in a row: %w", job.ID, consecutiveErrors, err)
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, &Error{
				Kind:    KindTimeout,
				Message: fmt.Sprintf("video job %s is still rendering after %s; it may yet finish", job.ID, deadline),
				Job:     job,
			}
		case <-ticker.C:
		}
	}
}
