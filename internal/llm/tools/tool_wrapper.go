// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/logging"
	rctxpkg "github.com/reliant-labs/reliant/internal/rctx"
)

// ErrInvalidParameters is returned when tool parameters cannot be parsed
var ErrInvalidParameters = errors.New("invalid tool parameters")

// unwrapStringifiedValues fixes a Claude API bug where values are sometimes
// sent as JSON strings instead of their actual types when using MCP-prefixed tool names.
// Examples:
//   - {"edits": "[{\"file_path\": \"...\"}]"} -> {"edits": [{"file_path": "..."}]}
//   - {"limit": "5"} -> {"limit": 5}
//   - {"enabled": "true"} -> {"enabled": true}
//
// Only unwraps when schema expects a non-string type but we received a string.
// This avoids mangling legitimate string values like "true" or "123".
func unwrapStringifiedValues(jsonStr string, schema *jsonschema.Schema) string {
	if schema == nil || schema.Properties == nil {
		return jsonStr
	}

	// The whole argument object is sometimes delivered as a JSON string (the model
	// or an MCP serialization layer double-encodes it). Unwrap that outer layer
	// before looking at individual properties.
	jsonStr = unwrapStringifiedObject(jsonStr)

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return jsonStr
	}

	modified := false
	for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
		key := pair.Key
		propSchema := pair.Value

		// Only process if schema expects a non-string type
		if propSchema == nil || propSchema.Type == "" || propSchema.Type == "string" {
			continue
		}

		strVal, ok := data[key].(string)
		if !ok || len(strVal) == 0 {
			continue
		}

		// Peel away one or more layers of JSON string encoding. Values are
		// occasionally double-stringified (a JSON string whose contents are
		// themselves a JSON string), so keep unwrapping until the decoded value
		// matches the schema's expected non-string type.
		parsed, ok := unwrapToType(strVal, propSchema.Type)
		if !ok {
			continue
		}

		logging.Warn("unwrapStringifiedValues: Fixed stringified value",
			"key", key, "expected_type", propSchema.Type, "parsed_type", fmt.Sprintf("%T", parsed))
		data[key] = parsed
		modified = true
	}

	if !modified {
		return jsonStr
	}

	fixedBytes, err := json.Marshal(data)
	if err != nil {
		return jsonStr
	}
	return string(fixedBytes)
}

// maxUnwrapDepth bounds how many layers of JSON string encoding we peel away,
// guarding against pathological or adversarial deeply-nested inputs.
const maxUnwrapDepth = 5

// unwrapStringifiedObject handles the case where the entire tool argument
// payload arrived as a JSON-encoded string rather than a JSON object. It peels
// string layers until it reaches a JSON object, returning the object's JSON
// text. If the input is already an object (or never resolves to one) the
// original string is returned unchanged.
func unwrapStringifiedObject(jsonStr string) string {
	current := jsonStr
	for depth := 0; depth < maxUnwrapDepth; depth++ {
		trimmed := strings.TrimSpace(current)
		if len(trimmed) == 0 || trimmed[0] != '"' {
			return current
		}
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err != nil {
			return current
		}
		current = unquoted
	}
	return current
}

// unwrapToType parses a JSON string value, peeling repeated string encodings
// until the decoded value matches the expected schema type ("array", "object",
// "integer", "number", "boolean"). It returns the decoded value and true on
// success, or false if the string does not decode to the expected type.
func unwrapToType(strVal, expectedType string) (interface{}, bool) {
	current := strVal
	for depth := 0; depth < maxUnwrapDepth; depth++ {
		var parsed interface{}
		if err := json.Unmarshal([]byte(current), &parsed); err != nil {
			return nil, false
		}
		if jsonValueMatchesType(parsed, expectedType) {
			return parsed, true
		}
		// Another layer of string encoding — keep peeling.
		if next, ok := parsed.(string); ok {
			current = next
			continue
		}
		return nil, false
	}
	return nil, false
}

// jsonValueMatchesType reports whether a decoded JSON value matches the JSON
// Schema type name. Numbers decode as float64, so both "integer" and "number"
// accept float64.
func jsonValueMatchesType(v interface{}, expectedType string) bool {
	switch expectedType {
	case "array":
		_, ok := v.([]interface{})
		return ok
	case "object":
		_, ok := v.(map[string]interface{})
		return ok
	case "integer", "number":
		_, ok := v.(float64)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	default:
		return false
	}
}

