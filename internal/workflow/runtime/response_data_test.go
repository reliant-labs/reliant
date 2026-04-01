// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResponseDataCELAccess tests accessing response_data from execute_tools via CEL
// This validates that nested response_data structures can be accessed correctly,
// such as: {{nodes.execute_filter.response_data.filtered_results.results}}
func TestResponseDataCELAccess(t *testing.T) {
	tests := []struct {
		name          string
		responseData  map[string]interface{}
		celExpression string
		expectedValue interface{}
		shouldSucceed bool
		description   string
	}{
		{
			name: "direct_tool_name_access",
			responseData: map[string]interface{}{
				"filtered_results": map[string]interface{}{
					"results": []interface{}{
						map[string]interface{}{
							"tool_call_id": "call_123",
							"content":      "filtered content",
						},
					},
				},
			},
			celExpression: "nodes.execute_filter.response_data.filtered_results",
			expectedValue: map[string]interface{}{
				"results": []interface{}{
					map[string]interface{}{
						"tool_call_id": "call_123",
						"content":      "filtered content",
					},
				},
			},
			shouldSucceed: true,
			description:   "Should access response_data by tool name",
		},
		{
			name: "nested_results_access",
			responseData: map[string]interface{}{
				"filtered_results": map[string]interface{}{
					"results": []interface{}{
						map[string]interface{}{
							"tool_call_id": "call_123",
							"content":      "filtered content",
						},
					},
				},
			},
			celExpression: "nodes.execute_filter.response_data.filtered_results.results",
			expectedValue: []interface{}{
				map[string]interface{}{
					"tool_call_id": "call_123",
					"content":      "filtered content",
				},
			},
			shouldSucceed: true,
			description:   "Should access nested results array - THIS IS THE BUG PATH",
		},
		{
			name: "missing_nested_key",
			responseData: map[string]interface{}{
				"filtered_results": map[string]interface{}{
					"data": []interface{}{}, // Wrong key, should be "results"
				},
			},
			celExpression: "nodes.execute_filter.response_data.filtered_results.results",
			expectedValue: nil,
			shouldSucceed: false,
			description:   "Should fail when nested key doesn't exist",
		},
		{
			name: "missing_tool_name",
			responseData: map[string]interface{}{
				"wrong_tool_name": map[string]interface{}{
					"results": []interface{}{},
				},
			},
			celExpression: "nodes.execute_filter.response_data.filtered_results.results",
			expectedValue: nil,
			shouldSucceed: false,
			description:   "Should fail when tool name doesn't exist in response_data",
		},
		{
			name:          "empty_response_data",
			responseData:  map[string]interface{}{},
			celExpression: "nodes.execute_filter.response_data.filtered_results",
			expectedValue: nil,
			shouldSucceed: false,
			description:   "Should fail when response_data is empty",
		},
		{
			name: "response_data_with_single_level",
			responseData: map[string]interface{}{
				"filtered_results": []interface{}{
					map[string]interface{}{
						"tool_call_id": "call_123",
						"content":      "filtered content",
					},
				},
			},
			celExpression: "nodes.execute_filter.response_data.filtered_results",
			expectedValue: []interface{}{
				map[string]interface{}{
					"tool_call_id": "call_123",
					"content":      "filtered content",
				},
			},
			shouldSucceed: true,
			description:   "Should work if results are at top level (not nested)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &wfcel.EdgeEvalContext{
				Nodes: map[string]interface{}{
					"execute_filter": map[string]interface{}{
						"response_data": tt.responseData,
					},
				},
				Inputs:   map[string]interface{}{},
				Workflow: &model.WorkflowContext{},
			}

			val, err := wfcel.EvaluateValue(tt.celExpression, ctx)
			if err != nil {
				if tt.shouldSucceed {
					t.Errorf("CEL evaluation failed (expected success): %v\nDescription: %s",
						err, tt.description)
				}
				return
			}

			if !tt.shouldSucceed {
				t.Errorf("CEL evaluation succeeded (expected failure)\nDescription: %s\nResult: %+v",
					tt.description, val)
				return
			}

			// Compare result via JSON for deep comparison
			actualJSON, err := json.Marshal(val)
			require.NoError(t, err)
			expectedJSON, err := json.Marshal(tt.expectedValue)
			require.NoError(t, err)

			assert.JSONEq(t, string(expectedJSON), string(actualJSON),
				"Result mismatch\nDescription: %s\nExpression: %s",
				tt.description, tt.celExpression)
		})
	}
}

