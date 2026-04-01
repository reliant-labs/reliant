// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"testing"

	"github.com/invopop/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

func TestUnwrapStringifiedValues(t *testing.T) {
	editTool := NewEditTool()
	editSchema := editTool.ParamSchema()

	// Create a test schema with various types
	props := orderedmap.New[string, *jsonschema.Schema]()
	props.Set("edits", &jsonschema.Schema{Type: "array"})
	props.Set("limit", &jsonschema.Schema{Type: "integer"})
	props.Set("ratio", &jsonschema.Schema{Type: "number"})
	props.Set("enabled", &jsonschema.Schema{Type: "boolean"})
	props.Set("name", &jsonschema.Schema{Type: "string"})
	props.Set("config", &jsonschema.Schema{Type: "object"})
	testSchema := &jsonschema.Schema{Properties: props}

	tests := []struct {
		name     string
		schema   *jsonschema.Schema
		input    string
		expected string
	}{
		{
			name:     "stringified array gets unwrapped",
			schema:   editSchema,
			input:    `{"edits": "[{\"file_path\": \"/test.go\"}]"}`,
			expected: `{"edits":[{"file_path":"/test.go"}]}`,
		},
		{
			name:     "normal array unchanged",
			schema:   editSchema,
			input:    `{"edits": [{"file_path": "/test.go"}]}`,
			expected: `{"edits":[{"file_path":"/test.go"}]}`,
		},
		{
			name:     "stringified integer gets unwrapped",
			schema:   testSchema,
			input:    `{"limit": "5"}`,
			expected: `{"limit":5}`,
		},
		{
			name:     "stringified float gets unwrapped",
			schema:   testSchema,
			input:    `{"ratio": "3.14"}`,
			expected: `{"ratio":3.14}`,
		},
		{
			name:     "stringified boolean gets unwrapped",
			schema:   testSchema,
			input:    `{"enabled": "true"}`,
			expected: `{"enabled":true}`,
		},
		{
			name:     "stringified object gets unwrapped",
			schema:   testSchema,
			input:    `{"config": "{\"key\": \"value\"}"}`,
			expected: `{"config":{"key":"value"}}`,
		},
		{
			name:     "string type NOT unwrapped (even if valid JSON)",
			schema:   testSchema,
			input:    `{"name": "true"}`,
			expected: `{"name": "true"}`,
		},
		{
			name:     "string type NOT unwrapped (numeric string)",
			schema:   testSchema,
			input:    `{"name": "123"}`,
			expected: `{"name": "123"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unwrapStringifiedValues(tt.input, tt.schema)

			// Normalize both for comparison
			var resultMap, expectedMap map[string]interface{}
			if err := json.Unmarshal([]byte(result), &resultMap); err != nil {
				t.Fatalf("Failed to unmarshal result: %v\nResult: %s", err, result)
			}
			if err := json.Unmarshal([]byte(tt.expected), &expectedMap); err != nil {
				t.Fatalf("Failed to unmarshal expected: %v", err)
			}

			resultNorm, _ := json.Marshal(resultMap)
			expectedNorm, _ := json.Marshal(expectedMap)

			if string(resultNorm) != string(expectedNorm) {
				t.Errorf("unwrapStringifiedValues()\ngot:  %s\nwant: %s", string(resultNorm), string(expectedNorm))
			}
		})
	}
}

func TestUnwrapStringifiedValues_NilSchema(t *testing.T) {
	input := `{"edits": "[{\"file_path\": \"/test.go\"}]"}`
	result := unwrapStringifiedValues(input, nil)

	if result != input {
		t.Errorf("Expected no change with nil schema, got: %s", result)
	}
}

func TestUnwrapStringifiedValues_InvalidJSON(t *testing.T) {
	editTool := NewEditTool()
	schema := editTool.ParamSchema()

	input := `{invalid json}`
	result := unwrapStringifiedValues(input, schema)

	if result != input {
		t.Errorf("Expected no change with invalid JSON, got: %s", result)
	}
}
