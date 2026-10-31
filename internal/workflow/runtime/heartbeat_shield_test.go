// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"

	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
)

const (
	shieldTestToolOutput = "command output"
	// What ExecuteTools reports for a tool whose context was cancelled
	// (handleToolExecutionResult) — 42 characters, the totalResultChars every
	// killed activity logged at 09:16:10.
	shieldTestCancelledPrefix = "Tool execution cancelled: "
)

// runShieldScenario runs a tool-shaped activity through the real
// ActivityWrapper and, once the tool is running, cancels the activity context
// the way the SDK does: internalHeartBeat calls the context's CancelCauseFunc
// with the error the heartbeat RPC returned. sdkCancelCause stands in for that
// RPC error. The tool finishes on its own once a heartbeat lands after the
// cancel, or reports itself cancelled if its context dies first — exactly
// ExecuteTools' two outcomes.
func runShieldScenario(t *testing.T, outlives bool, sdkCancelCause error) (result string, heartbeatsAfterCancel int32) {
	t.Helper()
	return runShieldScenarioWithVerdict(t, outlives, sdkCancelCause, nil)
}

// runShieldScenarioWithVerdict is runShieldScenario with the server's answer
// to the direct heartbeats an engaged shield sends: verdict is returned by
// each one (nil: the server accepted it). A heartbeat after the cancel —
// through the SDK or directly — lets the tool finish, unless its context dies
// first.
func runShieldScenarioWithVerdict(t *testing.T, outlives bool, sdkCancelCause, verdict error) (result string, heartbeatsAfterCancel int32) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())

	var cancelled atomic.Bool
	var afterCancel atomic.Int32
	heartbeatAfterCancel := make(chan struct{}, 64)
	env.SetOnActivityHeartbeatListener(func(*activity.Info, converter.EncodedValues) {
		// With a stopping verdict, only the shield's direct heartbeat may
		// decide the outcome: an SDK heartbeat that races in before the
		// shield engages must not let the tool finish first.
		if cancelled.Load() && verdict == nil {
			afterCancel.Add(1)
			heartbeatAfterCancel <- struct{}{}
		}
	})

	toolStarted := make(chan struct{})
	tool := func(ctx context.Context, _ string) (string, error) {
		close(toolStarted)
		select {
		case <-ctx.Done():
			return shieldTestCancelledPrefix + context.Cause(ctx).Error(), nil
		case <-heartbeatAfterCancel:
			// A verdict that stops the work cancels ctx in the same call
			// that delivered this heartbeat; prefer the cancellation.
			if ctx.Err() != nil {
				return shieldTestCancelledPrefix + context.Cause(ctx).Error(), nil
			}
			return shieldTestToolOutput, nil
		case <-time.After(10 * time.Second):
			return "tool timed out waiting for the scenario", nil
		}
	}

	registry := NewActivityRegistry(&wrapperTestRepo{})
	registry.directHeartbeat = func(context.Context, any) error {
		if verdict == nil {
			afterCancel.Add(1)
			heartbeatAfterCancel <- struct{}{}
		}
		return verdict
	}
	wrapped := wrapActivity(registry, "ExecuteTools", tool, lifecycle.AgentWork, outlives)
	env.RegisterActivityWithOptions(func(ctx context.Context, in string) (string, error) {
		sdkCtx, sdkCancel := context.WithCancelCause(ctx)
		defer sdkCancel(nil)
		go func() {
			<-toolStarted
			cancelled.Store(true)
			sdkCancel(sdkCancelCause)
		}()
		return wrapped(sdkCtx, in)
	}, activity.RegisterOptions{Name: "ExecuteTools"})

	encoded, err := env.ExecuteActivity("ExecuteTools", "input")
	require.NoError(t, err)
	require.NoError(t, encoded.Get(&result))
	return result, afterCancel.Load()
}

// The 09:16:09 incident: the heartbeat RPC timed out, the SDK cancelled the
// activity context, and the tool running inside ExecuteTools was killed with
// nothing having asked it to stop. A shielded activity must finish the tool and
// keep heartbeating, so the result reaches the conversation.
func TestHeartbeatShield_SlowHeartbeatRPCDoesNotKillRunningTool(t *testing.T) {
	t.Parallel()
	result, heartbeats := runShieldScenario(t, true, context.DeadlineExceeded)
	assert.Equal(t, shieldTestToolOutput, result,
		"a heartbeat RPC that merely timed out must not cancel the tool")
	assert.Positive(t, heartbeats,
		"the wrapper must keep heartbeating after the failed RPC, or the server times the attempt out instead")
}

