// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestScenarioStateIsCopiedAndConsumed(t *testing.T) {
	state := map[string]map[string]interface{}{
		"first": {
			"nested": map[string]interface{}{"value": "original"},
			"items":  []interface{}{map[string]interface{}{"value": "original"}},
		},
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		// Without the harness setter, production retains the original context.
		empty, unchanged := takeScenarioState(ctx)
		require.Nil(t, empty)
		require.Same(t, ctx, unchanged)
		for _, fixtures := range []map[string]map[string]interface{}{nil, {}} {
			require.Same(t, ctx, WithScenarioState(ctx, fixtures))
		}

		for i := 0; i < 2; i++ {
			seeded := WithScenarioState(ctx, state)
			outputs, consumed := takeScenarioState(seeded)
			require.Equal(t, "original", outputs["first"].(map[string]interface{})["nested"].(map[string]interface{})["value"])
			require.Equal(t, "original", outputs["first"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["value"])
			// A derived context, like an inline child's, cannot see the seed.
			child, _ := takeScenarioState(workflow.WithValue(consumed, struct{ unrelated bool }{true}, true))
			require.Nil(t, child)
			outputs["first"].(map[string]interface{})["nested"].(map[string]interface{})["value"] = "changed"
			outputs["first"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["value"] = "changed"
		}
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, "original", state["first"]["nested"].(map[string]interface{})["value"])
	require.Equal(t, "original", state["first"]["items"].([]interface{})[0].(map[string]interface{})["value"])
}
