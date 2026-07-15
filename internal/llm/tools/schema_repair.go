// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	"github.com/reliant-labs/reliant/internal/logging"
)

// This file implements schema-guided repair of "stringified JSON" tool inputs.
//
// Models (across providers) sometimes emit an array- or object-typed tool
// parameter as a JSON-encoded STRING containing the value, e.g.:
//
//	{"slides": "[{\"title\": \"Intro\"}]"}   instead of   {"slides": [{"title": "Intro"}]}
//
// Prior to this repair layer, JSON Schema validation hard-failed with
// `... has type "string", want "array"`, the model retried with the same
// encoding, and agent loops spun for dozens of round-trips.
//
// The repair is deliberately narrow:
//   - It only fires AFTER validation has already failed.
//   - It only rewrites a string value when the schema at that exact position
//     expects an array or object (never when the schema accepts a string).
//   - It walks the schema recursively (properties, items,
//     additionalProperties), so nested stringified values are repaired too.
//   - Double-encoded values (a JSON string whose content is itself a JSON
//     string) are unwrapped by a single bounded repair (see unwrapToType /
//     maxUnwrapDepth) — one repair per path, never a repair-of-a-repair.
//   - The repaired input is re-validated; if it still fails, the caller gets
//     the original input back along with the post-repair validation error.
//
// Both JSON-schema validation sites share this helper:
//   - regular tools:  ToolWrapper.Run (internal/llm/tools/tool_wrapper.go)
//   - response tools: executeResponseToolInline
//     (internal/workflow/runtime/activities/handlers/execute_tools.go)

// ValidateJSONAgainstSchema validates a JSON string against a JSON Schema
// document (raw JSON bytes) without attempting any repair.
func ValidateJSONAgainstSchema(jsonStr string, schemaJSON []byte) error {
	resolved, err := resolveSchemaJSON(schemaJSON)
	if err != nil {
		return err
	}

	var inputData interface{}
	if err := json.Unmarshal([]byte(jsonStr), &inputData); err != nil {
		return fmt.Errorf("failed to unmarshal input JSON: %w", err)
	}

	if err := resolved.Validate(inputData); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	return nil
}

// ValidateJSONWithRepair validates jsonStr against schemaJSON (a JSON Schema
// document). When validation fails, it attempts a schema-guided repair of
// stringified array/object values and re-validates.
//
// Returns the (possibly repaired) JSON string. A nil error means the returned
// JSON validates against the schema. On error, the original jsonStr is
// returned unchanged.
//
// When a repair fires, one INFO log is emitted with the tool name and the
// repaired property paths — telemetry for how often models emit stringified
// JSON, and from which tools.
func ValidateJSONWithRepair(toolName, jsonStr string, schemaJSON []byte) (string, error) {
	resolved, err := resolveSchemaJSON(schemaJSON)
	if err != nil {
		return jsonStr, err
	}

	var inputData interface{}
	if err := json.Unmarshal([]byte(jsonStr), &inputData); err != nil {
		return jsonStr, fmt.Errorf("failed to unmarshal input JSON: %w", err)
	}

	valErr := resolved.Validate(inputData)
	if valErr == nil {
		return jsonStr, nil
	}

	// Validation failed — attempt stringified-JSON repair guided by the schema.
	var schemaMap map[string]interface{}
	if err := json.Unmarshal(schemaJSON, &schemaMap); err != nil {
		return jsonStr, fmt.Errorf("validation failed: %w", valErr)
	}

	repairedData, repairedPaths := repairStringifiedJSON(inputData, schemaMap)
	if len(repairedPaths) == 0 {
		// Nothing repairable (e.g. the string isn't valid JSON of the expected
		// type, or the schema expects a string) — fail with the original error.
		return jsonStr, fmt.Errorf("validation failed: %w", valErr)
	}

	// Telemetry: log every repair, even if re-validation still fails — the
	// signal we want is "how often do models stringify values".
	logging.Info("Repaired stringified JSON in tool input",
		"tool", toolName,
		"properties", strings.Join(repairedPaths, ", "),
	)

	if err := resolved.Validate(repairedData); err != nil {
		// Repair fired but the input is still invalid for other reasons —
		// report the post-repair error (it's the more actionable one) and
		// hand back the original input.
		return jsonStr, fmt.Errorf("validation failed: %w", err)
	}

	repairedJSON, err := json.Marshal(repairedData)
	if err != nil {
		return jsonStr, fmt.Errorf("validation failed: %w", valErr)
	}
	return string(repairedJSON), nil
}