// coerceKVArrayMaps converts any top-level fields that look like:
//
//	"field": [{"key":"k","value":<any>}, ...]
//
// into:
//
//	"field": {"k": <any>, ...}
//
// This is used to support providers (notably OpenAI Responses strict tool schemas)
// that cannot express maps in JSON Schema and instead advertise them as arrays
// of key/value objects.
//
// We deliberately accept this encoding for all providers to keep tool execution
// robust and backwards-compatible across schema adapters.
func coerceKVArrayMaps(jsonStr string) string {
	var root map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &root); err != nil {
		return jsonStr
	}

	modified := false
	for k, v := range root {
		arr, ok := v.([]any)
		if !ok {
			continue
		}

		m := map[string]any{}
		looksLikeKV := true
		for _, item := range arr {
			obj, ok := item.(map[string]any)
			if !ok {
				looksLikeKV = false
				break
			}
			key, ok := obj["key"].(string)
			if !ok || key == "" {
				looksLikeKV = false
				break
			}
			val, ok := obj["value"]
			if !ok {
				looksLikeKV = false
				break
			}
			m[key] = val
		}
		if !looksLikeKV {
			continue
		}

		root[k] = m
		modified = true
	}

	if !modified {
		return jsonStr
	}
	out, err := json.Marshal(root)
	if err != nil {
		return jsonStr
	}
	return string(out)
}

// Verify that ToolWrapper implements Tool interface
var _ Tool = (*ToolWrapper[any, any])(nil)

// genericTool is the interface that generic tools must implement
type genericTool[P any, O any] interface {
	Name() string
	Description() string
	RequiresPermission(params P) (bool, error)
	Execute(rctx *rctxpkg.ToolContext, params P) (O, error)
}

// ToolWrapper implements Tool interface while wrapping a generic tool
type ToolWrapper[P any, O any] struct {
	tool       genericTool[P, O]
	paramsType reflect.Type
	outputType reflect.Type
}

// NewToolWrapper creates a new tool wrapper
func NewToolWrapper[P any, O any](t genericTool[P, O]) *ToolWrapper[P, O] {
	var p P
	var o O

	return &ToolWrapper[P, O]{
		tool:       t,
		paramsType: reflect.TypeOf(p),
		outputType: reflect.TypeOf(o),
	}
}

// Unwrap returns the inner tool implementation for interface type assertions.
func (t *ToolWrapper[P, O]) Unwrap() any {
	return t.tool
}

// Name returns the tool name
func (t *ToolWrapper[P, O]) Name() string {
	return t.tool.Name()
}

// Description returns the tool description
func (t *ToolWrapper[P, O]) Description() string {
	return t.tool.Description()
}

// ParamSchema returns the JSON schema for parameters
func (t *ToolWrapper[P, O]) ParamSchema() *jsonschema.Schema {
	var p P
	// OpenAI Responses / Structured Outputs require a strict subset of JSON Schema.
	// The openai-go docs recommend:
	// - AllowAdditionalProperties=false
	// - DoNotReference=true
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	return ResolveSchemaRefs(reflector.Reflect(&p))
}

