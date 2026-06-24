// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParseWorkflowYAML is a test helper to parse workflow YAML.
func ParseWorkflowYAML(data []byte) (*reliantv1.Workflow, error) {
	return v2.ParseWorkflowProtoBytes(data)
}

// Simple workflow for validation tests
const validationTestWorkflow = `
name: test-workflow
apiVersion: "1.0"
entry: [call_llm]
inputs:
  max_turns:
    type: integer
    default: 10
  mode:
    type: enum
    enum: [auto, manual, plan]
    default: auto
  model:
    type: model
    default:
      id: claude-4.5-sonnet
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: "{{inputs.model}}"
  - id: execute_tools
    type: execute_tools
  - id: save_result
    type: save_message
    args:
      role: "assistant"
      content: "Done"
edges:
  - from: call_llm
    default: execute_tools
  - from: execute_tools
    default: save_result
`

// Workflow with inline loop
const validationLoopWorkflow = `
name: test-loop
apiVersion: "1.0"
entry: [agent_loop]
inputs:
  agent:
    type: group
    inputs:
      max_turns:
        type: integer
        default: 10
      mode:
        type: enum
        enum: [auto, manual]
        default: auto
nodes:
  - id: agent_loop
    type: loop
    while: "outputs.tool_calls != null && size(outputs.tool_calls) > 0"
    inline:
      entry: [call_llm]
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
      nodes:
        - id: call_llm
          type: call_llm
          args:
            model: mock
        - id: execute_tools
          type: execute_tools
        - id: save_result
          type: save_message
          args:
            role: "assistant"
            content: "Done"
      edges:
        - from: call_llm
          default: execute_tools
        - from: execute_tools
          default: save_result
  - id: final_save
    type: save_message
    args:
      role: "assistant"
      content: "Complete"
edges:
  - from: agent_loop
    default: final_save
`

// Workflow with nested loops
const validationNestedLoopWorkflow = `
name: test-nested
apiVersion: "1.0"
entry: [outer_loop]
nodes:
  - id: outer_loop
    type: loop
    while: "true"
    inline:
      entry: [inner_loop]
      nodes:
        - id: inner_loop
          type: loop
          while: "true"
          inline:
            entry: [deep_node]
            nodes:
              - id: deep_node
                type: call_llm
                args:
                  model: mock
            edges: []
      edges: []
edges: []
`

