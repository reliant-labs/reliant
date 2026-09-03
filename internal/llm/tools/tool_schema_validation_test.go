// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAllToolsSchemasResolved verifies that all registered tools have schemas
// without $ref or $defs, as required by the Anthropic API
func TestAllToolsSchemasResolved(t *testing.T) {
	t.Parallel()
	// Create factory
	factory := NewToolsFactory(&ToolsOptions{})

	// Get tool registry
	registry := GetToolRegistry()

	if len(registry) == 0 {
		t.Fatal("No tools registered")
	}

	t.Logf("Testing %d tools", len(registry))

	for _, toolDef := range registry {
		toolName := toolDef.Name
		t.Run(toolName, func(t *testing.T) {
			tool := toolDef.Factory(factory)
			if tool == nil {
				t.Fatal("Factory returned nil tool")
			}

			schema := tool.ParamSchema()
			if schema == nil {
				t.Skip("Tool has no schema")
				return
			}

			// Marshal to JSON
			schemaJSON, err := json.Marshal(schema)
			if err != nil {
				t.Fatalf("Failed to marshal schema: %v", err)
			}

			schemaStr := string(schemaJSON)

			// Check for $defs (Anthropic doesn't support them)
			if strings.Contains(schemaStr, `"$defs"`) {
				t.Errorf("Schema contains $defs after ResolveSchemaRefs:\n%s", schemaStr)
			}

			// Check for $ref (Anthropic doesn't support them)
			if strings.Contains(schemaStr, `"$ref"`) {
				t.Errorf("Schema contains unresolved $ref:\n%s", schemaStr)
			}

			// OpenAI schema compatibility guardrail:
			// For any array schema, `items` must be a JSON object schema (not boolean / null).
			// OpenAI rejects function schemas where array schema items is not an object.
			if strings.Contains(schemaStr, `"type":"array","items":true`) ||
				strings.Contains(schemaStr, `"type":"array","items":false`) {
				t.Errorf("Schema contains boolean array items (OpenAI rejects this):\n%s", schemaStr)
			}
		})
	}
}