// An activity that did not opt in keeps today's behaviour: the failed
// heartbeat cancels it, and the wrapper's HeartbeatCancel retry path (CallLLM)
// still applies.
func TestHeartbeatShield_UnshieldedActivityStillCancelled(t *testing.T) {
	t.Parallel()
	result, _ := runShieldScenario(t, false, context.DeadlineExceeded)
	assert.Contains(t, result, shieldTestCancelledPrefix)
}

// Everything that is not a merely-failed RPC is a real instruction to stop and
// must still reach the tool, cause intact.
func TestHeartbeatShield_RealCancellationsStillStopTheTool(t *testing.T) {
	t.Parallel()
	for name, cause := range map[string]error{
		"server cancel":                 temporal.NewCanceledError(),
		"activity paused":               activity.ErrActivityPaused,
		"activity reset":                activity.ErrActivityReset,
		"attempt unknown to the server": serviceerror.NewNotFound("activity not found"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, _ := runShieldScenario(t, true, cause)
			assert.Contains(t, result, shieldTestCancelledPrefix)
		})
	}
}

// Once the shield has absorbed a spurious cancel, the SDK can no longer report
// a later one: its cancel func is spent. A cancel, pause or reset the server
// returns to the shield's own direct heartbeat must still stop the work, with
// its cause — otherwise a user who interrupts after a slow heartbeat watches
// the stream run on to the end of the turn.
func TestHeartbeatShield_RealCancelAfterEngagingStillStopsWork(t *testing.T) {
	t.Parallel()
	for name, verdict := range map[string]error{
		"server cancel":                 temporal.NewCanceledError(),
		"activity paused":               activity.ErrActivityPaused,
		"activity reset":                activity.ErrActivityReset,
		"attempt unknown to the server": serviceerror.NewNotFound("activity not found"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, _ := runShieldScenarioWithVerdict(t, true, context.DeadlineExceeded, verdict)
			assert.Equal(t, shieldTestCancelledPrefix+verdict.Error(), result,
				"the direct heartbeat's verdict must cancel the work with that cause")
		})
	}
}

// The engaged shield's direct heartbeat: rate-limited to the SDK's cadence,
// tolerant of a slow server, and stopped only by a real instruction.
func TestHeartbeatShield_DirectHeartbeatVerdicts(t *testing.T) {
	t.Parallel()
	newShield := func() *heartbeatShield {
		work, cancel := context.WithCancelCause(context.Background())
		t.Cleanup(func() { cancel(nil) })
		return &heartbeatShield{work: work, cancelWork: cancel}
	}
	t0 := time.Unix(1_800_000_000, 0)

	t.Run("rate limited to the SDK cadence", func(t *testing.T) {
		t.Parallel()
		s := newShield()
		var sent int
		send := func(context.Context, any) error { sent++; return nil }
		require.NoError(t, s.heartbeat(t0, send, nil))
		require.NoError(t, s.heartbeat(t0.Add(500*time.Millisecond), send, nil))
		require.NoError(t, s.heartbeat(t0.Add(shieldedHeartbeatInterval), send, nil))
		assert.Equal(t, 2, sent)
	})

	t.Run("a slow server keeps the work running", func(t *testing.T) {
		t.Parallel()
		s := newShield()
		var rpcDeadline time.Duration
		slow := func(ctx context.Context, _ any) error {
			deadline, _ := ctx.Deadline()
			rpcDeadline = time.Until(deadline)
			return context.DeadlineExceeded
		}
		err := s.heartbeat(t0, slow, nil)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.NoError(t, s.work.Err(), "a failed RPC is not an instruction to stop")
		assert.Equal(t, t0, s.failingSince)
		assert.Greater(t, rpcDeadline, 2*time.Second,
			"the direct RPC must get more than the SDK's 2s budget, which is what a loaded server blows")

		require.NoError(t, s.heartbeat(t0.Add(shieldedHeartbeatInterval), func(context.Context, any) error { return nil }, nil))
		assert.True(t, s.failingSince.IsZero(), "a successful heartbeat ends the failing run")
	})

	t.Run("a real cancel stops the work with its cause", func(t *testing.T) {
		t.Parallel()
		s := newShield()
		canceled := temporal.NewCanceledError()
		err := s.heartbeat(t0, func(context.Context, any) error { return canceled }, nil)
		require.Error(t, err)
		require.Error(t, s.work.Err())
		assert.True(t, temporal.IsCanceledError(context.Cause(s.work)))
	})
}

// ExecuteTools is the activity this exists for; the opt-in is read off the
// activity at registration.
func TestOutlivesHeartbeatRPCFailure_OptIn(t *testing.T) {
	t.Parallel()
	assert.False(t, outlivesHeartbeatRPCFailure(struct{}{}))
	assert.True(t, outlivesHeartbeatRPCFailure(survivorStub{}))
}

type survivorStub struct{}

func (survivorStub) OutlivesHeartbeatRPCFailure() bool { return true }
