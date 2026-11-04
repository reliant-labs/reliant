// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Unsupported tag/provider mapping visibly holds only its thread. The root
// stays live (not paused), and a compatible edit wakes the held model call.
func TestProviderSwitchUnsupportedMappingHoldsUntilEdited(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	input := spawnE2EWorkflowInput("provider-switch-hold")
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("held", "work")}, nil
		}
		return nil, nil
	}
	e.childTurn = func(_ string, turn int) (map[string]interface{}, error) {
		if turn == 1 {
			return nil, temporal.NewNonRetryableApplicationError("This thread is waiting: no fast model on chosen provider", "ProviderSwitchHold", nil)
		}
		return map[string]interface{}{"response_text": "done"}, nil
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"model": map[string]interface{}{"id": "gpt-6.1-sol@codex"}, InputKeyProviderSwitch: "codex"})
	}, 10*time.Minute)
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	require.Empty(t, e.pausedStatuses(), "a mapping hold does not pause healthy siblings or the root")
	require.NotNil(t, e.reportFor("held"))
	require.Equal(t, 2, reportKind(e.reportFor("held")))
	e.mu.Lock()
	defer e.mu.Unlock()
	require.Len(t, e.workflowErrors, 1, "a visible error explains the held thread")
}

// A switch is routing state, not an interrupt. A tool activity in flight at
// the update finishes once with its existing side effects, uncancelled.
func TestProviderSwitchDoesNotCancelInFlightSideEffectTools(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	input := spawnE2EWorkflowInput("provider-switch-tool")
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{{"id": "side-effect", "name": "shell", "input": `{}`}}, nil
		}
		return nil, nil
	}
	calls := 0
	env.RegisterActivityWithOptions(func(ctx context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
		calls++
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"model": map[string]interface{}{"id": "gpt-6.1-sol@codex"}, InputKeyProviderSwitch: "codex"})
		require.NoError(t, ctx.Err(), "the model switch must not cancel the tool")
		return map[string]interface{}{"tool_results": []interface{}{}}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools", DisableAlreadyRegisteredCheck: true})
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, calls, "the tool is not re-dispatched by a provider switch")
}

// The user's message to any portion of a chat wakes all unfinished portions.
// No sibling needs a per-thread continue after capacity is restored.
func TestMessageToOneThreadWakesAllUsageWaitingThreads(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	input := spawnE2EWorkflowInput("provider-switch-resume-all")
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	var c capacity
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("a", "work a"), spawnToolCall("b", "work b")}, nil
		}
		if !c.ok() {
			return nil, usageLimitError(env.Now().Add(4 * time.Hour))
		}
		return nil, nil
	}
	e.childTurn = func(_ string, _ int) (map[string]interface{}, error) {
		if !c.ok() {
			return nil, usageLimitError(env.Now().Add(4 * time.Hour))
		}
		return map[string]interface{}{"response_text": "done"}, nil
	}
	env.RegisterDelayedCallback(func() {
		c.restore()
		env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: e.threadOf("a"), FromUser: true})
	}, 10*time.Minute)
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	for _, who := range []string{"a", "b"} {
		at, ok := e.callAt(who, 2)
		require.True(t, ok, who)
		require.Less(t, at.Sub(startOf(e)), time.Hour, "wakes at one message, not at reset: %s", who)
	}
}
