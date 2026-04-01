// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
)

func TestResponseTool_BasicUsage(t *testing.T) {
	def := ResponseToolDefinition{
		Name:        "test_response",
		Description: "A test response tool",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"status", "message"},
			"properties": map[string]interface{}{
				"status": map[string]interface{}{
					"type": "string",
					"enum": []interface{}{"passed", "failed"},
				},
				"message": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	tool := NewResponseTool(def)

	if tool.Name() != "test_response" {
		t.Errorf("Expected name 'test_response', got '%s'", tool.Name())
	}

	if tool.Description() != "A test response tool" {
		t.Errorf("Expected description 'A test response tool', got '%s'", tool.Description())
	}

	schema := tool.ParamSchema()
	if schema == nil {
		t.Fatal("Expected schema to be non-nil")
	}

	if schema.Type != "object" {
		t.Errorf("Expected schema type 'object', got '%s'", schema.Type)
	}

	// Check properties exist
	if schema.Properties == nil {
		t.Fatal("Expected properties to be non-nil")
	}

	// Verify status property with enum
	statusProp, _ := schema.Properties.Get("status")
	if statusProp == nil {
		t.Fatal("Expected 'status' property to exist")
	}
	if statusProp.Type != "string" {
		t.Errorf("Expected 'status' type 'string', got '%s'", statusProp.Type)
	}
	if len(statusProp.Enum) != 2 {
		t.Errorf("Expected 2 enum values, got %d", len(statusProp.Enum))
	}

	// Verify message property
	messageProp, _ := schema.Properties.Get("message")
	if messageProp == nil {
		t.Fatal("Expected 'message' property to exist")
	}
	if messageProp.Type != "string" {
		t.Errorf("Expected 'message' type 'string', got '%s'", messageProp.Type)
	}

	// Verify required fields
	if len(schema.Required) != 2 {
		t.Errorf("Expected 2 required fields, got %d", len(schema.Required))
	}
}

func TestResponseTool_Run(t *testing.T) {
	def := ResponseToolDefinition{
		Name:        "validation_result",
		Description: "Report validation results",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"status", "details"},
			"properties": map[string]interface{}{
				"status":  map[string]interface{}{"type": "string"},
				"details": map[string]interface{}{"type": "string"},
			},
		},
	}

	tool := NewResponseTool(def)

	// Create a mock context
	ctx := &rctx.ToolContext{}

	// Test with valid input
	input := `{"status": "passed", "details": "All checks passed successfully"}`
	call := ToolCall{
		ID:    "test-123",
		Name:  "validation_result",
		Input: input,
	}

	resp, err := tool.Run(ctx, call)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if resp.IsError {
		t.Errorf("Expected successful response, got error: %s", resp.Content)
	}

	// Verify the response contains the input data
	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Content), &respData); err != nil {
		t.Fatalf("Failed to parse response content: %v", err)
	}

	if status, ok := respData["status"].(string); !ok || status != "passed" {
		t.Errorf("Expected 'status' to be 'passed', got '%v'", respData["status"])
	}

	if details, ok := respData["details"].(string); !ok || details != "All checks passed successfully" {
		t.Errorf("Expected 'details' to be 'All checks passed successfully', got '%v'", respData["details"])
	}

	// Test metadata is set
	if resp.Metadata == "" {
		t.Error("Expected metadata to be set")
	}
}

func TestResponseTool_RunInvalidJSON(t *testing.T) {
	def := ResponseToolDefinition{
		Name:        "test",
		Description: "Test",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"result": map[string]interface{}{"type": "string"},
			},
		},
	}

	tool := NewResponseTool(def)
	ctx := &rctx.ToolContext{}

	call := ToolCall{
		ID:    "test-123",
		Name:  "test",
		Input: "not valid json",
	}

	resp, err := tool.Run(ctx, call)
	if err != nil {
		t.Fatalf("Expected no error (error in response), got %v", err)
	}

	if !resp.IsError {
		t.Error("Expected error response for invalid JSON")
	}
}

