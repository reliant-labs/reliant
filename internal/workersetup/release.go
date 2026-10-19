// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
)

// activityReleaseGrace is how long an activity in flight when the worker
// begins stopping may keep running before it is released: cancelled, so it
// fails retryably and Temporal re-dispatches it to a live replica.
const activityReleaseGrace = 5 * time.Second

// activityReportReserve is the SDK's own stop wait beyond the release grace
// (worker.Options.WorkerStopTimeout is the sum): time for every released
// activity to unwind and report its failure. Worker.Stop returns as soon as
// they have, so a drain with nothing stuck ends at the grace, not the sum.
const activityReportReserve = 5 * time.Second

// errReleasedOnDrain is the cause a released activity's context ends with.
var errReleasedOnDrain = errors.New("activity released: the worker is draining")

// activityRelease releases the activities still running when the worker
// begins stopping, after activityReleaseGrace.
//
// The SDK cancels them too, but only once its own WorkerStopTimeout has
// passed, and Worker.Stop does not wait for them to report: it returns, the
// process closes its Temporal client, and the failure report is lost ("the
// client connection is closing"). The server then holds the activity until
// its heartbeat timeout runs out before re-dispatching it — a user-visible
// stall on every deploy. Released here instead, inside WorkerStopTimeout, the
// activity reports while Stop is still waiting for it, so the report reaches
// the server and the retry starts on its retry interval.
type activityRelease struct {
	interceptor.WorkerInterceptorBase

	grace time.Duration
	// stopChannel is the worker's stop channel as an activity sees it;
	// activity.GetWorkerStopChannel outside tests.
	stopChannel func(ctx context.Context) <-chan struct{}
}

func newActivityRelease() *activityRelease {
	return &activityRelease{grace: activityReleaseGrace, stopChannel: activity.GetWorkerStopChannel}
}

// InterceptActivity implements interceptor.WorkerInterceptor.
func (r *activityRelease) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &releasingActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, release: r}
}

// releasingActivity is one activity execution under activityRelease.
type releasingActivity struct {
	interceptor.ActivityInboundInterceptorBase
	release *activityRelease
}

// ExecuteActivity runs the activity on a context that ends
// activityReleaseGrace after the worker begins stopping.
func (a *releasingActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (interface{}, error) {
	stopping := a.release.stopChannel(ctx)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-stopping:
		case <-done:
			return
		}
		grace := time.NewTimer(a.release.grace)
		defer grace.Stop()
		select {
		case <-grace.C:
			cancel(errReleasedOnDrain)
		case <-done:
		}
	}()
	return a.Next.ExecuteActivity(ctx, in)
}
