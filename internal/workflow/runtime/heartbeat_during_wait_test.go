// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
)

// A tool call inside ExecuteTools can block for a long time without doing
// anything the activity could report: shell_wait parks for up to 20 minutes,
// and a call whose daemon dropped waits up to toolexec.DaemonReconnectGrace
// (90s) for it to reconnect. ExecuteTools is dispatched with HeartbeatTimeout =
// activityHeartbeatTimeout (60s, StepExecutor.activityOptions), and an attempt
// the server times out is re-dispatched — which for a tool means
// "interrupted", never a second run (isActivityRetry). So such a wait must not
// cost the attempt its heartbeat.
//
// It does not, because the ActivityWrapper heartbeats from its own goroutine
// every activityHeartbeatInterval whatever the activity body is doing. This
// pins that against the SDK's own test server, which enforces HeartbeatTimeout:
// a body that blocks for well over the timeout completes, while the same body
// registered without the wrapper is timed out. The timeout is shortened to
// keep the test quick; the property is the ratio, not the 30s.
//
// The margin here is narrower than in production. The test server derives
// the heartbeat throttle as 0.8 x HeartbeatTimeout with no cap, so heartbeats
// land every 3.2s against a 4s timeout. A worker caps the throttle at
// workersetup's 15s, so in production they land every 15s against 60s.
func TestActivityWrapper_HeartbeatsThroughAWaitLongerThanTheHeartbeatTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: blocks for several heartbeat timeouts in real time; runs in the full lane")
	}
	t.Parallel()

	const (
		heartbeatTimeout = 4 * time.Second
		blockFor         = 2*heartbeatTimeout + time.Second
	)
	// The wait: no progress and no heartbeat of its own, exactly like the
	// router's reconnect wait or shell_wait polling a running process.
	blocked := func(ctx context.Context, _ string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(blockFor):
			return "reconnected", nil
		}
	}

	run := func(t *testing.T, activityFn func(context.Context, string) (string, error)) (string, error) {
		t.Helper()
		var suite temporaltest.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.SetTestTimeout(blockFor + 30*time.Second) // the activity runs in real time
		env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
		env.RegisterActivityWithOptions(activityFn, activity.RegisterOptions{Name: executeToolsActivityName})
		env.RegisterWorkflowWithOptions(func(ctx workflow.Context) (string, error) {
			ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
				StartToCloseTimeout: time.Minute,
				HeartbeatTimeout:    heartbeatTimeout,
				RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
			})
			var out string
			err := workflow.ExecuteActivity(ctx, executeToolsActivityName, "input").Get(ctx, &out)
			return out, err
		}, workflow.RegisterOptions{Name: "heartbeat-during-wait"})

		env.ExecuteWorkflow("heartbeat-during-wait")
		require.True(t, env.IsWorkflowCompleted())
		var out string
		if err := env.GetWorkflowError(); err != nil {
			return "", err
		}
		require.NoError(t, env.GetWorkflowResult(&out))
		return out, nil
	}

	t.Run("wrapped, as ExecuteTools is registered", func(t *testing.T) {
		t.Parallel()
		registry := NewActivityRegistry(&wrapperTestRepo{})
		out, err := run(t, wrapActivity(registry, executeToolsActivityName, blocked, lifecycle.AgentWork, true))
		require.NoError(t, err, "a wait longer than the heartbeat timeout must not time the attempt out")
		assert.Equal(t, "reconnected", out)
	})

	// Without this the case above would prove nothing: the test server must
	// really time out a body that does not heartbeat.
	t.Run("the same wait unwrapped is timed out", func(t *testing.T) {
		t.Parallel()
		_, err := run(t, blocked)
		require.Error(t, err)
		var timeoutErr *temporal.TimeoutError
		require.True(t, errors.As(err, &timeoutErr), "want a timeout, got %v", err)
		assert.Equal(t, enumspb.TIMEOUT_TYPE_HEARTBEAT, timeoutErr.TimeoutType())
	})
}