// resolveSchemaJSON parses and resolves a JSON Schema document for validation.
func resolveSchemaJSON(schemaJSON []byte) (*gojsonschema.Resolved, error) {
	var goSchema gojsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &goSchema); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
	}
	resolved, err := goSchema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve schema: %w", err)
	}
	return resolved, nil
}

// repairStringifiedJSON walks value against schema (both as generic decoded
// JSON) and replaces string values with their parsed JSON when the schema
// expects an array or object at that position — including the root value.
// Returns the (possibly replaced) value and the list of repaired paths
// ("$", "$.slides", "$.config.edits", "$.items[2]", ...).
func repairStringifiedJSON(value interface{}, schema map[string]interface{}) (interface{}, []string) {
	var repairedPaths []string
	out := repairValueAgainstSchema(value, schema, "$", &repairedPaths)
	return out, repairedPaths
}

func repairValueAgainstSchema(value interface{}, schema map[string]interface{}, path string, repairedPaths *[]string) interface{} {
	if schema == nil {
		return value
	}

	if s, ok := value.(string); ok {
		expected := schemaExpectedTypes(schema)
		// Never repair when the schema accepts a string here — a string that
		// happens to contain JSON is a legitimate value.
		if !expected["string"] {
			for _, want := range []string{"array", "object"} {
				if !expected[want] {
					continue
				}
				// unwrapToType peels string-encoding layers (bounded by
				// maxUnwrapDepth) until the decoded value matches the wanted
				// type, so a double-encoded value is fixed by this single
				// repair. Each path is repaired at most once.
				if parsed, ok := unwrapToType(s, want); ok {
					*repairedPaths = append(*repairedPaths, path)
					value = parsed
					break
				}
			}
		}
		if _, still := value.(string); still {
			// Not repairable — leave it for validation to reject cleanly.
			return value
		}
	}

	// Recurse into containers per the schema so nested stringified values
	// (including ones inside a value we just repaired) are handled too.
	switch v := value.(type) {
	case map[string]interface{}:
		props, _ := schema["properties"].(map[string]interface{})
		additional, _ := schema["additionalProperties"].(map[string]interface{})
		for key, child := range v {
			childSchema, _ := props[key].(map[string]interface{})
			if childSchema == nil {
				childSchema = additional
			}
			if childSchema != nil {
				v[key] = repairValueAgainstSchema(child, childSchema, path+"."+key, repairedPaths)
			}
		}
	case []interface{}:
		// Only the single-schema "items" form is supported (tuple-form
		// prefixItems is not used by our tool schemas).
		if items, ok := schema["items"].(map[string]interface{}); ok {
			for i, item := range v {
				v[i] = repairValueAgainstSchema(item, items, fmt.Sprintf("%s[%d]", path, i), repairedPaths)
			}
		}
	}
	return value
}

// schemaExpectedTypes returns the set of type names a schema position accepts.
// Handles both the string form (`"type": "array"`) and the list form
// (`"type": ["array", "null"]`).
func schemaExpectedTypes(schema map[string]interface{}) map[string]bool {
	types := map[string]bool{}
	switch t := schema["type"].(type) {
	case string:
		types[t] = true
	case []interface{}:
		for _, item := range t {
			if s, ok := item.(string); ok {
				types[s] = true
			}
		}
	}
	return types
}