// RequiresPermission returns permission requirements for the tool call
func (t *ToolWrapper[P, O]) RequiresPermission(rctx *rctxpkg.ToolContext, call ToolCall) (bool, error) {
	var typedParams P
	decoder := json.NewDecoder(strings.NewReader(call.Input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&typedParams); err != nil {
		logging.Warn("RequiresPermission: unmarshaling failed", "tool", t.tool.Name(), "error", err, "input", truncateString(call.Input, 200))
		return false, fmt.Errorf("%w: %v", ErrInvalidParameters, err)
	}
	return t.tool.RequiresPermission(typedParams)
}

// Run converts ToolCall to typed parameters and executes the tool
func (t *ToolWrapper[P, O]) Run(rctx *rctxpkg.ToolContext, call ToolCall) (ToolResponse, error) {
	toolName := t.tool.Name()

	// Normalize input:
	// 1) Fix Claude API bug with stringified values
	// 2) Accept OpenAI-compatible encoding for maps (kv array) and convert back to objects
	schema := t.ParamSchema()
	normalizedInput := unwrapStringifiedValues(call.Input, schema)
	normalizedInput = coerceKVArrayMaps(normalizedInput)

	// Validate input against JSON Schema. When validation fails because the
	// model emitted an array/object value as a JSON-encoded string, the shared
	// repair helper (schema_repair.go) fixes it in place and re-validates.
	if schema != nil {
		repairedInput, err := validateJSONSchemaWithRepair(toolName, normalizedInput, schema)
		if err != nil {
			errMsg := fmt.Sprintf("JSON Schema validation failed: %v", err)
			logging.Warn("Tool input schema validation failed", "tool", toolName, "error", err, "input", truncateString(normalizedInput, 500))
			return NewTextErrorResponse(errMsg), nil
		}
		normalizedInput = repairedInput
	}

	// Unmarshal to typed params with strict validation
	var typedParams P

	// Log raw input for debugging
	logging.Debug("Tool input unmarshaling", "tool", toolName, "input_length", len(normalizedInput), "input_preview", truncateString(normalizedInput, 500))

	decoder := json.NewDecoder(strings.NewReader(normalizedInput))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&typedParams); err != nil {
		errMsg := fmt.Sprintf("%v: %v", ErrInvalidParameters, err)
		logging.Warn("Tool input unmarshaling failed", "tool", toolName, "error", err, "input", normalizedInput)
		return NewTextErrorResponse(errMsg), nil
	}

	// Log successfully parsed params for debugging
	logging.Debug("Tool params unmarshaled successfully", "tool", toolName, "params_type", fmt.Sprintf("%T", typedParams))

	// Create or update tool call context
	toolCallCtx := &ctxkeys.ToolCallContext{
		CurrentToolCallID: call.ID,
	}
	// Preserve parent tool call ID if it exists
	if existingCtx, ok := rctx.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && existingCtx != nil {
		toolCallCtx.ParentToolCallID = existingCtx.ParentToolCallID
	}
	ctxWithToolCall := context.WithValue(rctx.Context, ctxkeys.ToolCallContextKey, toolCallCtx)
	rctxCopy := *rctx // Copy all fields
	rctxCopy.Context = ctxWithToolCall
	rctxWithToolCall := &rctxCopy

	// Execute with typed params and updated context
	output, err := t.tool.Execute(rctxWithToolCall, typedParams)

	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	// Convert output to ToolResponse
	// Since we're using generics, we need to use reflection or interface conversion
	outputInterface := interface{}(output)

	// Handle ToolResponse type
	if response, ok := outputInterface.(ToolResponse); ok {
		// Check and potentially truncate the content
		if response.Content != "" {
			truncatedContent, wasTruncated, err := CheckOutputSize(toolName, response.Content)
			if err != nil {
				// Output is too large - truncate it
				response.Content = TruncateOutput(toolName, response.Content, true)
				// Add truncation info to metadata
				if response.Metadata == "" {
					response.Metadata = fmt.Sprintf("Output truncated from %d bytes", len(response.Content))
				} else {
					response.Metadata += fmt.Sprintf("; Output truncated from %d bytes", len(response.Content))
				}
			} else if wasTruncated {
				response.Content = truncatedContent
			}
		}
		return response, nil
	}

	// If output is a string, wrap it in a text response
	if str, ok := outputInterface.(string); ok {
		// Check output size before creating response
		truncatedStr, wasTruncated, err := CheckOutputSize(toolName, str)
		if err != nil {
			// Output is too large - truncate it
			str = TruncateOutput(toolName, str, true)
		} else if wasTruncated {
			str = truncatedStr
		}
		return NewTextResponse(str), nil
	}

	// Otherwise, marshal to JSON and return as text
	jsonBytes, err := json.Marshal(output)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to marshal output: %v", err)), nil
	}

	// Check JSON output size
	jsonStr := string(jsonBytes)
	truncatedJson, wasTruncated, sizeErr := CheckOutputSize(toolName, jsonStr)
	if sizeErr != nil {
		// Output is too large - truncate it
		jsonStr = TruncateOutput(toolName, jsonStr, true)
	} else if wasTruncated {
		jsonStr = truncatedJson
	}

	return NewTextResponse(jsonStr), nil
}

// ResolveSchemaRefs replaces $ref references with their actual definitions
// This is needed because some LLM APIs don't support JSON Schema references
func ResolveSchemaRefs(schema *jsonschema.Schema) *jsonschema.Schema {
	// First check if we even have definitions to resolve
	if schema == nil {
		return schema
	}

	// Convert schema to a map for easier manipulation
	var schemaMap map[string]interface{}
	data, err := json.Marshal(schema)
	if err != nil {
		return schema
	}

	if err := json.Unmarshal(data, &schemaMap); err != nil {
		return schema
	}

	// Get definitions map from either location
	var defs map[string]interface{}
	if d, ok := schemaMap["$defs"].(map[string]interface{}); ok {
		defs = d
	} else if d, ok := schemaMap["definitions"].(map[string]interface{}); ok {
		defs = d
	}

	// If no definitions, nothing to resolve
	if len(defs) == 0 {
		return schema
	}

	// Handle top-level $ref if present
	if ref, ok := schemaMap["$ref"].(string); ok {
		var defName string
		if strings.HasPrefix(ref, "#/$defs/") {
			defName = strings.TrimPrefix(ref, "#/$defs/")
		} else if strings.HasPrefix(ref, "#/definitions/") {
			defName = strings.TrimPrefix(ref, "#/definitions/")
		}

		if defName != "" {
			if defValue, exists := defs[defName]; exists {
				// Replace the entire schema with the definition content
				delete(schemaMap, "$ref")
				if defMap, ok := defValue.(map[string]interface{}); ok {
					// Copy all fields from the definition to the top level
					for k, v := range defMap {
						schemaMap[k] = v
					}
				}
			}
		}
	}

	// Recursively resolve references
	resolveRefsInValue(schemaMap, defs)

	// Remove definitions since they're now inlined
	delete(schemaMap, "$defs")
	delete(schemaMap, "definitions")

	// Convert back to schema
	resolvedData, err := json.Marshal(schemaMap)
	if err != nil {
		return schema
	}

	var resolved jsonschema.Schema
	if err := json.Unmarshal(resolvedData, &resolved); err != nil {
		return schema
	}

	return &resolved
}

