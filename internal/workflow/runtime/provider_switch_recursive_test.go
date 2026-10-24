// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// Nested and future spawns use the same chat binding. Their original
// selectors remain their own, and a switch doesn't cancel an in-flight tool.
func TestProviderSwitchReachesNestedAndFutureSpawns(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	input := spawnE2EWorkflowInput("provider-switch-recursive")
	input.Inputs["model"] = map[string]interface{}{"id": "claude-5.5-opus@claude"}
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	env.OnActivity("LoadPresetParams", mock.Anything, mock.Anything).Return(map[string]interface{}{
		"model": map[string]interface{}{"tags": []interface{}{"fast"}},
	}, nil)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		switch turn {
		case 1:
			return []map[string]interface{}{spawnToolCall("parent-agent", "delegate the nested work")}, nil
		case 2:
			return nil, usageLimitError(env.Now().Add(4 * time.Hour))
		case 3:
			return []map[string]interface{}{spawnToolCall("future", "work after the provider switch")}, nil
		}
		return nil, nil
	}
	e.childTurn = func(tool string, turn int) (map[string]interface{}, error) {
		if tool == "parent-agent" && turn == 1 {
			return map[string]interface{}{"response_text": "delegating", "tool_calls": toolCallsJSON(t,
				[]map[string]interface{}{spawnToolCall("nested", "nested work")})}, nil
		}
		if (tool == "parent-agent" && turn == 2) || (tool == "nested" && turn == 1) {
			return nil, usageLimitError(env.Now().Add(4 * time.Hour))
		}
		return map[string]interface{}{"response_text": "done"}, nil
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"model": map[string]interface{}{"id": "gpt-6.1-sol@codex"}, InputKeyProviderSwitch: "codex"})
		env.SignalWorkflow(CapacityWakeSignalName, CapacityWakeSignal{UseModel: "gpt-6.1-sol@codex"})
	}, 10*time.Minute)
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, key := range []string{"parent-agent#3", "nested#2", "future#1"} {
		rtx, ok := e.callRuntime[key]
		require.True(t, ok, key)
		require.Equal(t, "codex", rtx.ChatProvider, key)
		require.True(t, rtx.Descendant, key)
		require.Equal(t, []string{"fast"}, rtx.PresetModelTags, key)
		require.Empty(t, rtx.FallbackModel, "a root concrete-model lease must not overwrite descendant tags: %s", key)
	}
}

func TestProviderSwitchResumesStoppedSpawnsOnTheirPresetTags(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	input := spawnE2EWorkflowInput("provider-switch-stopped")
	input.Inputs["model"] = map[string]interface{}{"id": "claude-5.5-opus@claude"}
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	env.OnActivity("LoadPresetParams", mock.Anything, mock.Anything).Return(map[string]interface{}{
		"model": map[string]interface{}{"tags": []interface{}{"flagship"}},
	}, nil)
	var c capacity
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("stopped", "work")}, nil
		}
		if !c.ok() {
			return nil, creditExhausted()
		}
		return nil, nil
	}
	e.childTurn = func(_ string, _ int) (map[string]interface{}, error) {
		if !c.ok() {
			return nil, creditExhausted()
		}
		return map[string]interface{}{"response_text": "done"}, nil
	}
	e.hold = func(tool string, turn int) string {
		if tool == "" && turn == 2 {
			return "stopped-reported"
		}
		return ""
	}
	go awaitReports(e, "stopped-reported", "stopped")
	env.RegisterDelayedCallback(func() {
		c.restore()
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"model": map[string]interface{}{"id": "gpt-6.1-sol@codex"}, InputKeyProviderSwitch: "codex"})
	}, 10*time.Minute)
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	e.mu.Lock()
	defer e.mu.Unlock()
	rtx := e.callRuntime["stopped#2"]
	require.Equal(t, "codex", rtx.ChatProvider)
	require.Equal(t, []string{"flagship"}, rtx.PresetModelTags)
	require.NotNil(t, e.reports)
}
