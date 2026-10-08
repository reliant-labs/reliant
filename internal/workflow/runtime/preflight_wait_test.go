// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
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

func TestWaitForDaemon_BudgetExhaustionSetsFinalAndFails(t *testing.T) {
	env := newPreflightWaitEnv(t)
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
	require.GreaterOrEqual(t, len(finals), int(preflightWaitBudget/(preflightWaitSlice+preflightSliceBackoff)))
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
