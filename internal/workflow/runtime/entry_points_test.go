// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindTriggeredNodes_WithEntryPoints(t *testing.T) {
	t.Run("workflow with entry field from JSON", func(t *testing.T) {
		wfJSON := `{
            "name": "test",
            "entry": ["start_node"],
            "nodes": [
                {"id": "start_node", "type": "test_action"}
            ],
            "edges": []
        }`

		wf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		t.Logf("Entry: %v", wf.GetEntry())
		t.Logf("HasExplicitEntry: %v", len(wf.GetEntry()) > 0)
		t.Logf("GetEntryNodes: %v", model.GetEntryNodes(wf))

		assert.True(t, len(wf.GetEntry()) > 0)
		assert.Equal(t, []string{"start_node"}, model.GetEntryNodes(wf))

		// Create state machine and verify
		sm := NewSimplifiedStateMachine("test-wf", wf)
		event := &core.WorkflowEvent{
			ID:         "start-test",
			WorkflowID: "test-wf",
			StepID:     "",
			Data:       map[string]interface{}{},
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, map[string]interface{}{}, map[string]interface{}{})
		require.NoError(t, err)
		require.Len(t, triggered, 1)
		assert.Equal(t, "start_node", triggered[0].Node.GetId())
	})
}

func TestLoopExecutor_InlineWithEntry(t *testing.T) {
	// Test that inline loop sub-workflows with entry fields work correctly.
	// Use a direct proto workflow to avoid v3 type casting issues.
	wfJSON := `{
		"name": "parent",
		"entry": ["test_loop"],
		"nodes": [
			{
				"id": "test_loop",
				"type": "loop",
				"while": "outputs.done == true || iter.iteration >= 2",
				"inline": {
					"entry": ["node_a"],
					"nodes": [
						{"id": "node_a", "type": "test_action"},
						{"id": "node_b", "type": "test_action_2"}
					],
					"edges": [
						{"from": "node_a", "default": "node_b"}
					],
					"outputs": {
						"done": "true"
					}
				}
			}
		],
		"edges": []
	}`
	parentWf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	// Verify parent has entry
	assert.True(t, len(parentWf.GetEntry()) > 0)
	assert.Equal(t, []string{"test_loop"}, model.GetEntryNodes(parentWf))

	// Find loop node and check inline workflow entry
	for _, n := range parentWf.GetNodes() {
		if n.GetId() == "test_loop" {
			loopCfg := n.GetLoop()
			require.NotNil(t, loopCfg, "node should have loop config")
			inlineWf := loopCfg.GetInline()
			require.NotNil(t, inlineWf, "loop should have inline workflow")
			assert.Equal(t, []string{"node_a"}, inlineWf.GetEntry())

			// Test state machine can find entry node
			sm := NewSimplifiedStateMachine("test", inlineWf)
			startEvent := &core.WorkflowEvent{
				ID:         "start",
				WorkflowID: "test",
				StepID:     "",
				Data:       map[string]interface{}{},
			}

			triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{startEvent}, map[string]interface{}{}, map[string]interface{}{})
			require.NoError(t, err)
			require.Len(t, triggered, 1)
			assert.Equal(t, "node_a", triggered[0].Node.GetId())
			t.Logf("Successfully triggered entry node: %s", triggered[0].Node.GetId())
			return
		}
	}
	t.Fatal("test_loop node not found")
}
