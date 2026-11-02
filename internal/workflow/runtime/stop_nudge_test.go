// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/stopnudge"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
)

var testNudgeProbes = []time.Duration{0, 10 * time.Millisecond, 30 * time.Millisecond}

// runFollow runs followStopNudges until it returns or the test's budget
// passes, and reports the probe count and the stop cause (nil: never stopped).
func runFollow(t *testing.T, nudges int, verdict func(probe int) error) (probes int32, cause error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	nudged := make(chan struct{}, 1)
	for range nudges {
		nudged <- struct{}{}
	}
	var n atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		followStopNudges(ctx, nudged, testNudgeProbes, func(context.Context) error {
			return verdict(int(n.Add(1)))
		}, func(c error) { cause = c })
	}()
	<-done
	return n.Load(), cause
}

// No nudge, no probe: the regular heartbeat stays the only RPC.
func TestFollowStopNudges_NoNudgeSendsNothing(t *testing.T) {
	t.Parallel()
	probes, cause := runFollow(t, 0, func(int) error { return nil })
	assert.NoError(t, cause)
	assert.Zero(t, probes)
}

// The cancel lands after the nudge (the workflow must process the signal
// first): early probes see nothing, a later one sees the cancel and stops
// the work with the server's verdict as the cause.
func TestFollowStopNudges_StopsOnTheFirstRealVerdict(t *testing.T) {
	t.Parallel()
	for name, verdict := range map[string]error{
		"server cancel":                 temporal.NewCanceledError(),
		"activity paused":               activity.ErrActivityPaused,
		"activity reset":                activity.ErrActivityReset,
		"attempt unknown to the server": serviceerror.NewNotFound("activity not found"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			probes, cause := runFollow(t, 1, func(probe int) error {
				if probe < 2 {
					return nil
				}
				return verdict
			})
			require.Error(t, cause)
			assert.ErrorIs(t, cause, verdict)
			assert.EqualValues(t, 2, probes, "it stops probing once the server has answered")
		})
	}
}

// A slow or failing server is not a stop: the burst runs out and the work
// continues, with the regular heartbeat still in charge.
func TestFollowStopNudges_FailedProbesDoNotStopTheWork(t *testing.T) {
	t.Parallel()
	probes, cause := runFollow(t, 1, func(int) error { return context.DeadlineExceeded })
	assert.NoError(t, cause)
	assert.EqualValues(t, len(testNudgeProbes), probes, "one burst is exactly the probe schedule, then nothing")
}

// The whole wrapper: an activity of a workflow a stop is requested for stops
// within the probe schedule, not a heartbeat window later, with the server's
// cancel as its cause — the same cause the SDK heartbeat would have delivered.
func TestActivityWrapper_StopNudgeStopsTheActivity(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())

	hub := stopnudge.NewHub()
	var cancelRequested atomic.Bool
	registry := NewActivityRegistry(&wrapperTestRepo{})
	registry.SetStopNudges(hub)
	registry.directHeartbeat = func(context.Context, any) error {
		if cancelRequested.Load() {
			return temporal.NewCanceledError()
		}
		return nil
	}

	started := make(chan string, 1)
	llm := func(ctx context.Context, _ string) (string, error) {
		started <- activity.GetInfo(ctx).WorkflowExecution.ID
		select {
		case <-ctx.Done():
			return "stopped: " + context.Cause(ctx).Error(), nil
		case <-time.After(10 * time.Second):
			return "never stopped", nil
		}
	}
	wrapped := wrapActivity(registry, "CallLLM", llm, lifecycle.AgentWork, true)
	env.RegisterActivityWithOptions(wrapped, activity.RegisterOptions{Name: "CallLLM"})

	go func() {
		workflowID := <-started
		// A nudge before the cancel is recorded probes and finds nothing;
		// the server records the cancel; the next nudge's probe finds it.
		hub.Deliver(workflowID)
		time.Sleep(50 * time.Millisecond)
		cancelRequested.Store(true)
		hub.Deliver(workflowID)
	}()

	begin := time.Now()
	encoded, err := env.ExecuteActivity("CallLLM", "input")
	require.NoError(t, err)
	var result string
	require.NoError(t, encoded.Get(&result))
	assert.Equal(t, "stopped: "+temporal.NewCanceledError().Error(), result)
	assert.Less(t, time.Since(begin), 3*time.Second,
		"a nudged stop must not wait for the heartbeat window (%s)", activityHeartbeatTimeout)
}

// A nudge for another workflow does not reach this activity.
func TestActivityWrapper_StopNudgeForAnotherWorkflowIsIgnored(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())

	hub := stopnudge.NewHub()
	var probes atomic.Int32
	registry := NewActivityRegistry(&wrapperTestRepo{})
	registry.SetStopNudges(hub)
	registry.directHeartbeat = func(context.Context, any) error {
		probes.Add(1)
		return temporal.NewCanceledError()
	}
	finish := make(chan struct{})
	started := make(chan struct{})
	act := func(ctx context.Context, _ string) (string, error) {
		close(started)
		select {
		case <-ctx.Done():
			return "", errors.New("stopped by a nudge meant for another workflow")
		case <-finish:
			return "done", nil
		}
	}
	env.RegisterActivityWithOptions(wrapActivity(registry, "ExecuteTools", act, lifecycle.AgentWork, true),
		activity.RegisterOptions{Name: "ExecuteTools"})
	go func() {
		<-started
		hub.Deliver("some-other-workflow")
		time.Sleep(400 * time.Millisecond)
		close(finish)
	}()
	encoded, err := env.ExecuteActivity("ExecuteTools", "input")
	require.NoError(t, err)
	var result string
	require.NoError(t, encoded.Get(&result))
	assert.Equal(t, "done", result)
	assert.Zero(t, probes.Load())
}
