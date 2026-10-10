// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/machinewait"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func preflightWaitWorkflow(ctx workflow.Context) error {
	return waitForDaemon(ctx, map[string]interface{}{"chat_id": "c1"})
}

func newPreflightWaitEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterWorkflow(preflightWaitWorkflow)
	env.RegisterActivityWithOptions(func(map[string]interface{}) (map[string]interface{}, error) { return nil, nil },
		activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	return env
}

func TestWaitForDaemon_ReadyIsSingleActivity(t *testing.T) {
	env := newPreflightWaitEnv(t)
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(map[string]interface{}{"daemon_available": true}, nil).Once()
	env.ExecuteWorkflow(preflightWaitWorkflow)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestWaitForDaemon_WaitsThenProceeds(t *testing.T) {
	env := newPreflightWaitEnv(t)
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(map[string]interface{}{"waiting": true}, nil).Twice()
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(map[string]interface{}{"daemon_available": true}, nil).Once()
	env.ExecuteWorkflow(preflightWaitWorkflow)
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// machineUpAt is a PreflightDaemonCheck that behaves like the real activity
// for a machine that attaches at workflow time upAt: before then a slice
// reports waiting, and the final slice fails (the activity's terminal error).
// checks records the workflow time of every execution.
func machineUpAt(env *testsuite.TestWorkflowEnvironment, upAt time.Time, checks *[]time.Time) func(map[string]interface{}) (map[string]interface{}, error) {
	return func(in map[string]interface{}) (map[string]interface{}, error) {
		now := env.Now()
		*checks = append(*checks, now)
		if !now.Before(upAt) {
			return map[string]interface{}{"daemon_available": true}, nil
		}
		if final, _ := in["final"].(bool); final {
			return nil, errors.New("Your machine didn't come online in time")
		}
		return map[string]interface{}{"waiting": true}, nil
	}
}

// A cold start with an image pull outlasted the old 10-minute budget, and the
// run then failed with the message unsent. A machine that is still starting
// keeps the run waiting — and the wait costs a timer, not a polling activity:
// a handful of checks over twenty minutes, not a 60-second slice after
// another.
func TestWaitForDaemon_MachineStartingForTwentyMinutesStillDelivers(t *testing.T) {
	env := newPreflightWaitEnv(t)
	start := env.Now()
	var checks []time.Time
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(machineUpAt(env, start.Add(20*time.Minute), &checks))

	env.ExecuteWorkflow(preflightWaitWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a machine that comes up after 20 minutes is still waited for")
	require.LessOrEqual(t, len(checks), 10,
		"waiting is a timer between checks, not back-to-back polling slices (got %d checks)", len(checks))
	last := checks[len(checks)-1]
	require.Less(t, last.Sub(start.Add(20*time.Minute)), machineRecheckMax+time.Minute,
		"the run notices the machine within one recheck interval")
}

// A machine that connects signals the waiting run, which checks at once
// instead of sleeping out its recheck timer.
func TestWaitForDaemon_MachineSignalChecksAtOnce(t *testing.T) {
	env := newPreflightWaitEnv(t)
	start := env.Now()
	upAt := start.Add(10 * time.Minute)
	var checks []time.Time
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(machineUpAt(env, upAt, &checks))
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(machinewait.SignalName, machinewait.Signal{Reason: machinewait.ReasonMachineConnected})
	}, 10*time.Minute)

	env.ExecuteWorkflow(preflightWaitWorkflow)

	require.NoError(t, env.GetWorkflowError())
	last := checks[len(checks)-1]
	require.Less(t, last.Sub(upAt), time.Second,
		"the check runs when the signal lands (the recheck timer would have waited until ~13.5m)")
}

// "Continue without machine" moves the conversation to a no-machine branch;
// the original run stops waiting and ends cancelled, not failed.
func TestWaitForDaemon_ContinueWithoutMachineEndsTheWaitCancelled(t *testing.T) {
	env := newPreflightWaitEnv(t)
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(map[string]interface{}{"waiting": true}, nil)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(machinewait.SignalName, machinewait.Signal{Abandon: true, Reason: machinewait.ReasonContinuedWithoutMachine})
	}, 3*time.Minute)

	env.ExecuteWorkflow(preflightWaitWorkflow)

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.True(t, temporal.IsCanceledError(err), "the run ends cancelled, got %v", err)
	require.True(t, isMachineWaitAbandoned(err), "and is recorded as cancelled by the completion handler")
	require.False(t, isMachineWaitAbandoned(temporal.NewCanceledError()), "a plain cancel is not an abandoned wait")
	require.False(t, isMachineWaitAbandoned(errors.New("boom")))
}

func TestWaitForDaemon_CapExhaustionSetsFinalAndFails(t *testing.T) {
	env := newPreflightWaitEnv(t)
	start := env.Now()
	var finals []bool
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(func(in map[string]interface{}) (map[string]interface{}, error) {
			final, _ := in["final"].(bool)
			finals = append(finals, final)
			if final {
				return nil, errors.New("machine never came up")
			}
			return map[string]interface{}{"waiting": true}, nil
		})
	env.ExecuteWorkflow(preflightWaitWorkflow)
	err := env.GetWorkflowError()
	require.Error(t, err)
	require.Contains(t, err.Error(), "machine never came up")
	require.NotEmpty(t, finals)
	require.True(t, finals[len(finals)-1], "last call is final")
	for _, f := range finals[:len(finals)-1] {
		require.False(t, f)
	}
	require.GreaterOrEqual(t, env.Now().Sub(start), preflightMachineWaitCap, "the final check comes only after the cap")
	require.Less(t, len(finals), 100, "hours of waiting stay a bounded number of checks (got %d)", len(finals))
}

func TestWaitForDaemon_CancelDuringWait(t *testing.T) {
	env := newPreflightWaitEnv(t)
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).
		Return(map[string]interface{}{"waiting": true}, nil)
	env.RegisterDelayedCallback(env.CancelWorkflow, 5*time.Minute)
	env.ExecuteWorkflow(preflightWaitWorkflow)
	err := env.GetWorkflowError()
	require.Error(t, err)
	var canceled *temporal.CanceledError
	require.True(t, errors.As(err, &canceled) || temporal.IsCanceledError(err), "got %v", err)
}

// The daemon_unavailable card shows the activity's own message, not Temporal's
// "activity error (type: …, scheduledEventID: …)" wrapping that err.Error()
// leads with — observed burying the user-facing text on a real stack.
func TestPreflightFailureMessage_ShowsTheActivityMessage(t *testing.T) {
	const want = "Your machine didn't come online within 10 minutes. Check that it is running"
	env := newPreflightWaitEnv(t)
	var activityErr error
	env.OnActivity("PreflightDaemonCheck", mock.Anything, mock.Anything).Return(nil, errors.New(want))
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
		activityErr = waitForDaemon(ctx, map[string]interface{}{"chat_id": "c1"})
		return nil
	}, workflow.RegisterOptions{Name: "capturePreflightErr"})
	env.ExecuteWorkflow("capturePreflightErr")
	require.NoError(t, env.GetWorkflowError())

	require.Error(t, activityErr)
	require.Contains(t, activityErr.Error(), "activity error", "precondition: the raw error carries Temporal's wrapping")
	require.Equal(t, want, preflightFailureMessage(activityErr))
	require.Equal(t, "plain", preflightFailureMessage(errors.New("plain")), "a non-activity error is shown as-is")
}