func TestValidateScenario_ValidNodes(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "valid_scenario",
		Events: []SimulatedEvent{
			{Node: "call_llm", Output: map[string]interface{}{"response_text": "Hi"}},
			{Node: "execute_tools", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{
			Outcome: OutcomeCompleted,
			Reached: []string{"call_llm", "execute_tools", "save_result"},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.True(t, result.Valid, "Expected valid scenario, got errors: %v", result.Errors)
	assert.Empty(t, result.Errors)
}

func TestValidateScenario_InvalidEventNode(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "invalid_node",
		Events: []SimulatedEvent{
			{Node: "nonexistent_node", Output: map[string]interface{}{}},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "unknown node \"nonexistent_node\"")
}

func TestValidateScenario_InvalidReachedNode(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "invalid_reached",
		Events: []SimulatedEvent{},
		Expect: &Expectation{
			Reached: []string{"call_llm", "fake_node"},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "expect.reached: unknown node \"fake_node\"")
}

func TestValidateScenario_InvalidNotReachedNode(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "invalid_not_reached",
		Events: []SimulatedEvent{},
		Expect: &Expectation{
			NotReached: []string{"imaginary_node"},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[0], "expect.not_reached: unknown node \"imaginary_node\"")
}

func TestValidateScenario_InlineLoopNodes(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationLoopWorkflow))
	require.NoError(t, err)

	t.Run("valid qualified node ID", func(t *testing.T) {
		scenario := &Scenario{
			Name: "valid_loop_node",
			Events: []SimulatedEvent{
				{Node: "agent_loop.call_llm", Output: map[string]interface{}{}},
				{Node: "agent_loop.execute_tools", Output: map[string]interface{}{}},
			},
			Expect: &Expectation{
				Reached: []string{"agent_loop", "agent_loop.call_llm", "final_save"},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
	})

	t.Run("invalid qualified node ID", func(t *testing.T) {
		scenario := &Scenario{
			Name: "invalid_loop_node",
			Events: []SimulatedEvent{
				{Node: "agent_loop.nonexistent", Output: map[string]interface{}{}},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors[0], "unknown node \"agent_loop.nonexistent\"")
	})
}

func TestValidateScenario_NestedLoopNodes(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationNestedLoopWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "nested_loop",
		Events: []SimulatedEvent{
			{Node: "outer_loop.inner_loop.deep_node", Output: map[string]interface{}{}},
		},
		Expect: &Expectation{
			Reached: []string{"outer_loop", "outer_loop.inner_loop", "outer_loop.inner_loop.deep_node"},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.True(t, result.Valid, "Errors: %v", result.Errors)
}

func TestValidateScenario_InvalidStartAt(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:    "invalid_start_at",
		StartAt: "nonexistent_start",
		Events:  []SimulatedEvent{},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[0], "start_at: unknown node")
}

func TestValidateScenario_InvalidStateNode(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "invalid_state",
		Events: []SimulatedEvent{},
		State: map[string]map[string]interface{}{
			"fake_node": {"output": "value"},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[0], "state: unknown node")
}

func TestValidateScenario_ValidInputs(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "valid_inputs",
		Events: []SimulatedEvent{},
		Inputs: map[string]interface{}{
			"max_turns": 5,
			"mode":      "manual",
			"model":     map[string]interface{}{"id": "gpt-4"},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.True(t, result.Valid, "Errors: %v", result.Errors)
	assert.Empty(t, result.Errors)
}

func TestValidateScenario_InvalidInputType(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "invalid_input_type",
		Events: []SimulatedEvent{},
		Inputs: map[string]interface{}{
			"max_turns": "not_an_integer", // Should be integer
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[0], "expected integer")
}

func TestValidateScenario_InvalidEnumValue(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "invalid_enum",
		Events: []SimulatedEvent{},
		Inputs: map[string]interface{}{
			"mode": "invalid_mode", // Not in enum: [auto, manual, plan]
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[0], "is not in enum list")
}

func TestValidateScenario_UndefinedInput(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "undefined_input",
		Events: []SimulatedEvent{},
		Inputs: map[string]interface{}{
			"undefined_param": "value",
		},
	}

	result := ValidateScenario(scenario, workflow)
	// Undefined inputs are warnings, not errors
	assert.True(t, result.Valid)
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "not defined in workflow schema")
}

func TestValidateScenario_GroupedInputs(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationLoopWorkflow))
	require.NoError(t, err)

	t.Run("valid grouped input", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "valid_grouped",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"agent": map[string]interface{}{
					"max_turns": 5,
					"mode":      "manual",
				},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
	})

	t.Run("invalid grouped input type", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "invalid_grouped",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"agent": map[string]interface{}{
					"max_turns": "not_an_int",
				},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors[0], "agent.max_turns")
	})
}

func TestValidateScenario_OutputFieldWarnings(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "unknown_output_field",
		Events: []SimulatedEvent{
			{
				Node: "call_llm",
				Output: map[string]interface{}{
					"response_text":   "Hi",    // Valid field
					"unknown_field":   "value", // Unknown field
					"another_unknown": 123,     // Another unknown field
				},
			},
		},
	}

	result := ValidateScenario(scenario, workflow)
	// Unknown output fields are warnings, not errors
	assert.True(t, result.Valid, "Should be valid, unknown fields are warnings")
	assert.GreaterOrEqual(t, len(result.Warnings), 1, "Should have at least one warning")

	// Check that warnings mention the unknown fields
	warningText := result.String()
	assert.Contains(t, warningText, "unknown_field")
}

func TestValidateScenario_NodeOutputsValidation(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name:   "invalid_node_outputs",
		Events: []SimulatedEvent{},
		Expect: &Expectation{
			NodeOutputs: map[string]map[string]interface{}{
				"fake_node": {"field": "value"},
			},
		},
	}

	result := ValidateScenario(scenario, workflow)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[0], "expect.node_outputs: unknown node")
}

func TestValidationResult_String(t *testing.T) {
	result := &ValidationResult{
		Valid:    false,
		Errors:   []string{"error 1", "error 2"},
		Warnings: []string{"warning 1"},
	}

	str := result.String()
	assert.Contains(t, str, "Errors:")
	assert.Contains(t, str, "error 1")
	assert.Contains(t, str, "error 2")
	assert.Contains(t, str, "Warnings:")
	assert.Contains(t, str, "warning 1")
}

