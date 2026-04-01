// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// SchemaOnlyTool is a tool that only provides a schema to the LLM but doesn't execute
// This is useful for workflow-native tools where the workflow handles execution directly
type SchemaOnlyTool struct {
	name        string
	description string
	schemaMap   map[string]any
}

// NewSchemaOnlyTool creates a new schema-only tool
// The schemaMap parameter should be a map representing the JSON schema for parameters
func NewSchemaOnlyTool(name string, description string, schemaMap map[string]interface{}) Tool {
	return &SchemaOnlyTool{
		name:        name,
		description: description,
		schemaMap:   toAnyMap(schemaMap),
	}
}

func toAnyMap(m map[string]interface{}) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (t *SchemaOnlyTool) Name() string {
	return t.name
}

func (t *SchemaOnlyTool) Description() string {
	return t.description
}

func (t *SchemaOnlyTool) ParamSchema() *jsonschema.Schema {
	// SchemaOnlyTool is intended for provider-specific schema emission (e.g. OpenAI Responses).
	// Our Tool interface currently returns *jsonschema.Schema, so we marshal/unmarshal to preserve
	// nested structure rather than dropping it.
	if t.schemaMap == nil {
		return &jsonschema.Schema{Type: "object"}
	}
	b, err := json.Marshal(t.schemaMap)
	if err != nil {
		return &jsonschema.Schema{Type: "object"}
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return &jsonschema.Schema{Type: "object"}
	}
	return &s
}

// ParamSchemaMap returns the raw JSON schema map for providers that can consume it directly.
// This preserves nested schema structure (unlike the invopop Schema object model).
func (t *SchemaOnlyTool) ParamSchemaMap() map[string]any {
	return t.schemaMap
}

func (t *SchemaOnlyTool) RequiresPermission(rctx *rctx.ToolContext, params ToolCall) (bool, error) {
	return false, nil
}

func (t *SchemaOnlyTool) Run(rctx *rctx.ToolContext, params ToolCall) (ToolResponse, error) {
	// This should never be called - the workflow handles execution
	return NewTextErrorResponse(fmt.Sprintf("Tool %s is workflow-native and should not be executed directly", t.name)), nil
}
