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
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(rtemporal.NewFlexibleDataConverter())

	var cancelled atomic.Bool
	var afterCancel atomic.Int32
	heartbeatAfterCancel := make(chan struct{}, 64)
	env.SetOnActivityHeartbeatListener(func(*activity.Info, converter.EncodedValues) {
		if cancelled.Load() {
			afterCancel.Add(1)
			heartbeatAfterCancel <- struct{}{}
		}
	})

	toolStarted := make(chan struct{})
	tool := func(ctx context.Context, _ string) (string, error) {
		close(toolStarted)
		select {
		case <-ctx.Done():
			return shieldTestCancelledPrefix + ctx.Err().Error(), nil
		case <-heartbeatAfterCancel:
			return shieldTestToolOutput, nil
		case <-time.After(10 * time.Second):
			return "tool timed out waiting for the scenario", nil
		}
	}

	registry := NewActivityRegistry(&wrapperTestRepo{})
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

// ExecuteTools is the activity this exists for; the opt-in is read off the
// activity at registration.
func TestOutlivesHeartbeatRPCFailure_OptIn(t *testing.T) {
	t.Parallel()
	assert.False(t, outlivesHeartbeatRPCFailure(struct{}{}))
	assert.True(t, outlivesHeartbeatRPCFailure(survivorStub{}))
}

type survivorStub struct{}

func (survivorStub) OutlivesHeartbeatRPCFailure() bool { return true }
