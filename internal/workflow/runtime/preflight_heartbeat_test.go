// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/stopnudge"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/lifecycle"
)

type preflightHeartbeatStore struct {
	wrapperTestRepo
	ensureRunning func(context.Context, string, string)
}

func (r *preflightHeartbeatStore) EnsureWorkflowRunning(ctx context.Context, workflowID, chatID string) {
	r.ensureRunning(ctx, workflowID, chatID)
}

// The worker's liveness read uses the activity context, which allows it to
// outlast PreflightDaemonCheck's heartbeat deadline. The existing heartbeat
// loop must be alive while that read is still outstanding, before handler
// code has even started. The SDK listener observes heartbeats that reached
// its test service, not just calls to RecordHeartbeat.
func TestPreflightHeartbeatDuringLivenessRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var suite temporaltest.WorkflowTestSuite
		env := suite.NewTestActivityEnvironment()
		heartbeats := make(chan time.Time, 1)
		env.SetOnActivityHeartbeatListener(func(_ *activity.Info, _ converter.EncodedValues) {
			select {
			case heartbeats <- time.Now():
			default:
			}
		})

		var readStart, firstHeartbeat time.Time
		repo := &preflightHeartbeatStore{ensureRunning: func(context.Context, string, string) {
			readStart = time.Now()
			time.Sleep(preflightHeartbeatTimeout + time.Second)
			select {
			case firstHeartbeat = <-heartbeats:
			default:
			}
		}}
		registry := NewActivityRegistry(ActivityRegistryDeps{Store: repo, Workflows: repo})
		check := func(context.Context, map[string]any) (bool, error) { return true, nil }
		const name = "PreflightDaemonCheck"
		env.RegisterActivityWithOptions(wrapActivity(registry, name, check, lifecycle.AgentWork, false), activity.RegisterOptions{Name: name})

		_, err := env.ExecuteActivity(name, map[string]any{"chat_id": "chat"})
		require.NoError(t, err)
		require.False(t, readStart.IsZero(), "the test must execute the liveness read")
		require.False(t, firstHeartbeat.IsZero(), "no heartbeat reached the service while the read was blocked")
		require.Less(t, firstHeartbeat.Sub(readStart), preflightHeartbeatTimeout)
	})
}

func TestPreflightStopDuringLivenessRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shutdown bool
	}{
		{name: "cancel nudge"},
		{name: "worker shutdown", shutdown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var suite temporaltest.WorkflowTestSuite
				env := suite.NewTestActivityEnvironment()
				stop := make(chan struct{})
				env.SetWorkerStopChannel(stop)
				hub := stopnudge.NewHub()
				var readCause error
				repo := &preflightHeartbeatStore{ensureRunning: func(ctx context.Context, workflowID, _ string) {
					if tc.shutdown {
						close(stop)
					} else {
						hub.Deliver(workflowID)
					}
					select {
					case <-ctx.Done():
						readCause = context.Cause(ctx)
					case <-time.After(preflightHeartbeatTimeout):
					}
				}}
				registry := NewActivityRegistry(ActivityRegistryDeps{Store: repo, Workflows: repo})
				registry.SetStopNudges(hub)
				registry.directHeartbeat = func(context.Context, any) error { return temporal.NewCanceledError() }
				var handlerCalled bool
				check := func(context.Context, map[string]any) (bool, error) {
					handlerCalled = true
					return true, nil
				}
				const name = "PreflightDaemonCheck"
				env.RegisterActivityWithOptions(wrapActivity(registry, name, check, lifecycle.AgentWork, false), activity.RegisterOptions{Name: name})

				_, err := env.ExecuteActivity(name, map[string]any{"chat_id": "chat"})
				require.False(t, handlerCalled, "a stopped preflight must not reach the machine wake")
				if tc.shutdown {
					require.ErrorIs(t, readCause, context.Canceled)
					var appErr *temporal.ApplicationError
					require.ErrorAs(t, err, &appErr)
					require.Equal(t, "WorkerShutdown", appErr.Type())
					require.False(t, appErr.NonRetryable())
				} else {
					require.True(t, temporal.IsCanceledError(readCause), "liveness read lost the server's cancellation cause: %v", readCause)
					require.True(t, temporal.IsCanceledError(err), "the wrapper must not turn cancellation into a retry: %v", err)
				}
			})
		})
	}
}

func TestPreflightHeartbeatDoesNotBypassStoppedRunGuard(t *testing.T) {
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	registry := NewActivityRegistry(ActivityRegistryDeps{
		Store: &wrapperTestRepo{}, Workflows: completedPreflightRun{},
	})
	var handlerCalled bool
	check := func(context.Context, map[string]any) (bool, error) {
		handlerCalled = true
		return true, nil
	}
	const name = "PreflightDaemonCheck"
	env.RegisterActivityWithOptions(wrapActivity(registry, name, check, lifecycle.AgentWork, false), activity.RegisterOptions{Name: name})

	_, err := env.ExecuteActivity(name, map[string]any{"chat_id": "chat"})
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "WorkflowStopped", appErr.Type())
	require.True(t, appErr.NonRetryable())
	require.False(t, handlerCalled)
}

type completedPreflightRun struct{ WorkflowRows }

func (completedPreflightRun) LoadWorkflow(context.Context, string) (*db.Workflow, error) {
	return &db.Workflow{Status: db.Completed()}, nil
}
