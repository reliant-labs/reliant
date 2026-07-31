// Copyright (c) 2025 Reliant Labs
package openai

import (
	"encoding/json"
	"testing"
)

func TestNormalizeResponsesToolSchema_EmptyObjectStaysObject(t *testing.T) {
	// This test verifies the fix for the OpenAI error:
	// "Invalid schema for function 'mcp__chrome-devtools__emulate': schema must be a JSON Schema of 'type: \"object\"', got 'type: \"array\"'."
	//
	// Tools like mcp__chrome-devtools__emulate have schemas like {"type": "object"} with no properties.
	// OpenAI requires root tool parameters to be type=object, so we must not rewrite these to arrays.

	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
	}

	NormalizeResponsesToolSchema(schema)

	// Must remain type: object (not be rewritten to array)
	if schema["type"] != "object" {
		t.Errorf("expected type 'object', got %v", schema["type"])
	}

	// Must have additionalProperties: false
	if schema["additionalProperties"] != false {
		t.Errorf("expected additionalProperties=false, got %v", schema["additionalProperties"])
	}

	// Must have properties (even if empty) for strict mode
	if schema["properties"] == nil {
		t.Error("expected properties to be set (even if empty)")
	}

	// Must have required (even if empty)
	if schema["required"] == nil {
		t.Error("expected required to be set (even if empty)")
	}
}

func TestNormalizeResponsesToolSchema_NestedMapRewrittenToArray(t *testing.T) {
	// Nested map schemas (like metadata: map[string]any) should still be rewritten to KV arrays.
	// Only root-level empty objects should stay as objects.

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"metadata": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
		},
	}

	NormalizeResponsesToolSchema(schema)

	// Root must stay as object
	if schema["type"] != "object" {
		t.Errorf("root type should be 'object', got %v", schema["type"])
	}

	// Nested metadata should be rewritten to array
	props := schema["properties"].(map[string]any)
	metadata := props["metadata"].(map[string]any)
	if metadata["type"] != "array" {
		t.Errorf("metadata type should be 'array' (KV pairs), got %v", metadata["type"])
	}

	// Verify KV array structure
	items := metadata["items"].(map[string]any)
	if items["type"] != "object" {
		t.Errorf("items type should be 'object', got %v", items["type"])
	}
	itemProps := items["properties"].(map[string]any)
	if itemProps["key"] == nil || itemProps["value"] == nil {
		t.Error("items should have 'key' and 'value' properties")
	}
}

func TestNormalizeResponsesToolSchema_ObjectWithProperties(t *testing.T) {
	// Normal object schemas with properties should work correctly

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
		},
		"required": []any{"name"},
	}

	NormalizeResponsesToolSchema(schema)

	// Type should remain object
	if schema["type"] != "object" {
		t.Errorf("expected type 'object', got %v", schema["type"])
	}

	// additionalProperties should be false
	if schema["additionalProperties"] != false {
		t.Errorf("expected additionalProperties=false, got %v", schema["additionalProperties"])
	}

	// required is the tool's declared list, untouched: 'count' is optional and
	// must stay optional so the model is allowed to omit it.
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("required should be a slice")
	}
	if len(required) != 1 || required[0] != "name" {
		t.Errorf("required should be ['name'], got %v", required)
	}
}

func TestNormalizeResponsesToolSchema_ObjectWithNoDeclaredRequired(t *testing.T) {
	// A tool whose parameters are all optional must reach the model that way.

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
	}

	NormalizeResponsesToolSchema(schema)

	if raw, ok := schema["required"]; ok {
		if names := requiredNames(raw); len(names) != 0 {
			t.Errorf("required should stay empty, got %v", names)
		}
	}
}

func TestNormalizeResponsesToolSchema_NestedObjects(t *testing.T) {
	// Nested object schemas should also be normalized

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"enabled": map[string]any{"type": "boolean"},
				},
				"required": []any{"enabled"},
			},
		},
	}

	NormalizeResponsesToolSchema(schema)

	// Check nested object was normalized
	props := schema["properties"].(map[string]any)
	config := props["config"].(map[string]any)

	if config["additionalProperties"] != false {
		t.Errorf("nested object should have additionalProperties=false")
	}

	required, ok := config["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "enabled" {
		t.Errorf("nested object should have required=['enabled'], got %v", config["required"])
	}
}