// resolveRefsInValue recursively resolves $ref in any value type
func resolveRefsInValue(obj interface{}, defs map[string]interface{}) {
	// Use a map to track resolved refs to prevent infinite recursion
	resolved := make(map[string]bool)
	resolveRefsInValueWithTracker(obj, defs, resolved)
}

// resolveRefsInValueWithTracker handles the actual resolution with cycle detection
func resolveRefsInValueWithTracker(obj interface{}, defs map[string]interface{}, resolved map[string]bool) {
	switch v := obj.(type) {
	case map[string]interface{}:
		// Check if this is a $ref
		if ref, ok := v["$ref"].(string); ok {
			// Skip if we've already resolved this ref (prevents infinite recursion)
			if resolved[ref] {
				return
			}

			var defName string
			if strings.HasPrefix(ref, "#/$defs/") {
				defName = strings.TrimPrefix(ref, "#/$defs/")
			} else if strings.HasPrefix(ref, "#/definitions/") {
				defName = strings.TrimPrefix(ref, "#/definitions/")
			}

			if defName != "" {
				if defValue, exists := defs[defName]; exists {
					// Mark this ref as being resolved
					resolved[ref] = true

					// Replace the entire map with the definition content
					delete(v, "$ref")
					if defMap, ok := defValue.(map[string]interface{}); ok {
						// Make a deep copy of the definition to avoid modifying the original
						defCopy := deepCopyMap(defMap)
						// First resolve any refs in the definition copy
						resolveRefsInValueWithTracker(defCopy, defs, resolved)
						// Then copy all fields
						for k, val := range defCopy {
							v[k] = val
						}
					}
					// Unmark to allow reuse in other places
					delete(resolved, ref)
				}
			}
		} else {
			// Not a ref, recursively process all values
			for _, val := range v {
				resolveRefsInValueWithTracker(val, defs, resolved)
			}
		}

	case []interface{}:
		// Process array elements
		for _, item := range v {
			resolveRefsInValueWithTracker(item, defs, resolved)
		}
	}
}

// deepCopyMap creates a deep copy of a map
func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	copy := make(map[string]interface{})
	for k, v := range m {
		switch val := v.(type) {
		case map[string]interface{}:
			copy[k] = deepCopyMap(val)
		case []interface{}:
			copy[k] = deepCopySlice(val)
		default:
			copy[k] = val
		}
	}
	return copy
}

// deepCopySlice creates a deep copy of a slice
func deepCopySlice(s []interface{}) []interface{} {
	copy := make([]interface{}, len(s))
	for i, v := range s {
		switch val := v.(type) {
		case map[string]interface{}:
			copy[i] = deepCopyMap(val)
		case []interface{}:
			copy[i] = deepCopySlice(val)
		default:
			copy[i] = val
		}
	}
	return copy
}

// truncateString truncates a string to a maximum length for logging
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}

// validateJSONSchema validates a JSON string against a JSON Schema (no repair).
func validateJSONSchema(jsonStr string, schema *jsonschema.Schema) error {
	// Convert invopop/jsonschema to raw JSON for google/jsonschema-go
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}
	return ValidateJSONAgainstSchema(jsonStr, schemaBytes)
}

// validateJSONSchemaWithRepair validates a JSON string against a JSON Schema,
// repairing stringified array/object values (see schema_repair.go) before
// failing. Returns the (possibly repaired) JSON string.
func validateJSONSchemaWithRepair(toolName, jsonStr string, schema *jsonschema.Schema) (string, error) {
	// Convert invopop/jsonschema to raw JSON for google/jsonschema-go
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return jsonStr, fmt.Errorf("failed to marshal schema: %w", err)
	}
	return ValidateJSONWithRepair(toolName, jsonStr, schemaBytes)
}
