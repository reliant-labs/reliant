// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// callLLMWorkflow returns a minimal workflow with a single call_llm node so a
// raw-output mock can be validated against CallLLMOutput.
func callLLMWorkflow() *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: "call_llm"},
		},
	}
}

func scenarioWithToolCallInput(input interface{}) *Scenario {
	return &Scenario{
		Name: "tool_call_input_shape",
		Events: []SimulatedEvent{{
			Node: "call_llm",
			Output: map[string]interface{}{
				"response_text": "calling a tool",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"id":    "call_0",
						"name":  "view",
						"input": input,
					},
				},
			},
		}},
	}
}

// TestValidateScenario_ToolCallInputMustBeString pins the shape contract that
// the fixture corpus previously violated corpus-wide.
//
// ToolCallMsg.input is `string` in proto, message.ToolCall.Input is a string,
// and every LLM driver assigns it the provider's JSON arguments string. A mock
// that writes a nested mapping there describes a response no driver can emit;
// the fast simulator tolerated it while the Temporal-backed lane failed inside
// save_message's convertToToolCalls. This test makes the bad shape a validation
// error at authoring time, on both lanes.
func TestValidateScenario_ToolCallInputMustBeString(t *testing.T) {
	t.Run("map input is rejected", func(t *testing.T) {
		scenario := scenarioWithToolCallInput(map[string]interface{}{
			"file_path": "/path/to/file.go",
		})

		result := ValidateScenario(scenario, callLLMWorkflow())

		if result.Valid {
			t.Fatalf("expected map-shaped tool_calls[].input to be invalid, got valid result")
		}
		joined := strings.Join(result.Errors, "; ")
		if !strings.Contains(joined, "tool_calls[0].input") {
			t.Errorf("expected an error naming tool_calls[0].input, got: %s", joined)
		}
		if !strings.Contains(joined, "expected string") {
			t.Errorf("expected an 'expected string' error, got: %s", joined)
		}
	})

	t.Run("JSON string input is accepted", func(t *testing.T) {
		scenario := scenarioWithToolCallInput(`{"file_path":"/path/to/file.go"}`)

		result := ValidateScenario(scenario, callLLMWorkflow())

		if !result.Valid {
			t.Fatalf("expected JSON-string tool_calls[].input to be valid, got errors: %v", result.Errors)
		}
	})
}

// TestValidateScenario_TypedEventOutputNotShapeChecked guards the other half of
// the contract. A typed event's `output:` is a shorthand payload the engine
// converts (for tool_result it is the tool's own result), NOT a raw activity
// output — so it must not be type-checked against the activity's proto.
// SimToolCall.Input is deliberately a map there, and buildLLMResponseOutput
// JSON-marshals it into the string the activity emits.
func TestValidateScenario_TypedEventOutputNotShapeChecked(t *testing.T) {
	scenario := &Scenario{
		Name: "typed_event",
		Events: []SimulatedEvent{{
			Node: "call_llm",
			Type: "llm_response",
			Text: "let me look",
			ToolCalls: []SimToolCall{{
				Name:  "view",
				Input: map[string]interface{}{"file_path": "/x.go"},
			}},
		}},
	}

	result := ValidateScenario(scenario, callLLMWorkflow())

	if !result.Valid {
		t.Fatalf("typed llm_response event should not be shape-checked, got errors: %v", result.Errors)
	}
}
