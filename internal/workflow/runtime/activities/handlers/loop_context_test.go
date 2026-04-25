// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test: Activity Input Structs Have Loop Context Fields
// =============================================================================
//
// These tests verify that when Temporal deserializes JSON inputs to typed
// activity input structs, the loop_node_id and loop_iteration fields are
// correctly populated.
//
// BACKGROUND:
// When the workflow executes an activity, it serializes the input as JSON.
// The activity receives this JSON and deserializes it into the typed struct.
// If the struct doesn't have the loop context fields, they get silently dropped.
// =============================================================================

func TestExecuteRunStepInput_LoopContextFields(t *testing.T) {
	t.Run("struct has loop context fields", func(t *testing.T) {
		// Create input with loop context
		input := ExecuteRunStepInput{
			WorkflowID:    "test-workflow",
			ChatID:        "test-chat",
			StepID:        "test-step",
			Command:       "echo test",
			LoopNodeID:    "test_loop",
			LoopIteration: 5,
		}

		// Verify fields are accessible
		assert.Equal(t, "test_loop", input.LoopNodeID)
		assert.Equal(t, 5, input.LoopIteration)
	})

	t.Run("JSON roundtrip preserves loop context", func(t *testing.T) {
		original := ExecuteRunStepInput{
			WorkflowID:    "workflow-123",
			ChatID:        "chat-456",
			StepID:        "step-789",
			Command:       "echo hello",
			LoopNodeID:    "my_loop",
			LoopIteration: 10,
		}

		// Serialize to JSON (what Temporal does internally)
		jsonData, err := json.Marshal(original)
		require.NoError(t, err)

		t.Logf("Serialized: %s", string(jsonData))

		// Verify JSON contains loop fields
		var jsonMap map[string]interface{}
		err = json.Unmarshal(jsonData, &jsonMap)
		require.NoError(t, err)

		assert.Equal(t, "my_loop", jsonMap["loop_node_id"])
		assert.Equal(t, float64(10), jsonMap["loop_iteration"])

		// Deserialize back to struct (what Temporal does in activity)
		var restored ExecuteRunStepInput
		err = json.Unmarshal(jsonData, &restored)
		require.NoError(t, err)

		// Verify all fields preserved
		assert.Equal(t, original.LoopNodeID, restored.LoopNodeID)
		assert.Equal(t, original.LoopIteration, restored.LoopIteration)

		t.Logf("✓ JSON roundtrip preserved loop_node_id=%s, loop_iteration=%d",
			restored.LoopNodeID, restored.LoopIteration)
	})

	t.Run("map to struct conversion preserves loop context", func(t *testing.T) {
		// Simulate workflow -> activity: map serialized to JSON, then deserialized to struct
		inputMap := map[string]interface{}{
			"workflow_id":    "workflow-abc",
			"chat_id":        "chat-def",
			"step_id":        "step-ghi",
			"command":        "ls -la",
			"loop_node_id":   "map_test_loop",
			"loop_iteration": 7,
		}

		// Serialize map to JSON
		jsonData, err := json.Marshal(inputMap)
		require.NoError(t, err)

		// Deserialize to typed struct (what Temporal does)
		var input ExecuteRunStepInput
		err = json.Unmarshal(jsonData, &input)
		require.NoError(t, err)

		assert.Equal(t, "map_test_loop", input.LoopNodeID)
		assert.Equal(t, 7, input.LoopIteration)

		t.Logf("✓ Map-to-struct preserved loop_node_id=%s, loop_iteration=%d",
			input.LoopNodeID, input.LoopIteration)
	})

	t.Run("omitempty behavior for empty loop context", func(t *testing.T) {
		// When not in a loop, loop fields should be empty/zero
		input := ExecuteRunStepInput{
			WorkflowID: "workflow",
			ChatID:     "chat",
			StepID:     "step",
			Command:    "echo test",
			// No loop context set
		}

		jsonData, err := json.Marshal(input)
		require.NoError(t, err)

		var jsonMap map[string]interface{}
		err = json.Unmarshal(jsonData, &jsonMap)
		require.NoError(t, err)

		// loop_node_id has omitempty, so it should not appear when empty
		_, hasLoopNodeID := jsonMap["loop_node_id"]
		assert.False(t, hasLoopNodeID, "Empty loop_node_id should be omitted from JSON")

		// loop_iteration does NOT have omitempty (per struct definition), so it appears
		// This is intentional - iteration 0 is valid
		_, hasLoopIteration := jsonMap["loop_iteration"]
		assert.True(t, hasLoopIteration, "loop_iteration should appear (no omitempty)")

		t.Logf("✓ omitempty behavior correct for empty loop context")
	})
}