func TestValidateScenarioNodes_Convenience(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationTestWorkflow))
	require.NoError(t, err)

	scenario := &Scenario{
		Name: "invalid_nodes",
		Events: []SimulatedEvent{
			{Node: "fake1", Output: map[string]interface{}{}},
			{Node: "fake2", Output: map[string]interface{}{}},
		},
	}

	errors := ValidateScenarioNodes(scenario, workflow)
	assert.Len(t, errors, 2)
}

// Workflow with multi-select enum for testing
const validationMultiEnumWorkflow = `
name: test-multi-enum
apiVersion: "1.0"
entry: [call_llm]
inputs:
  steps:
    type: enum
    enum: [plan, implement, lint, test, build]
    multi: true
    default:
      - plan
      - implement
    description: Steps to include in the workflow
  mode:
    type: enum
    enum: [auto, manual]
    default: auto
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: mock
edges: []
`

func TestValidateScenario_MultiEnumInput(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationMultiEnumWorkflow))
	require.NoError(t, err)

	t.Run("valid multi-enum array with []interface{}", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "multi_enum_valid",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"steps": []interface{}{"implement", "lint", "test"},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
		assert.Empty(t, result.Errors)
	})

	t.Run("valid multi-enum array with []string", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "multi_enum_valid_string",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"steps": []string{"implement", "lint"},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
		assert.Empty(t, result.Errors)
	})

	t.Run("invalid multi-enum value not in allowed list", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "multi_enum_invalid_value",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"steps": []interface{}{"implement", "invalid_step"},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors[0], "invalid_step")
		assert.Contains(t, result.Errors[0], "is not in enum list")
	})

	t.Run("invalid multi-enum with non-string element", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "multi_enum_non_string",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"steps": []interface{}{"implement", 123}, // 123 is not a string
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors[0], "expected array of strings")
	})

	t.Run("invalid multi-enum with string instead of array", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "multi_enum_wrong_type",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"steps": "implement", // Should be array, not string
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors[0], "expected array for multi-select enum")
	})

	t.Run("single-select enum still works with string", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "single_enum_valid",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"mode": "manual",
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
		assert.Empty(t, result.Errors)
	})

	t.Run("single-select enum rejects array", func(t *testing.T) {
		scenario := &Scenario{
			Name:   "single_enum_rejects_array",
			Events: []SimulatedEvent{},
			Inputs: map[string]interface{}{
				"mode": []interface{}{"auto", "manual"}, // Should be string, not array
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors[0], "expected string for enum")
	})
}

func TestValidateScenario_MixedMockingConflict(t *testing.T) {
	workflow, err := ParseWorkflowYAML([]byte(validationLoopWorkflow))
	require.NoError(t, err)

	t.Run("conflict: black-box and internal events for same node", func(t *testing.T) {
		scenario := &Scenario{
			Name: "mixed_mocking",
			Events: []SimulatedEvent{
				// Black-box event for agent_loop
				{Node: "agent_loop", Output: map[string]interface{}{"result": "done"}},
				// Internal event for agent_loop's inner node
				{Node: "agent_loop.call_llm", Output: map[string]interface{}{"response_text": "hi"}},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.False(t, result.Valid, "Should be invalid due to mixed mocking")
		assert.Len(t, result.Errors, 1)
		assert.Contains(t, result.Errors[0], "agent_loop")
		assert.Contains(t, result.Errors[0], "black-box")
		assert.Contains(t, result.Errors[0], "internal")
	})

	t.Run("valid: only black-box events", func(t *testing.T) {
		scenario := &Scenario{
			Name: "black_box_only",
			Events: []SimulatedEvent{
				{Node: "agent_loop", Output: map[string]interface{}{"result": "done"}},
				{Node: "final_save", Output: map[string]interface{}{}},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
	})

	t.Run("valid: only internal events", func(t *testing.T) {
		scenario := &Scenario{
			Name: "internal_only",
			Events: []SimulatedEvent{
				{Node: "agent_loop.call_llm", Output: map[string]interface{}{"response_text": "hi"}},
				{Node: "agent_loop.execute_tools", Output: map[string]interface{}{}},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
	})

	t.Run("valid: different nodes with different approaches", func(t *testing.T) {
		scenario := &Scenario{
			Name: "different_nodes",
			Events: []SimulatedEvent{
				// Internal events for agent_loop
				{Node: "agent_loop.call_llm", Output: map[string]interface{}{"response_text": "hi"}},
				// Black-box event for final_save (different node, no conflict)
				{Node: "final_save", Output: map[string]interface{}{}},
			},
		}

		result := ValidateScenario(scenario, workflow)
		assert.True(t, result.Valid, "Errors: %v", result.Errors)
	})
}