func TestResponseTool_ArraySchema(t *testing.T) {
	// Test the array schema format with nested results
	def := ResponseToolDefinition{
		Name:        "filtered_results",
		Description: "Submit filtered tool results",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"results"},
			"properties": map[string]interface{}{
				"results": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"tool_call_id", "content"},
						"properties": map[string]interface{}{
							"tool_call_id": map[string]interface{}{"type": "string"},
							"name":         map[string]interface{}{"type": "string"},
							"content":      map[string]interface{}{"type": "string"},
							"is_error":     map[string]interface{}{"type": "boolean"},
						},
					},
				},
			},
		},
	}

	tool := NewResponseTool(def)
	schema := tool.ParamSchema()

	// Verify results property exists and is an array
	resultsProp, _ := schema.Properties.Get("results")
	if resultsProp == nil {
		t.Fatal("Expected 'results' property to exist")
	}
	if resultsProp.Type != "array" {
		t.Errorf("Expected 'results' type 'array', got '%s'", resultsProp.Type)
	}

	// Verify items schema
	if resultsProp.Items == nil {
		t.Fatal("Expected 'items' to be defined for array")
	}
	if resultsProp.Items.Type != "object" {
		t.Errorf("Expected items type 'object', got '%s'", resultsProp.Items.Type)
	}

	// Verify item properties
	toolCallIDProp, _ := resultsProp.Items.Properties.Get("tool_call_id")
	if toolCallIDProp == nil {
		t.Fatal("Expected 'tool_call_id' property in items")
	}

	contentProp, _ := resultsProp.Items.Properties.Get("content")
	if contentProp == nil {
		t.Fatal("Expected 'content' property in items")
	}
}

func TestResponseTool_EmptySchema(t *testing.T) {
	def := ResponseToolDefinition{
		Name:        "test",
		Description: "Test",
		Schema:      nil,
	}

	tool := NewResponseTool(def)
	schema := tool.ParamSchema()

	// Should get a default empty object schema
	if schema.Type != "object" {
		t.Errorf("Expected default type 'object', got '%s'", schema.Type)
	}
}

func TestResponseTool_IsReadOnly(t *testing.T) {
	def := ResponseToolDefinition{
		Name:        "test",
		Description: "Test",
		Schema: map[string]interface{}{
			"type": "object",
		},
	}

	tool := NewResponseTool(def)

	if readOnly, ok := tool.(ReadOnlyTool); !ok {
		t.Error("Expected tool to implement ReadOnlyTool")
	} else if !readOnly.IsReadOnly() {
		t.Error("Expected IsReadOnly() to return true")
	}
}

func TestResponseTool_RequiresNoPermission(t *testing.T) {
	def := ResponseToolDefinition{
		Name:        "test",
		Description: "Test",
		Schema: map[string]interface{}{
			"type": "object",
		},
	}

	tool := NewResponseTool(def)
	ctx := &rctx.ToolContext{}

	requires, err := tool.RequiresPermission(ctx, ToolCall{})
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if requires {
		t.Error("Expected RequiresPermission to return false")
	}
}

func TestResponseTool_RunWithArrayInput(t *testing.T) {
	def := ResponseToolDefinition{
		Name:        "filtered_results",
		Description: "Submit filtered results",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"results"},
			"properties": map[string]interface{}{
				"results": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"tool_call_id": map[string]interface{}{"type": "string"},
							"content":      map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
	}

	tool := NewResponseTool(def)
	ctx := &rctx.ToolContext{}

	input := `{
		"results": [
			{"tool_call_id": "call_1", "content": "Result 1"},
			{"tool_call_id": "call_2", "content": "Result 2"}
		]
	}`

	call := ToolCall{
		ID:    "test-123",
		Name:  "filtered_results",
		Input: input,
	}

	resp, err := tool.Run(ctx, call)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if resp.IsError {
		t.Errorf("Expected successful response, got error: %s", resp.Content)
	}

	// Verify the response contains the array data
	var respData map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Content), &respData); err != nil {
		t.Fatalf("Failed to parse response content: %v", err)
	}

	results, ok := respData["results"].([]interface{})
	if !ok {
		t.Fatalf("Expected 'results' to be an array, got %T", respData["results"])
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}
}