func TestSaveMessageInput_LoopContextFields(t *testing.T) {
	t.Run("struct has loop context fields", func(t *testing.T) {
		input := SaveMessageInput{
			ChatID:        "test-chat",
			Thread:        "test-thread",
			WorkflowID:    "test-workflow",
			LoopNodeID:    "save_loop",
			LoopIteration: 3,
		}

		assert.Equal(t, "save_loop", input.LoopNodeID)
		assert.Equal(t, 3, input.LoopIteration)
	})

	t.Run("JSON roundtrip preserves loop context via ActivityInput", func(t *testing.T) {
		original := ActivityInput{
			Runtime: types.RuntimeContext{
				ChatID:        "chat-1",
				Thread:        "thread-1",
				WorkflowID:    "workflow-1",
				LoopNodeID:    "save_loop_test",
				LoopIteration: 8,
			},
			Node: &reliantv1.Node{
				Type: "save_message",
				Args: &reliantv1.Node_SaveMessageNode{
					SaveMessageNode: &reliantv1.SaveMessageNodeArgs{
						ResolvedRole:    "assistant",
						ResolvedContent: "Hello",
					},
				},
			},
		}

		jsonData, err := json.Marshal(original)
		require.NoError(t, err)

		var restored ActivityInput
		err = json.Unmarshal(jsonData, &restored)
		require.NoError(t, err)

		assert.Equal(t, "save_loop_test", restored.Runtime.LoopNodeID)
		assert.Equal(t, 8, restored.Runtime.LoopIteration)

		t.Logf("✓ SaveMessage ActivityInput roundtrip preserved loop context")
	})
}

func TestCallLLMActivityInput_LoopContextFields(t *testing.T) {
	t.Run("struct has loop context fields in Runtime", func(t *testing.T) {
		// Loop context is in RuntimeContext
		input := ActivityInput{
			Runtime: types.RuntimeContext{
				ChatID:        "test-chat",
				Thread:        "test-thread",
				LoopNodeID:    "llm_loop",
				LoopIteration: 7,
			},
			Node: &reliantv1.Node{
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{},
				},
			},
		}

		assert.Equal(t, "llm_loop", input.Runtime.LoopNodeID)
		assert.Equal(t, 7, input.Runtime.LoopIteration)
	})

	t.Run("JSON roundtrip preserves loop context", func(t *testing.T) {
		original := ActivityInput{
			Runtime: types.RuntimeContext{
				ChatID:        "chat-1",
				Thread:        "thread-1",
				LoopNodeID:    "llm_loop_test",
				LoopIteration: 12,
			},
			Node: &reliantv1.Node{
				Type: "call_llm",
				Args: &reliantv1.Node_CallLlm{
					CallLlm: &reliantv1.CallLLMArgs{},
				},
			},
		}

		jsonData, err := json.Marshal(original)
		require.NoError(t, err)

		var restored ActivityInput
		err = json.Unmarshal(jsonData, &restored)
		require.NoError(t, err)

		assert.Equal(t, "llm_loop_test", restored.Runtime.LoopNodeID)
		assert.Equal(t, 12, restored.Runtime.LoopIteration)

		t.Logf("✓ CallLLM ActivityInput roundtrip preserved loop context")
	})

	t.Run("proto node JSON with call_llm oneof is deserialized correctly", func(t *testing.T) {
		// This tests the correct proto format where the oneof variant name
		// is used as the JSON key (not the legacy "args" format).
		protoJSON := `{
			"runtime": {"chat_id": "chat-legacy", "thread": "thread-legacy"},
			"node": {
				"type": "call_llm",
				"call_llm": {
					"model": {"literal": {"id": "gpt-4.1"}},
					"temperature": {"literal": 0.4},
					"tools_config": {"filter": {"literal": {"values": ["view", "edit"]}}}
				}
			}
		}`

		var input ActivityInput
		err := json.Unmarshal([]byte(protoJSON), &input)
		require.NoError(t, err)
		require.NotNil(t, input.Node)
		require.NotNil(t, input.Node.GetCallLlm())
		require.NotNil(t, input.Node.GetCallLlm().GetModel())
		require.NotNil(t, input.Node.GetCallLlm().GetModel().GetLiteral())
		require.Equal(t, "gpt-4.1", input.Node.GetCallLlm().GetModel().GetLiteral().GetId())
		require.InDelta(t, 0.4, input.Node.GetCallLlm().GetTemperature().GetLiteral(), 0.0001)
		require.Equal(t, []string{"view", "edit"}, input.Node.GetCallLlm().GetToolsConfig().GetFilter().GetLiteral().GetValues())
	})
}

