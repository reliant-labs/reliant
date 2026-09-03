// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnthropicAPISchemaCompliance validates that all tool schemas comply with
// Anthropic API requirements: no $ref or $defs should be present in the final schema.
// This test ensures we don't regress on the fix for:
// "tools.41.custom.input_schema: Invalid $ref in schema: '#/$defs/LinterConfig' does not exist"
func TestAnthropicAPISchemaCompliance(t *testing.T) {
	t.Parallel()
	factory := NewToolsFactory(&ToolsOptions{})
	registry := GetToolRegistry()

	var failedTools []string
	var totalSchemas int

	for _, toolDef := range registry {
		tool := toolDef.Factory(factory)
		if tool == nil {
			continue
		}

		schema := tool.ParamSchema()
		if schema == nil {
			continue
		}

		totalSchemas++

		schemaJSON, err := json.Marshal(schema)
		if err != nil {
			t.Errorf("Tool %s: failed to marshal schema: %v", toolDef.Name, err)
			failedTools = append(failedTools, toolDef.Name)
			continue
		}

		schemaStr := string(schemaJSON)

		// Check for $defs (should be removed by ResolveSchemaRefs)
		if strings.Contains(schemaStr, `"$defs"`) {
			t.Errorf("Tool %s: schema contains $defs which violates Anthropic API requirements", toolDef.Name)
			failedTools = append(failedTools, toolDef.Name)
			continue
		}

		// Check for unresolved $ref (should be inlined by ResolveSchemaRefs)
		if strings.Contains(schemaStr, `"$ref"`) {
			t.Errorf("Tool %s: schema contains unresolved $ref which violates Anthropic API requirements", toolDef.Name)
			failedTools = append(failedTools, toolDef.Name)

			// Show where the $ref appears for debugging
			lines := strings.Split(schemaStr, "\n")
			for i, line := range lines {
				if strings.Contains(line, `"$ref"`) {
					t.Logf("  Line %d: %s", i+1, line)
				}
			}
			continue
		}
	}

	if len(failedTools) > 0 {
		t.Fatalf("❌ %d/%d tools failed Anthropic API schema validation: %v",
			len(failedTools), totalSchemas, failedTools)
	}

	t.Logf("✅ All %d tool schemas comply with Anthropic API requirements (no $ref or $defs)", totalSchemas)
}

// TestLinterConfigSchemaResolution specifically tests that LinterConfig references
// are properly resolved, as this was the original issue reported.
func TestLinterConfigSchemaResolution(t *testing.T) {
	t.Parallel()
	factory := NewToolsFactory(&ToolsOptions{})
	tool := factory.MetadataWriter()

	schema := tool.ParamSchema()
	if schema == nil {
		t.Fatal("metadata_writer tool should have a schema")
	}

	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Failed to marshal schema: %v", err)
	}

	schemaStr := string(schemaJSON)

	// The original error was about LinterConfig
	if strings.Contains(schemaStr, `"$ref"`) && strings.Contains(schemaStr, "LinterConfig") {
		t.Error("Found unresolved $ref to LinterConfig - this is the original bug!")

		// Find and display the problematic reference
		var found bool
		var jsonData map[string]interface{}
		json.Unmarshal(schemaJSON, &jsonData)

		if searchForRef(jsonData, "LinterConfig") {
			found = true
		}

		if found {
			t.Log("Schema still contains reference to LinterConfig")
		}
	}

	// Verify LinterConfig properties are inlined
	if !strings.Contains(schemaStr, `"Command"`) || !strings.Contains(schemaStr, `"Name"`) {
		t.Error("LinterConfig properties (Name, Command) should be inlined in the schema")
	}

	t.Log("✅ LinterConfig references are properly resolved and inlined")
}

// searchForRef recursively searches for $ref references in a JSON object
func searchForRef(data interface{}, targetRef string) bool {
	switch v := data.(type) {
	case map[string]interface{}:
		if ref, ok := v["$ref"].(string); ok && strings.Contains(ref, targetRef) {
			return true
		}
		for _, val := range v {
			if searchForRef(val, targetRef) {
				return true
			}
		}
	case []interface{}:
		for _, item := range v {
			if searchForRef(item, targetRef) {
				return true
			}
		}
	}
	return false
}

// TestResolveSchemaRefsWithNestedArrays tests the fix for nested array item references
func TestResolveSchemaRefsWithNestedArrays(t *testing.T) {
	t.Parallel()
	// This simulates the exact structure that was failing:
	// An array with items that reference a definition
	testSchema := map[string]interface{}{
		"$defs": map[string]interface{}{
			"LinterConfig": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":    map[string]interface{}{"type": "string"},
					"command": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"name", "command"},
			},
		},
		"properties": map[string]interface{}{
			"linters": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"$ref": "#/$defs/LinterConfig",
				},
			},
		},
	}

	// Convert to JSON and back to simulate what jsonschema library does
	jsonBytes, _ := json.Marshal(testSchema)
	var schemaObj map[string]interface{}
	json.Unmarshal(jsonBytes, &schemaObj)

	// Get the defs
	defs := schemaObj["$defs"].(map[string]interface{})

	// Resolve refs
	resolveRefsInValue(schemaObj, defs)

	// Marshal to check result
	resultJSON, _ := json.MarshalIndent(schemaObj, "", "  ")
	resultStr := string(resultJSON)

	// Should not contain any $ref
	if strings.Contains(resultStr, `"$ref"`) {
		t.Error("Schema still contains $ref after resolving")
		t.Logf("Result:\n%s", resultStr)
	}

	// Should contain the inlined properties
	if !strings.Contains(resultStr, `"name"`) || !strings.Contains(resultStr, `"command"`) {
		t.Error("LinterConfig properties should be inlined")
	}

	t.Log("✅ Nested array item references are properly resolved")
}
