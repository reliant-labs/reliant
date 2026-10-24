// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// The model picker updates the root's concrete model but each spawn owns its
// preset's semantic selector. Updating that model must change the provider the
// spawn resolves its own tag on, not replace the tag with the root's model.
func TestProviderSwitchReachesAnExistingTaggedSpawn(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	input := spawnE2EWorkflowInput("provider-switch-existing")
	input.Inputs["model"] = map[string]interface{}{"id": "claude-5.5-opus@claude"}
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	// The general preset owns its selector, as production's general.yaml
	// does. The base harness otherwise returns no preset params, which would
	// accidentally let a global concrete-model update overwrite the child.
	env.OnActivity("LoadPresetParams", mock.Anything, mock.Anything).Return(map[string]interface{}{
		"model": map[string]interface{}{"tags": []interface{}{"flagship"}},
	}, nil)
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{spawnToolCall("tagged", "implement the feature")}, nil
		}
		if turn == 2 {
			return nil, usageLimitError(env.Now().Add(4 * time.Hour))
		}
		return nil, nil
	}
	e.childTurn = func(_ string, turn int) (map[string]interface{}, error) {
		if turn == 1 {
			return nil, usageLimitError(env.Now().Add(4 * time.Hour))
		}
		return map[string]interface{}{"response_text": "done"}, nil
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{
			"model":                map[string]interface{}{"id": "gpt-6.1-sol@codex"},
			InputKeyProviderSwitch: "codex",
		})
		env.SignalWorkflow(CapacityWakeSignalName, CapacityWakeSignal{})
	}, 10*time.Minute)
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	e.mu.Lock()
	defer e.mu.Unlock()
	require.Equal(t, []string{"codex"}, e.callRuntime["tagged#2"].ProviderPreference,
		"the existing spawn must resolve its original selector on the newly chosen provider, not its startup provider")
}
