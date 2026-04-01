// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecuteResponseToolInline tests the executeResponseToolInline helper function
func TestExecuteResponseToolInline(t *testing.T) {
	tests := []struct {
		name               string
		toolCallID         string
		toolName           string
		toolInput          string
		expectError        bool
		expectedContent    string
		expectedMetadata   string
		checkContentIsJSON bool
	}{
		{
			name:       "valid_json_input",
			toolCallID: "call_123",
			toolName:   "filtered_results",
			toolInput: `{
				"results": [
					{
						"tool_call_id": "call_abc",
						"name": "bash",
						"content": "output",
						"is_error": false
					}
				]
			}`,
			expectError:        false,
			checkContentIsJSON: true,
		},
		{
			name:            "invalid_json_input",
			toolCallID:      "call_456",
			toolName:        "filtered_results",
			toolInput:       "not valid json",
			expectError:     true,
			expectedContent: "Invalid JSON input:",
		},
		{
			name:               "empty_object_input",
			toolCallID:         "call_789",
			toolName:           "review",
			toolInput:          `{}`,
			expectError:        false,
			expectedContent:    "{}",
			expectedMetadata:   "{}",
			checkContentIsJSON: false,
		},
		{
			name:       "complex_nested_input",
			toolCallID: "call_complex",
			toolName:   "analysis",
			toolInput: `{
				"summary": "Found issues",
				"issues": [
					{"line": 10, "message": "unused variable"},
					{"line": 20, "message": "missing return"}
				],
				"confidence": 0.95
			}`,
			expectError:        false,
			checkContentIsJSON: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := executeResponseToolInline(tt.toolCallID, tt.toolName, tt.toolInput, nil)

			assert.Equal(t, tt.toolCallID, result.ToolCallID)
			assert.Equal(t, tt.toolName, result.Name)
			assert.Equal(t, tt.expectError, result.IsError)

			if tt.expectError {
				assert.Contains(t, result.Content, tt.expectedContent)
			} else if tt.checkContentIsJSON {
				// Verify content is valid JSON
				var contentJSON map[string]interface{}
				err := json.Unmarshal([]byte(result.Content), &contentJSON)
				require.NoError(t, err, "Content should be valid JSON")

				// Verify metadata matches content
				assert.Equal(t, result.Content, result.Metadata)
			} else {
				assert.Equal(t, tt.expectedContent, result.Content)
				assert.Equal(t, tt.expectedMetadata, result.Metadata)
			}
		})
	}
}

// TestExecuteResponseToolInlineMetadataExtraction tests that response tool
// metadata is properly structured for CEL access
func TestExecuteResponseToolInlineMetadataExtraction(t *testing.T) {
	// Simulate what the LLM would send as input to filtered_results
	llmInput := `{
		"results": [
			{
				"tool_call_id": "call_abc",
				"name": "bash",
				"content": "filtered output",
				"is_error": false
			}
		]
	}`

	result := executeResponseToolInline("call_response", "filtered_results", llmInput, nil)

	require.False(t, result.IsError)
	require.NotEmpty(t, result.Metadata)

	// Parse the metadata
	var metadata map[string]interface{}
	err := json.Unmarshal([]byte(result.Metadata), &metadata)
	require.NoError(t, err)

	// Verify the structure matches what CEL expects:
	// response_data.filtered_results.results
	// The metadata IS the "filtered_results" value, so we need "results" at top level
	results, ok := metadata["results"].([]interface{})
	require.True(t, ok, "metadata should have 'results' array")
	require.Len(t, results, 1)

	firstResult, ok := results[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "call_abc", firstResult["tool_call_id"])
	assert.Equal(t, "bash", firstResult["name"])
	assert.Equal(t, "filtered output", firstResult["content"])
}
