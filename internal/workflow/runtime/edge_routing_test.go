// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEdgeRoutingWithConditionalAndDefault tests edge routing when a step has
// multiple cases: one with a condition and one default (no condition).
// This pattern was failing in the context-reducing-turn workflow.
func TestEdgeRoutingWithConditionalAndDefault(t *testing.T) {
	t.Parallel()
	// Workflow with conditional + default edge cases from a single step
	wfJSON := `{
		"name": "test-conditional-default",
		"nodes": [
			{"id": "step_a", "action": "TestAction"},
			{"id": "step_b", "action": "TestAction"},
			{"id": "conditional_target", "action": "TestAction"},
			{"id": "default_target", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "step_a"
			},
			{
				"from": "step_a",
				"default": "step_b"
			},
			{
				"from": "step_b",
				"cases": [
					{"to": "conditional_target", "condition": "nodes.step_b.some_value > 100", "label": "conditional"}
				],
				"default": "default_target"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("routes to conditional_target when condition is true", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"step_a": map[string]interface{}{"result": "done"},
			"step_b": map[string]interface{}{"some_value": 150}, // > 100
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "step_b",
			Data:         nodeOutputs["step_b"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "conditional_target", triggered[0].Node.GetId())
	})

	t.Run("routes to default_target when condition is false", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"step_a": map[string]interface{}{"result": "done"},
			"step_b": map[string]interface{}{"some_value": 50}, // < 100
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "step_b",
			Data:         nodeOutputs["step_b"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "default_target", triggered[0].Node.GetId())
	})

	t.Run("routes to default_target when value is exactly at boundary", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"step_a": map[string]interface{}{"result": "done"},
			"step_b": map[string]interface{}{"some_value": 100}, // == 100, not > 100
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "step_b",
			Data:         nodeOutputs["step_b"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "default_target", triggered[0].Node.GetId())
	})
}

// TestEdgeRoutingWithJSONSerializedNumbers tests that edge routing works correctly
// when numbers have been through JSON serialization (int -> float64).
// This is important because Temporal serializes activity outputs as JSON.
func TestEdgeRoutingWithJSONSerializedNumbers(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-json-numbers",
		"nodes": [
			{"id": "producer", "action": "TestAction"},
			{"id": "large_handler", "action": "TestAction"},
			{"id": "small_handler", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "producer"
			},
			{
				"from": "producer",
				"cases": [
					{"to": "large_handler", "condition": "nodes.producer.total_chars > 4000", "label": "large"}
				],
				"default": "small_handler"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	// Simulate JSON round-trip (what happens with Temporal)
	simulateJSONRoundTrip := func(data map[string]interface{}) map[string]interface{} {
		jsonBytes, _ := json.Marshal(data)
		var result map[string]interface{}
		json.Unmarshal(jsonBytes, &result)
		return result
	}

	t.Run("routes correctly with int values (no JSON round-trip)", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"total_chars": 5000, // int, > 4000
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "large_handler", triggered[0].Node.GetId())
	})

	t.Run("routes correctly with float64 values (after JSON round-trip)", func(t *testing.T) {
		// This simulates what happens after Temporal serialization
		originalOutput := map[string]interface{}{
			"total_chars": 5000,
		}
		jsonSerializedOutput := simulateJSONRoundTrip(originalOutput)

		// Verify the type changed to float64
		_, isFloat := jsonSerializedOutput["total_chars"].(float64)
		require.True(t, isFloat, "After JSON round-trip, int should become float64")

		nodeOutputs := map[string]interface{}{
			"producer": jsonSerializedOutput,
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         jsonSerializedOutput,
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "large_handler", triggered[0].Node.GetId(), "Should route to large_handler even with float64")
	})

	t.Run("routes to default with small float64 value", func(t *testing.T) {
		originalOutput := map[string]interface{}{
			"total_chars": 100,
		}
		jsonSerializedOutput := simulateJSONRoundTrip(originalOutput)

		nodeOutputs := map[string]interface{}{
			"producer": jsonSerializedOutput,
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         jsonSerializedOutput,
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "small_handler", triggered[0].Node.GetId())
	})
}

// TestEdgeRoutingWithMissingField tests behavior when the condition references
// a field that doesn't exist in the step output.
func TestEdgeRoutingWithMissingField(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-missing-field",
		"nodes": [
			{"id": "producer", "action": "TestAction"},
			{"id": "conditional_target", "action": "TestAction"},
			{"id": "default_target", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "producer"
			},
			{
				"from": "producer",
				"cases": [
					{"to": "conditional_target", "condition": "nodes.producer.nonexistent_field > 100", "label": "conditional"}
				],
				"default": "default_target"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("routes to default when field is missing", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"other_field": "value",
				// Note: nonexistent_field is not present
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.Error(t, err)
		assert.Nil(t, triggered)
	})
}

// TestEdgeRoutingWithNullValue tests behavior when the condition references
// a field that is explicitly null.
func TestEdgeRoutingWithNullValue(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-null-value",
		"nodes": [
			{"id": "producer", "action": "TestAction"},
			{"id": "has_value", "action": "TestAction"},
			{"id": "no_value", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "producer"
			},
			{
				"from": "producer",
				"cases": [
					{"to": "has_value", "condition": "nodes.producer.items != null && size(nodes.producer.items) > 0", "label": "has_items"}
				],
				"default": "no_value"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("routes to has_value when items array is non-empty", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"items": []interface{}{"item1", "item2"},
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "has_value", triggered[0].Node.GetId())
	})

	t.Run("routes to no_value when items is null", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"items": nil,
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "no_value", triggered[0].Node.GetId())
	})

	t.Run("routes to no_value when items is empty array", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"items": []interface{}{},
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "no_value", triggered[0].Node.GetId())
	})
}

// TestEdgeRoutingMultipleCases tests edge routing with more than two cases.
func TestEdgeRoutingMultipleCases(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-multiple-cases",
		"nodes": [
			{"id": "classifier", "action": "TestAction"},
			{"id": "critical", "action": "TestAction"},
			{"id": "warning", "action": "TestAction"},
			{"id": "info", "action": "TestAction"},
			{"id": "default", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "classifier"
			},
			{
				"from": "classifier",
				"cases": [
					{"to": "critical", "condition": "nodes.classifier.severity >= 90", "label": "critical"},
					{"to": "warning", "condition": "nodes.classifier.severity >= 50", "label": "warning"},
					{"to": "info", "condition": "nodes.classifier.severity >= 10", "label": "info"}
				],
				"default": "default"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	testCases := []struct {
		name           string
		severity       int
		expectedTarget string
	}{
		{"severity 95 -> critical", 95, "critical"},
		{"severity 90 -> critical (boundary)", 90, "critical"},
		{"severity 89 -> warning", 89, "warning"},
		{"severity 50 -> warning (boundary)", 50, "warning"},
		{"severity 49 -> info", 49, "info"},
		{"severity 10 -> info (boundary)", 10, "info"},
		{"severity 9 -> default", 9, "default"},
		{"severity 0 -> default", 0, "default"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeOutputs := map[string]interface{}{
				"classifier": map[string]interface{}{
					"severity": tc.severity,
				},
			}

			event := &core.WorkflowEvent{
				ID:           "test-event",
				WorkflowID:   "test-workflow",
				ChatID:       "test-chat",
				WorkflowName: wf.Name,
				StepID:       "classifier",
				Data:         nodeOutputs["classifier"].(map[string]interface{}),
			}

			triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
			require.NoError(t, err)

			require.Len(t, triggered, 1, "Should trigger exactly one node")
			assert.Equal(t, tc.expectedTarget, triggered[0].Node.GetId())
		})
	}
}

// TestEdgeRoutingContextReducingPattern tests the exact pattern that was used
// in context-reducing-turn workflow before simplification.
func TestEdgeRoutingContextReducingPattern(t *testing.T) {
	t.Parallel()
	// This is the pattern that was failing in runtime:
	// execute_tools -> filter_results (if total_result_chars > 4000)
	// execute_tools -> save_tool_results (default)
	wfJSON := `{
		"name": "context-reducing-pattern",
		"nodes": [
			{"id": "call_llm", "action": "CallLLM"},
			{"id": "execute_tools", "action": "ExecuteTools"},
			{"id": "filter_results", "action": "CallLLM"},
			{"id": "save_tool_results", "action": "SaveMessage"}
		],
		"edges": [
			{
				"from": "started",
				"default": "call_llm"
			},
			{
				"from": "call_llm",
				"cases": [
					{"to": "execute_tools", "condition": "nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0", "label": "exec_tools"}
				]
			},
			{
				"from": "execute_tools",
				"cases": [
					{"to": "filter_results", "condition": "nodes.execute_tools.total_result_chars > 4000", "label": "filter_large"}
				],
				"default": "save_tool_results"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	// Simulate JSON round-trip like Temporal does
	simulateJSONRoundTrip := func(data map[string]interface{}) map[string]interface{} {
		jsonBytes, _ := json.Marshal(data)
		var result map[string]interface{}
		json.Unmarshal(jsonBytes, &result)
		return result
	}

	t.Run("routes to save_tool_results when total_result_chars < 4000", func(t *testing.T) {
		// Simulate ExecuteToolsOutput structure
		executeToolsOutput := map[string]interface{}{
			"total_result_chars": 100,
			"thread_token_count": 1000,
			"tool_results": []interface{}{
				map[string]interface{}{"tool_call_id": "123", "content": "test"},
			},
			"message": map[string]interface{}{
				"role": "tool",
				"text": "",
			},
		}

		// Apply JSON round-trip to simulate Temporal serialization
		executeToolsOutput = simulateJSONRoundTrip(executeToolsOutput)

		nodeOutputs := map[string]interface{}{
			"call_llm": map[string]interface{}{
				"tool_calls": []interface{}{map[string]interface{}{"id": "123"}},
			},
			"execute_tools": executeToolsOutput,
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "execute_tools",
			Data:         executeToolsOutput,
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		t.Logf("Triggered nodes: %d", len(triggered))
		for _, tr := range triggered {
			t.Logf("  - %s", tr.Node.GetId())
		}

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "save_tool_results", triggered[0].Node.GetId(), "Should route to save_tool_results for small results")
	})

	t.Run("routes to filter_results when total_result_chars > 4000", func(t *testing.T) {
		executeToolsOutput := map[string]interface{}{
			"total_result_chars": 5000,
			"thread_token_count": 1000,
			"tool_results": []interface{}{
				map[string]interface{}{"tool_call_id": "123", "content": "large content..."},
			},
			"message": map[string]interface{}{
				"role": "tool",
				"text": "",
			},
		}

		// Apply JSON round-trip
		executeToolsOutput = simulateJSONRoundTrip(executeToolsOutput)

		nodeOutputs := map[string]interface{}{
			"call_llm": map[string]interface{}{
				"tool_calls": []interface{}{map[string]interface{}{"id": "123"}},
			},
			"execute_tools": executeToolsOutput,
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "execute_tools",
			Data:         executeToolsOutput,
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		t.Logf("Triggered nodes: %d", len(triggered))
		for _, tr := range triggered {
			t.Logf("  - %s", tr.Node.GetId())
		}

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "filter_results", triggered[0].Node.GetId(), "Should route to filter_results for large results")
	})

	t.Run("routes to save_tool_results when total_result_chars is exactly 4000", func(t *testing.T) {
		executeToolsOutput := map[string]interface{}{
			"total_result_chars": 4000, // exactly at boundary, not > 4000
			"thread_token_count": 1000,
			"tool_results": []interface{}{
				map[string]interface{}{"tool_call_id": "123", "content": "test"},
			},
			"message": map[string]interface{}{
				"role": "tool",
				"text": "",
			},
		}

		executeToolsOutput = simulateJSONRoundTrip(executeToolsOutput)

		nodeOutputs := map[string]interface{}{
			"call_llm": map[string]interface{}{
				"tool_calls": []interface{}{map[string]interface{}{"id": "123"}},
			},
			"execute_tools": executeToolsOutput,
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "execute_tools",
			Data:         executeToolsOutput,
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "save_tool_results", triggered[0].Node.GetId(), "Should route to save_tool_results when exactly at boundary")
	})
}

// TestEdgeRoutingWithNestedMapAccess tests conditions that access nested map fields.
func TestEdgeRoutingWithNestedMapAccess(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-nested-access",
		"nodes": [
			{"id": "producer", "action": "TestAction"},
			{"id": "success_handler", "action": "TestAction"},
			{"id": "error_handler", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "producer"
			},
			{
				"from": "producer",
				"cases": [
					{"to": "error_handler", "condition": "nodes.producer.message.is_error == true", "label": "error"}
				],
				"default": "success_handler"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("routes to error_handler when nested is_error is true", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"message": map[string]interface{}{
					"is_error": true,
					"text":     "Something went wrong",
				},
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "error_handler", triggered[0].Node.GetId())
	})

	t.Run("routes to success_handler when nested is_error is false", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"message": map[string]interface{}{
					"is_error": false,
					"text":     "Success",
				},
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1)
		assert.Equal(t, "success_handler", triggered[0].Node.GetId())
	})
}

// TestEdgeRoutingNoMatchingEdge tests behavior when no edge matches the event source.
func TestEdgeRoutingNoMatchingEdge(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-no-edge",
		"nodes": [
			{"id": "step_a", "action": "TestAction"},
			{"id": "step_b", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "step_a"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("returns empty when no edge from completed step", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"step_a": map[string]interface{}{"result": "done"},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "step_a", // No edge from step_a
			Data:         nodeOutputs["step_a"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		assert.Len(t, triggered, 0, "Should not trigger any nodes when no edge exists")
	})
}

// TestEdgeRoutingAllConditionsFail tests behavior when all conditional cases fail
// and there's no default case.
func TestEdgeRoutingAllConditionsFail(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-all-fail",
		"nodes": [
			{"id": "producer", "action": "TestAction"},
			{"id": "high", "action": "TestAction"},
			{"id": "medium", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "producer"
			},
			{
				"from": "producer",
				"cases": [
					{"to": "high", "condition": "nodes.producer.value > 100", "label": "high"},
					{"to": "medium", "condition": "nodes.producer.value > 50", "label": "medium"}
				]
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("returns empty when all conditions fail and no default", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"producer": map[string]interface{}{
				"value": 10, // < 50, so both conditions fail
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "producer",
			Data:         nodeOutputs["producer"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		assert.Len(t, triggered, 0, "Should not trigger any nodes when all conditions fail")
	})
}

// TestEdgeRoutingWithInputsNamespace tests that the inputs.* namespace is available
// for edge condition evaluation. This is important for workflows like thread-demo
// that use conditions like: has(inputs.message.role) ? inputs.message.role == "user" : true
func TestEdgeRoutingWithInputsNamespace(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-inputs-namespace",
		"nodes": [
			{"id": "save_user_message", "action": "SaveMessage"},
			{"id": "planner", "workflow": "builtin://agent"},
			{"id": "skip_planner", "action": "TestAction"}
		],
		"edges": [
			{
				"from": "started",
				"default": "save_user_message"
			},
			{
				"from": "save_user_message",
				"cases": [
					{"to": "planner", "condition": "has(inputs.message.role) ? inputs.message.role == 'user' : true", "label": "plan"}
				],
				"default": "skip_planner"
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("routes to planner when inputs.message.role is user", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"save_user_message": map[string]interface{}{"message_id": "test-id"},
		}

		workflowInputs := map[string]interface{}{
			"message": map[string]interface{}{
				"role": "user",
				"text": "hello",
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "save_user_message",
			Data:         nodeOutputs["save_user_message"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, workflowInputs)
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "planner", triggered[0].Node.GetId(), "Should route to planner when role is user")
	})

	t.Run("routes to skip_planner when inputs.message.role is assistant", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"save_user_message": map[string]interface{}{"message_id": "test-id"},
		}

		workflowInputs := map[string]interface{}{
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "hello",
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "save_user_message",
			Data:         nodeOutputs["save_user_message"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, workflowInputs)
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "skip_planner", triggered[0].Node.GetId(), "Should route to skip_planner when role is not user")
	})

	t.Run("routes to planner when inputs.message has no role (defaults to true)", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"save_user_message": map[string]interface{}{"message_id": "test-id"},
		}

		// Message without role field
		workflowInputs := map[string]interface{}{
			"message": map[string]interface{}{
				"text": "hello",
			},
		}

		event := &core.WorkflowEvent{
			ID:           "test-event",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "save_user_message",
			Data:         nodeOutputs["save_user_message"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, workflowInputs)
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "planner", triggered[0].Node.GetId(), "Should route to planner when no role (defaults to true)")
	})
}
