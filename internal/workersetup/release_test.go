// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/interceptor"
)

// blockingActivity runs until its context ends, or returns at once when
// quick, and reports the cause its context ended with.
type blockingActivity struct {
	interceptor.ActivityInboundInterceptorBase
	quick bool
	cause chan error
}

func (b *blockingActivity) ExecuteActivity(ctx context.Context, _ *interceptor.ExecuteActivityInput) (interface{}, error) {
	if b.quick {
		b.cause <- nil
		return "done", nil
	}
	<-ctx.Done()
	b.cause <- context.Cause(ctx)
	return nil, ctx.Err()
}

func releaseFor(stop <-chan struct{}, grace time.Duration, next *blockingActivity) interceptor.ActivityInboundInterceptor {
	r := &activityRelease{grace: grace, stopChannel: func(context.Context) <-chan struct{} { return stop }}
	return r.InterceptActivity(context.Background(), next)
}

func TestActivityReleaseCancelsWhatOutlivesTheGraceAfterStop(t *testing.T) {
	stop := make(chan struct{})
	next := &blockingActivity{cause: make(chan error, 1)}
	a := releaseFor(stop, 10*time.Millisecond, next)

	ran := make(chan error, 1)
	go func() {
		_, err := a.ExecuteActivity(context.Background(), &interceptor.ExecuteActivityInput{})
		ran <- err
	}()
	select {
	case <-next.cause:
		t.Fatal("released before the worker began stopping")
	case <-time.After(30 * time.Millisecond):
	}

	close(stop)
	select {
	case cause := <-next.cause:
		require.ErrorIs(t, cause, errReleasedOnDrain)
	case <-time.After(5 * time.Second):
		t.Fatal("not released after the grace")
	}
	require.ErrorIs(t, <-ran, context.Canceled)
}

func TestActivityReleaseLeavesAnActivityThatFinishesAlone(t *testing.T) {
	stop := make(chan struct{})
	close(stop) // stopping already: the grace still applies
	next := &blockingActivity{quick: true, cause: make(chan error, 1)}
	a := releaseFor(stop, time.Hour, next)

	out, err := a.ExecuteActivity(context.Background(), &interceptor.ExecuteActivityInput{})
	require.NoError(t, err)
	require.Equal(t, "done", out)
	require.NoError(t, <-next.cause)
}

func TestActivityReleaseKeepsTheCallersCancellation(t *testing.T) {
	next := &blockingActivity{cause: make(chan error, 1)}
	a := releaseFor(make(chan struct{}), time.Hour, next)

	ctx, cancel := context.WithCancelCause(context.Background())
	server := context.Canceled
	go cancel(server)
	_, err := a.ExecuteActivity(ctx, &interceptor.ExecuteActivityInput{})
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, <-next.cause, errReleasedOnDrain, "a server cancel is not a release")
}
