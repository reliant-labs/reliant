// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// exhaustedErrorOfType returns the error a workflow REALLY observes when an
// activity's ladder runs out, for an activity that fails with the given
// ApplicationError type.
//
// Built by running a failing activity in the Temporal test environment rather
// than by hand: heartbeatCancelExhausted has to see through the *ActivityError
// wrapper Temporal puts around the cause, and a synthetic stand-in would prove
// only that the test and the code agree with each other.
func exhaustedErrorOfType(t *testing.T, errType, msg string) error {
	t.Helper()

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	failing := func(context.Context) error {
		return temporal.NewApplicationError(msg, errType)
	}
	env.RegisterActivityWithOptions(failing, activity.RegisterOptions{Name: "AlwaysFails"})

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: activityHeartbeatTimeout,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
		})
		return workflow.ExecuteActivity(ctx, "AlwaysFails").Get(ctx, nil)
	})

	require.True(t, env.IsWorkflowCompleted())
	err := env.GetWorkflowError()
	require.Error(t, err, "the activity must actually exhaust its ladder")
	return err
}

// A step whose ladder was consumed by heartbeat RPC failures must be
// distinguishable from one that failed for a real reason. The first is
// infrastructure noise against a HEALTHY activity and deserves a fresh ladder;
// the second needs to reach the user.
//
// This is the classification that decided chat ee527bdd: five attempts landed
// inside one burst of heartbeat failures and the chat auto-paused with an error
// naming a Temporal deadline, which is not something its user could act on.
func TestHeartbeatCancelExhausted(t *testing.T) {
	t.Parallel()

	t.Run("heartbeat cancel is recognized through the ActivityError wrapper", func(t *testing.T) {
		err := exhaustedErrorOfType(t, heartbeatCancelErrorType,
			"heartbeat RPC failed while running CallLLM; retrying")
		assert.True(t, heartbeatCancelExhausted(err),
			"a ladder spent on heartbeat failures must be recognized so the step gets a fresh one")
	})

	t.Run("a real provider failure is not a heartbeat cancel", func(t *testing.T) {
		err := exhaustedErrorOfType(t, "RateLimitError", "429 Too Many Requests")
		assert.False(t, heartbeatCancelExhausted(err),
			"retrying a rate limit harder does not make it succeed; the user must be told")
	})

	t.Run("worker shutdown is not a heartbeat cancel", func(t *testing.T) {
		err := exhaustedErrorOfType(t, "WorkerShutdown", "worker shut down while running CallLLM")
		assert.False(t, heartbeatCancelExhausted(err))
	})

	t.Run("nil and plain errors are not heartbeat cancels", func(t *testing.T) {
		assert.False(t, heartbeatCancelExhausted(nil))
		assert.False(t, heartbeatCancelExhausted(errors.New("context deadline exceeded")))
	})
}

// The restart allowance is what keeps a genuinely-down Temporal server from
// livelocking a chat forever, so the bound has to actually bind.
func TestLadderRestartsBound(t *testing.T) {
	t.Parallel()

	t.Run("grants exactly maxHeartbeatLadderRestarts then stops", func(t *testing.T) {
		var restarts ladderRestarts
		for i := range maxHeartbeatLadderRestarts {
			assert.True(t, restarts.grantRestart("call_llm"),
				"restart %d of %d should be granted", i+1, maxHeartbeatLadderRestarts)
		}
		assert.False(t, restarts.grantRestart("call_llm"),
			"past the bound the chat must pause rather than spin forever")
	})

	t.Run("allowance is per step", func(t *testing.T) {
		var restarts ladderRestarts
		for range maxHeartbeatLadderRestarts {
			require.True(t, restarts.grantRestart("call_llm"))
		}
		assert.True(t, restarts.grantRestart("execute_tools"),
			"one exhausted step must not spend another step's allowance")
	})

	t.Run("a successful step gets its full allowance back", func(t *testing.T) {
		var restarts ladderRestarts
		for range maxHeartbeatLadderRestarts {
			require.True(t, restarts.grantRestart("call_llm"))
		}
		require.False(t, restarts.grantRestart("call_llm"))

		restarts.clear("call_llm")
		assert.True(t, restarts.grantRestart("call_llm"),
			"an unrelated burst hours later must not inherit a spent allowance")
	})

	t.Run("zero value is usable", func(t *testing.T) {
		var restarts ladderRestarts
		assert.True(t, restarts.grantRestart("call_llm"))
		restarts.clear("never-seen")
	})
}