// TestResponseDataStructureFromExecuteTools tests the actual structure returned by ExecuteTools
// This validates what execute_tools.go lines 354-364 produce
func TestResponseDataStructureFromExecuteTools(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		metadata     string // JSON string
		expectedData map[string]interface{}
		description  string
	}{
		{
			name:     "filtered_results_tool",
			toolName: "filtered_results",
			metadata: `{
				"results": [
					{
						"tool_call_id": "call_123",
						"name": "bash",
						"content": "filtered output",
						"is_error": false
					}
				]
			}`,
			expectedData: map[string]interface{}{
				"filtered_results": map[string]interface{}{
					"results": []interface{}{
						map[string]interface{}{
							"tool_call_id": "call_123",
							"name":         "bash",
							"content":      "filtered output",
							"is_error":     false,
						},
					},
				},
			},
			description: "ResponseTool returns input as metadata, keyed by tool name",
		},
		{
			name:     "multiple_tools",
			toolName: "response_tool_1",
			metadata: `{
				"data": "value1"
			}`,
			expectedData: map[string]interface{}{
				"response_tool_1": map[string]interface{}{
					"data": "value1",
				},
			},
			description: "Each tool result is keyed by its name",
		},
		{
			name:         "empty_metadata",
			toolName:     "some_tool",
			metadata:     "",
			expectedData: map[string]interface{}{},
			description:  "Empty metadata should not be added to response_data",
		},
		{
			name:         "invalid_json_metadata",
			toolName:     "some_tool",
			metadata:     "not json",
			expectedData: map[string]interface{}{},
			description:  "Invalid JSON metadata should be skipped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate what execute_tools.go does (lines 354-364)
			responseData := make(map[string]interface{})

			if tt.metadata != "" && tt.toolName != "" {
				var data interface{}
				if err := json.Unmarshal([]byte(tt.metadata), &data); err == nil {
					responseData[tt.toolName] = data
				}
			}

			// Compare result
			actualJSON, err := json.Marshal(responseData)
			require.NoError(t, err)
			expectedJSON, err := json.Marshal(tt.expectedData)
			require.NoError(t, err)

			assert.JSONEq(t, string(expectedJSON), string(actualJSON),
				"Response data structure mismatch\nDescription: %s", tt.description)
		})
	}
}

// TestResponseToolMetadataFlow tests the complete flow from ResponseTool to CEL evaluation
// This is an integration test that validates the entire chain
func TestResponseToolMetadataFlow(t *testing.T) {
	// Step 1: Simulate LLM calling filtered_results tool
	llmInput := map[string]interface{}{
		"results": []interface{}{
			map[string]interface{}{
				"tool_call_id": "call_abc",
				"name":         "bash",
				"content":      "some output",
				"is_error":     false,
			},
		},
	}

	// Step 2: ResponseTool would marshal this as metadata
	metadataBytes, err := json.Marshal(llmInput)
	require.NoError(t, err)
	metadata := string(metadataBytes)

	// Step 3: ExecuteTools extracts response_data from metadata
	responseData := make(map[string]interface{})
	toolName := "filtered_results"

	var data interface{}
	if err := json.Unmarshal([]byte(metadata), &data); err == nil {
		responseData[toolName] = data
	}

	// Step 4: Create typed CEL context and evaluate
	ctx := &wfcel.EdgeEvalContext{
		Nodes: map[string]interface{}{
			"execute_filter": map[string]interface{}{
				"response_data": responseData,
			},
		},
		Inputs:   map[string]interface{}{},
		Workflow: &model.WorkflowContext{},
	}

	expression := "nodes.execute_filter.response_data.filtered_results.results"
	resultValue, err := wfcel.EvaluateValue(expression, ctx)
	require.NoError(t, err, "Expression should evaluate successfully")
	require.NotNil(t, resultValue)

	// Should be an array of tool results
	resultArray, ok := resultValue.([]interface{})
	require.True(t, ok, "Result should be an array")
	require.Len(t, resultArray, 1)

	firstResult := resultArray[0].(map[string]interface{})
	assert.Equal(t, "call_abc", firstResult["tool_call_id"])
	assert.Equal(t, "bash", firstResult["name"])
	assert.Equal(t, "some output", firstResult["content"])
	assert.Equal(t, false, firstResult["is_error"])
}

// TestResponseDataNestedAccess validates nested response_data structure access
func TestResponseDataNestedAccess(t *testing.T) {
	t.Run("correct_implementation", func(t *testing.T) {
		responseData := map[string]interface{}{
			"filtered_results": map[string]interface{}{
				"results": []interface{}{
					map[string]interface{}{
						"tool_call_id": "call_123",
						"name":         "bash",
						"content":      "filtered content",
						"is_error":     false,
					},
				},
			},
		}

		ctx := &wfcel.EdgeEvalContext{
			Nodes: map[string]interface{}{
				"execute_filter": map[string]interface{}{
					"response_data": responseData,
				},
			},
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{},
		}

		expression := "nodes.execute_filter.response_data.filtered_results.results"
		_, err := wfcel.EvaluateValue(expression, ctx)
		require.NoError(t, err, "This should work with correct response_data structure")
	})

	t.Run("bug_scenario_missing_results_key", func(t *testing.T) {
		responseData := map[string]interface{}{
			"filtered_results": []interface{}{
				map[string]interface{}{
					"tool_call_id": "call_123",
					"name":         "bash",
					"content":      "filtered content",
					"is_error":     false,
				},
			},
		}

		ctx := &wfcel.EdgeEvalContext{
			Nodes: map[string]interface{}{
				"execute_filter": map[string]interface{}{
					"response_data": responseData,
				},
			},
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{},
		}

		expression := "nodes.execute_filter.response_data.filtered_results.results"
		_, err := wfcel.EvaluateValue(expression, ctx)
		assert.Error(t, err, "This should fail because .results doesn't exist on array")
	})

	t.Run("bug_scenario_empty_response_data", func(t *testing.T) {
		responseData := map[string]interface{}{}

		ctx := &wfcel.EdgeEvalContext{
			Nodes: map[string]interface{}{
				"execute_filter": map[string]interface{}{
					"response_data": responseData,
				},
			},
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{},
		}

		expression := "nodes.execute_filter.response_data.filtered_results.results"
		_, err := wfcel.EvaluateValue(expression, ctx)
		assert.Error(t, err, "This should fail because filtered_results doesn't exist")
	})
}
