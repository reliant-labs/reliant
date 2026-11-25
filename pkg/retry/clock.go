package retry

import (
	"context"
	"time"
)

// Clock is the time source and the wait of a [Do] loop. Tests substitute
// retrytest.Clock so a retry loop never sleeps; a Temporal workflow can
// substitute one backed by workflow.Now and workflow.Sleep.
type Clock interface {
	Now() time.Time
	// Sleep waits d, or returns ctx's error as soon as ctx ends.
	Sleep(ctx context.Context, d time.Duration) error
}

// SystemClock returns the Clock a nil [Policy.Clock] means: time.Now and a
// timer.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
