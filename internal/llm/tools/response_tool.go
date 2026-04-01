// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// ResponseToolDefinition defines a custom response tool from workflow YAML.
// The schema field accepts standard JSON Schema, which gets wrapped in an object type.
//
// Example YAML:
//
//	response_tool:
//	  name: filtered_results
//	  description: Submit filtered tool results
//	  schema:
//	    type: object
//	    required: [results]
//	    properties:
//	      results:
//	        type: array
//	        items:
//	          type: object
//	          required: [tool_call_id, content]
//	          properties:
//	            tool_call_id: { type: string }
//	            name: { type: string }
//	            content: { type: string }
//	            is_error: { type: boolean }
type ResponseToolDefinition struct {
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	Schema      map[string]interface{} `json:"schema" yaml:"schema"` // Raw JSON Schema
}

// ResponseTool is a tool that captures structured responses from the LLM.
// Unlike SchemaOnlyTool, this actually executes and returns the LLM's input
// back as the tool result, making it available for workflow condition evaluation.
type ResponseTool struct {
	name        string
	description string
	schema      *jsonschema.Schema
}

// NewResponseTool creates a response tool from a definition
func NewResponseTool(def ResponseToolDefinition) Tool {
	schema := schemaFromMap(def.Schema)

	return &ResponseTool{
		name:        def.Name,
		description: def.Description,
		schema:      schema,
	}
}

// schemaFromMap converts a map[string]interface{} (from YAML) to a jsonschema.Schema.
// This allows workflows to define standard JSON Schema in YAML format.
func schemaFromMap(m map[string]interface{}) *jsonschema.Schema {
	if m == nil {
		// Return empty object schema as fallback
		return &jsonschema.Schema{Type: "object"}
	}

	// Marshal to JSON then unmarshal to jsonschema.Schema
	// This is the cleanest way to convert since jsonschema.Schema has proper JSON tags
	data, err := json.Marshal(m)
	if err != nil {
		return &jsonschema.Schema{Type: "object"}
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return &jsonschema.Schema{Type: "object"}
	}

	return &schema
}

func (t *ResponseTool) Name() string {
	return t.name
}

func (t *ResponseTool) Description() string {
	return t.description
}

func (t *ResponseTool) ParamSchema() *jsonschema.Schema {
	return t.schema
}

func (t *ResponseTool) RequiresPermission(rctx *rctx.ToolContext, params ToolCall) (bool, error) {
	return false, nil
}

func (t *ResponseTool) IsReadOnly() bool {
	return true
}

func (t *ResponseTool) Run(rctx *rctx.ToolContext, params ToolCall) (ToolResponse, error) {
	// Parse the input to validate it's proper JSON
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(params.Input), &input); err != nil {
		return NewTextErrorResponse("Invalid JSON input: " + err.Error()), nil
	}

	// Return the input as-is - this makes the structured data available
	// to the workflow through the tool result
	responseJSON, err := json.Marshal(input)
	if err != nil {
		return NewTextErrorResponse("Failed to serialize response: " + err.Error()), nil
	}

	// Return the JSON as the response content and also as metadata
	// This allows workflows to access it via:
	// - nodes.<node_id>.tool_results[*].content (as JSON string)
	// - nodes.<node_id>.tool_results[*].metadata (as parsed object)
	response := NewTextResponse(string(responseJSON))
	return WithResponseMetadata(response, input), nil
}
