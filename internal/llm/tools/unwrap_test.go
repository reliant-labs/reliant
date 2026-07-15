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

func TestUnwrapStringifiedValues_DoubleStringified(t *testing.T) {
	editTool := NewEditTool()
	schema := editTool.ParamSchema()

	// edits arrives as a JSON string whose contents are *themselves* a JSON
	// string (double-encoded). A single unwrap yields a string, not an array,
	// so the value must be peeled twice to reach the array.
	input := `{"edits": "\"[{\\\"file_path\\\": \\\"/x.go\\\", \\\"old_string\\\": \\\"a\\\", \\\"new_string\\\": \\\"b\\\"}]\""}`

	result := unwrapStringifiedValues(input, schema)
	if err := validateJSONSchema(result, schema); err != nil {
		t.Fatalf("double-stringified edits should validate after unwrap, got: %v\nresult: %s", err, result)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(result), &data); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if _, ok := data["edits"].([]interface{}); !ok {
		t.Errorf("expected edits to be an array after unwrap, got %T", data["edits"])
	}
}

func TestUnwrapStringifiedValues_WholeObjectStringified(t *testing.T) {
	editTool := NewEditTool()
	schema := editTool.ParamSchema()

	// The entire argument payload arrived as a JSON-encoded string rather than
	// a JSON object.
	input := `"{\"edits\": [{\"file_path\": \"/x.go\", \"old_string\": \"a\", \"new_string\": \"b\"}]}"`

	result := unwrapStringifiedValues(input, schema)
	if err := validateJSONSchema(result, schema); err != nil {
		t.Fatalf("whole-object-stringified payload should validate after unwrap, got: %v\nresult: %s", err, result)
	}
}

func TestUnwrapStringifiedObject(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantObj bool // result should parse as a JSON object
	}{
		{name: "already object", input: `{"a": 1}`, wantObj: true},
		{name: "single stringified", input: `"{\"a\": 1}"`, wantObj: true},
		{name: "double stringified", input: `"\"{\\\"a\\\": 1}\""`, wantObj: true},
		{name: "plain string stays", input: `"just text"`, wantObj: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := unwrapStringifiedObject(tt.input)
			var m map[string]interface{}
			gotObj := json.Unmarshal([]byte(out), &m) == nil
			if gotObj != tt.wantObj {
				t.Errorf("unwrapStringifiedObject(%s) = %q; parsed-as-object=%v want %v", tt.input, out, gotObj, tt.wantObj)
			}
		})
	}
}

func TestUnwrapToType(t *testing.T) {
	tests := []struct {
		name   string
		strVal string
		typ    string
		wantOK bool
	}{
		{name: "array single", strVal: `[{"x":1}]`, typ: "array", wantOK: true},
		{name: "array double", strVal: `"[{\"x\":1}]"`, typ: "array", wantOK: true},
		{name: "integer", strVal: `5`, typ: "integer", wantOK: true},
		{name: "boolean", strVal: `true`, typ: "boolean", wantOK: true},
		{name: "object", strVal: `{"k":"v"}`, typ: "object", wantOK: true},
		{name: "wrong type stays string", strVal: `"hello"`, typ: "array", wantOK: false},
		{name: "not json", strVal: `not json`, typ: "array", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := unwrapToType(tt.strVal, tt.typ)
			if ok != tt.wantOK {
				t.Errorf("unwrapToType(%s, %s) ok=%v want %v", tt.strVal, tt.typ, ok, tt.wantOK)
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
