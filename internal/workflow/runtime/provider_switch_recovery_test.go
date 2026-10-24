// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

func TestDelayedWholeChatLookupDoesNotRetryParticipatingAgentTwice(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"agents": []*db.StoppedSpawnAgent{{ChildThreadID: "agent", ToolCallID: "tc", ParentThreadID: "root"}}}, nil
	}, activity.RegisterOptions{Name: LookupStoppedSubAgentActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) (int, error) {
		tracker := &ChildWorkflowTracker{liveDetachedSpawns: map[string]*detachedSpawnRecord{"tc": {ChildThread: "agent"}}}
		retries := 0
		tracker.resumeStopped = func(workflow.Context, string) bool { retries++; return true }
		tracker.reviveDurableStoppedAsync(ctx, "chat")
		// The first retry fails while the durable lookup is still outstanding.
		delete(tracker.liveDetachedSpawns, "tc")
		tracker.seedStoppedSpawns([]SpawnHandoff{{ToolCallID: "tc", ChildThread: "agent", ParentThread: "root"}})
		if err := workflow.Await(ctx, func() bool { return !tracker.stoppedLookupsInFlight() }); err != nil {
			return -1, err
		}
		return retries, nil
	})
	require.NoError(t, env.GetWorkflowError())
	var retries int
	require.NoError(t, env.GetWorkflowResult(&retries))
	require.Zero(t, retries, "lookup is part of the first recovery, not permission to retry its just-failed participant again")
}

func TestOrdinaryUserWakePreservesUnboundFallbackLease(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	input := spawnE2EWorkflowInput("lease-user-wake")
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return nil, limitWithFacts(env.Now().Add(time.Hour), types.UsageLimitFacts{Provider: "claude", FallbackModel: "gpt-6.1-sol@codex", ProviderEarliestReset: env.Now().Add(time.Hour)})
		}
		if turn == 2 {
			env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: input.ExecContext.Thread, FromUser: true})
			return []map[string]interface{}{{"id": "read", "name": "read_file", "input": "{}"}}, nil
		}
		return nil, nil
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	e.mu.Lock()
	defer e.mu.Unlock()
	require.Equal(t, "gpt-6.1-sol@codex", e.callRuntime["parent#3"].FallbackModel)
	require.Len(t, e.notices, 1, "one opt-in lease notice, not one per message")
}