func TestExecuteToolsInput_LoopContextFields(t *testing.T) {
	t.Run("struct has loop context fields", func(t *testing.T) {
		input := ExecuteToolsInput{
			ChatID:        "test-chat",
			Thread:        "test-thread",
			LoopNodeID:    "tools_loop",
			LoopIteration: 2,
		}

		assert.Equal(t, "tools_loop", input.LoopNodeID)
		assert.Equal(t, 2, input.LoopIteration)
	})

	t.Run("JSON roundtrip preserves loop context via ActivityInput", func(t *testing.T) {
		original := ActivityInput{
			Runtime: types.RuntimeContext{
				ChatID:        "chat-1",
				Thread:        "thread-1",
				LoopNodeID:    "tools_loop_test",
				LoopIteration: 4,
			},
			Node: &reliantv1.Node{
				Type: "execute_tools",
				Args: &reliantv1.Node_ExecuteTools{
					ExecuteTools: &reliantv1.ExecuteToolsArgs{},
				},
			},
		}

		jsonData, err := json.Marshal(original)
		require.NoError(t, err)

		var restored ActivityInput
		err = json.Unmarshal(jsonData, &restored)
		require.NoError(t, err)

		assert.Equal(t, "tools_loop_test", restored.Runtime.LoopNodeID)
		assert.Equal(t, 4, restored.Runtime.LoopIteration)

		t.Logf("✓ ExecuteTools ActivityInput roundtrip preserved loop context")
	})
}

// =============================================================================
// Regression Test: Temporal Deserialization
// =============================================================================
// This test ensures that when Temporal deserializes JSON containing loop context
// into the typed struct, the values are correctly populated.
//
// This caught a bug where the struct fields were missing, causing Temporal to
// silently drop the loop_node_id and loop_iteration fields.

func TestTemporalDeserializationRegression(t *testing.T) {
	t.Run("ExecuteRunStep: JSON with loop context deserializes correctly", func(t *testing.T) {
		// This is what Temporal would receive as JSON from the workflow
		temporalJSON := `{
			"workflow_id": "wf-123",
			"chat_id": "chat-456",
			"step_id": "step-789",
			"command": "echo test",
			"loop_node_id": "regression_loop",
			"loop_iteration": 99
		}`

		var input ExecuteRunStepInput
		err := json.Unmarshal([]byte(temporalJSON), &input)
		require.NoError(t, err)

		// THIS IS THE CRITICAL CHECK:
		// If the struct doesn't have these fields, they would be "" and 0
		assert.Equal(t, "regression_loop", input.LoopNodeID,
			"REGRESSION: loop_node_id not deserialized - check struct field definition")
		assert.Equal(t, 99, input.LoopIteration,
			"REGRESSION: loop_iteration not deserialized - check struct field definition")

		t.Logf("✓ Temporal deserialization regression test passed")
	})

	t.Run("SaveMessage: JSON with loop context deserializes correctly", func(t *testing.T) {
		// ActivityInput JSON with loop context in runtime
		temporalJSON := `{
			"runtime": {
				"chat_id": "chat-123",
				"thread": "thread-456",
				"workflow_id": "wf-789",
				"loop_node_id": "save_regression",
				"loop_iteration": 88
			},
			"node": {}
		}`

		var input ActivityInput
		err := json.Unmarshal([]byte(temporalJSON), &input)
		require.NoError(t, err)

		assert.Equal(t, "save_regression", input.Runtime.LoopNodeID,
			"REGRESSION: SaveMessage loop_node_id not deserialized")
		assert.Equal(t, 88, input.Runtime.LoopIteration,
			"REGRESSION: SaveMessage loop_iteration not deserialized")

		t.Logf("✓ SaveMessage deserialization regression test passed")
	})

	t.Run("CallLLM: JSON with loop context deserializes correctly", func(t *testing.T) {
		// Loop context is in the runtime field within ActivityInput
		temporalJSON := `{
			"runtime": {
				"chat_id": "chat-123",
				"thread": "thread-456",
				"loop_node_id": "llm_regression",
				"loop_iteration": 77
			},
			"node": {}
		}`

		var input ActivityInput
		err := json.Unmarshal([]byte(temporalJSON), &input)
		require.NoError(t, err)

		assert.Equal(t, "llm_regression", input.Runtime.LoopNodeID,
			"REGRESSION: CallLLM RuntimeContext loop_node_id not deserialized")
		assert.Equal(t, 77, input.Runtime.LoopIteration,
			"REGRESSION: CallLLM RuntimeContext loop_iteration not deserialized")

		t.Logf("✓ CallLLM deserialization regression test passed")
	})

	t.Run("ExecuteTools: JSON with loop context deserializes correctly", func(t *testing.T) {
		// ActivityInput JSON with loop context in runtime
		temporalJSON := `{
			"runtime": {
				"chat_id": "chat-123",
				"thread": "thread-456",
				"loop_node_id": "tools_regression",
				"loop_iteration": 66
			},
			"node": {}
		}`

		var input ActivityInput
		err := json.Unmarshal([]byte(temporalJSON), &input)
		require.NoError(t, err)

		assert.Equal(t, "tools_regression", input.Runtime.LoopNodeID,
			"REGRESSION: ExecuteTools loop_node_id not deserialized")
		assert.Equal(t, 66, input.Runtime.LoopIteration,
			"REGRESSION: ExecuteTools loop_iteration not deserialized")

		t.Logf("✓ ExecuteTools deserialization regression test passed")
	})

}