func TestNormalizeResponsesToolSchema_ArrayItems(t *testing.T) {
	// Array items should also be normalized

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "string"},
					},
					"required": []any{"id"},
				},
			},
		},
	}

	NormalizeResponsesToolSchema(schema)

	// Check array items object was normalized
	props := schema["properties"].(map[string]any)
	items := props["items"].(map[string]any)
	arrayItems := items["items"].(map[string]any)

	if arrayItems["additionalProperties"] != false {
		t.Errorf("array items object should have additionalProperties=false")
	}

	required, ok := arrayItems["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "id" {
		t.Errorf("array items should have required=['id'], got %v", arrayItems["required"])
	}
}

func TestNormalizeResponsesToolSchema_NilSchema(t *testing.T) {
	// Should handle nil gracefully
	NormalizeResponsesToolSchema(nil)
	// No panic = pass
}

func TestNormalizeResponsesToolSchema_Combinators(t *testing.T) {
	// anyOf/oneOf/allOf should have their children normalized

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{
				"anyOf": []any{
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"str": map[string]any{"type": "string"},
						},
						"required": []any{"str"},
					},
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"num": map[string]any{"type": "integer"},
						},
						"required": []any{"num"},
					},
				},
			},
		},
	}

	NormalizeResponsesToolSchema(schema)

	// Check anyOf children were normalized
	props := schema["properties"].(map[string]any)
	value := props["value"].(map[string]any)
	anyOf := value["anyOf"].([]any)

	for i, opt := range anyOf {
		optMap := opt.(map[string]any)
		if optMap["additionalProperties"] != false {
			t.Errorf("anyOf[%d] should have additionalProperties=false", i)
		}
		if optMap["required"] == nil {
			t.Errorf("anyOf[%d] should have required set", i)
		}
	}
}

func TestValidateResponsesToolSchema_RejectsNonObjectItems(t *testing.T) {
	t.Run("direct tuple items", func(t *testing.T) {
		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"payload": map[string]any{
					"type":  "array",
					"items": []any{map[string]any{"type": "string"}},
				},
			},
			"required":             []any{"payload"},
			"additionalProperties": false,
		}

		NormalizeResponsesToolSchema(schema)
		err := ValidateResponsesToolSchema(schema)
		if err == nil {
			t.Fatal("expected tuple-style items to be rejected")
		}
	})

	t.Run("nested under anyOf", func(t *testing.T) {
		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"params": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"application/json": map[string]any{
							"anyOf": []any{
								map[string]any{
									"type":  "array",
									"items": []any{map[string]any{"type": "string"}},
								},
							},
						},
					},
				},
			},
			"required":             []any{"params"},
			"additionalProperties": false,
		}

		NormalizeResponsesToolSchema(schema)
		err := ValidateResponsesToolSchema(schema)
		if err == nil {
			t.Fatal("expected non-object items nested under anyOf to be rejected")
		}
	})
}

func TestRewriteMapSchemaToKVArray(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected bool // whether rewrite should occur
	}{
		{
			name:     "nil schema",
			input:    nil,
			expected: false,
		},
		{
			name:     "non-object type",
			input:    map[string]any{"type": "string"},
			expected: false,
		},
		{
			name: "object with properties",
			input: map[string]any{
				"type":       "object",
				"properties": map[string]any{"foo": map[string]any{"type": "string"}},
			},
			expected: false,
		},
		{
			name: "object without additionalProperties",
			input: map[string]any{
				"type": "object",
			},
			expected: false,
		},
		{
			name: "map schema with additionalProperties",
			input: map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
			expected: true,
		},
		{
			name: "map schema with additionalProperties=true",
			input: map[string]any{
				"type":                 "object",
				"additionalProperties": true,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to avoid modifying the test case
			var schema map[string]any
			if tt.input != nil {
				b, _ := json.Marshal(tt.input)
				_ = json.Unmarshal(b, &schema)
			}

			result := rewriteMapSchemaToKVArray(schema)
			if result != tt.expected {
				t.Errorf("rewriteMapSchemaToKVArray() = %v, want %v", result, tt.expected)
			}

			if result {
				// Verify the rewritten schema structure
				if schema["type"] != "array" {
					t.Errorf("rewritten schema should have type=array")
				}
				items, ok := schema["items"].(map[string]any)
				if !ok {
					t.Fatal("rewritten schema should have items")
				}
				if items["type"] != "object" {
					t.Errorf("items should be an object")
				}
				itemProps := items["properties"].(map[string]any)
				if itemProps["key"] == nil || itemProps["value"] == nil {
					t.Error("items should have key and value properties")
				}
			}
		})
	}
}