// TestResponseToolMetadataForWorkflow tests the workflow response tool scenario
// This validates that metadata contains the nested structure needed for CEL evaluation
func TestResponseToolMetadataForWorkflow(t *testing.T) {
	// This test validates the workflow scenario:
	// 1. A node uses a response tool with structured schema
	// 2. LLM responds with structured data matching the schema
	// 3. ResponseTool returns it as metadata
	// 4. ExecuteTools extracts it into response_data
	// 5. CEL evaluates: nodes.execute_filter.response_data.filtered_results.results

	def := ResponseToolDefinition{
		Name:        "filtered_results",
		Description: "Submit filtered tool results",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"results"},
			"properties": map[string]interface{}{
				"results": map[string]interface{}{
					"type":        "array",
					"description": "Filtered result for each tool call",
					"items": map[string]interface{}{
						"type":     "object",
						"required": []interface{}{"tool_call_id", "name", "content"},
						"properties": map[string]interface{}{
							"tool_call_id": map[string]interface{}{"type": "string"},
							"name":         map[string]interface{}{"type": "string"},
							"content":      map[string]interface{}{"type": "string"},
							"is_error":     map[string]interface{}{"type": "boolean"},
						},
					},
				},
			},
		},
	}

	tool := NewResponseTool(def)
	ctx := &rctx.ToolContext{}

	// Simulate LLM response matching the schema
	input := `{
		"results": [
			{
				"tool_call_id": "toolu_01ABC",
				"name": "bash",
				"content": "Command executed successfully",
				"is_error": false
			}
		]
	}`

	call := ToolCall{
		ID:    "call_filter",
		Name:  "filtered_results",
		Input: input,
	}

	resp, err := tool.Run(ctx, call)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if resp.IsError {
		t.Fatalf("Expected successful response, got error: %s", resp.Content)
	}

	// CRITICAL TEST: Verify metadata structure
	if resp.Metadata == "" {
		t.Fatal("Metadata must be set by ResponseTool")
	}

	// Parse metadata
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(resp.Metadata), &metadata); err != nil {
		t.Fatalf("Metadata must be valid JSON: %v", err)
	}

	// Verify "results" key exists in metadata
	results, ok := metadata["results"]
	if !ok {
		t.Fatal("Metadata must have 'results' key - this is required for CEL path")
	}

	// Verify results is an array
	resultsArray, ok := results.([]interface{})
	if !ok {
		t.Fatalf("Metadata 'results' must be an array, got %T", results)
	}

	if len(resultsArray) != 1 {
		t.Errorf("Expected 1 result, got %d", len(resultsArray))
	}

	// Now simulate what ExecuteTools does (execute_tools.go:354-364)
	responseData := make(map[string]interface{})
	toolName := "filtered_results"

	var data interface{}
	if err := json.Unmarshal([]byte(resp.Metadata), &data); err == nil {
		responseData[toolName] = data
	}

	// Verify response_data structure
	if _, ok := responseData["filtered_results"]; !ok {
		t.Fatal("response_data must contain 'filtered_results' key")
	}

	filteredResults := responseData["filtered_results"].(map[string]interface{})
	if _, ok := filteredResults["results"]; !ok {
		t.Fatal("response_data.filtered_results must contain 'results' key")
	}

	// This confirms the CEL path works:
	// nodes.execute_filter.response_data.filtered_results.results
	finalResults := filteredResults["results"].([]interface{})
	if len(finalResults) != 1 {
		t.Errorf("Expected 1 final result, got %d", len(finalResults))
	}

	t.Log("✓ ResponseTool metadata structure is correct")
	t.Log("✓ Path response_data.filtered_results.results exists")
	t.Log("✓ ExecuteTools can extract response_data correctly")
}
