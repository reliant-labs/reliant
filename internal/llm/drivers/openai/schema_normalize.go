// Copyright (c) 2025 Reliant Labs
package openai

import (
	"fmt"
	"sort"
)

// OpenAI Responses strict schemas cannot express maps (additionalProperties schemas).
// We encode them as an array of {key,value} objects.
//
// This keeps the schema valid while allowing us to coerce the model-produced
// args back into a map at execution time.
func rewriteMapSchemaToKVArray(schema map[string]any) bool {
	if schema == nil {
		return false
	}

	// Heuristic for map/unknown-object schemas:
	// - type: object
	// - no explicit properties
	// - additionalProperties is present (schema/true/false)
	//
	// This covers:
	// - invopop/jsonschema output for map fields (additionalProperties: <schema>)
	// - the stricter shapes we may end up with after other normalization steps
	//   (additionalProperties: false)
	t, _ := schema["type"].(string)
	if t != "object" {
		return false
	}
	if schema["properties"] != nil {
		return false
	}
	ap, ok := schema["additionalProperties"]
	if !ok || ap == nil {
		return false
	}

	// Determine value type if provided.
	// OpenAI requires every schema object to have a 'type'.
	// For arbitrary metadata values, represent them as strings (JSON-encoded) rather than "any".
	valueSchema := map[string]any{"type": "string"}
	if v, ok := ap.(map[string]any); ok {
		if vt, ok := v["type"].(string); ok && vt != "" {
			valueSchema = map[string]any{"type": vt}
		}
	} else if v, ok := ap.(map[string]interface{}); ok {
		// Copy to map[string]any
		if vt, ok := v["type"].(string); ok && vt != "" {
			valueSchema = map[string]any{"type": vt}
		}
	}

	// Rewrite in-place.
	for k := range schema {
		delete(schema, k)
	}
	schema["type"] = "array"
	schema["items"] = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"key":   map[string]any{"type": "string"},
			"value": valueSchema,
		},
		"required": []any{"key", "value"},
	}
	return true
}

// stripRequired removes a required field if present. OpenAI Responses rejects schemas where
// 'required' contains keys not present in properties.
func stripRequired(schema map[string]any) {
	if schema == nil {
		return
	}
	delete(schema, "required")
}

// NormalizeResponsesToolSchema mutates the given JSON-schema-ish map into the strict subset
// required by OpenAI Responses when using function tools with strict=true.
//
// Known constraints enforced here:
// - Every object schema must set additionalProperties=false
// - If an object schema has properties, it must also have required containing *every* property key
// - required must not include keys that are not present in properties
//
// This function is intentionally conservative and schema-shape-agnostic; it only
// touches object/array/combinator nodes and recurses.
func NormalizeResponsesToolSchema(schema map[string]any) {
	if schema == nil {
		return
	}

	// If we have a map schema but it's missing additionalProperties (or has already been flattened),
	// we still must ensure it doesn't carry an invalid 'required'. The safe move is to drop it.
	// (If it's truly a map schema, rewriteMapSchemaToKVArray will handle it; otherwise dropping
	// required just avoids OpenAI schema validation errors.)
	stripRequired(schema)

	// Map schemas must be rewritten before we enforce strict object rules.
	if rewriteMapSchemaToKVArray(schema) {
		// We rewrote to array form; do not treat it as an object schema.
		// Still recurse into nested nodes below.
	} else {
		// If this looks like an object schema, enforce strict object rules.
		if schema["type"] == "object" || schema["properties"] != nil {
			// Always close objects.
			schema["additionalProperties"] = false

			if props, ok := schema["properties"].(map[string]any); ok && props != nil {
				keys := make([]string, 0, len(props))
				for k := range props {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				req := make([]any, 0, len(keys))
				for _, k := range keys {
					req = append(req, k)
				}
				schema["required"] = req
			}
		}
	}

	// If this is an object schema with additionalProperties=false but no explicit properties,
	// we need to add empty properties to satisfy OpenAI's strict validation.
	// We cannot rewrite to array here because OpenAI requires root tool parameters to be type=object.
	// (Previously we rewrote to kv array, but that breaks for root-level schemas.)
	if schema["type"] == "object" && schema["properties"] == nil {
		if ap, ok := schema["additionalProperties"]; ok {
			if ap == false {
				// Set empty properties and required to make it a valid empty object schema
				schema["properties"] = map[string]any{}
				schema["required"] = []any{}
			}
		}
	}

	// Recurse into properties
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, v := range props {
			if child, ok := v.(map[string]any); ok {
				NormalizeResponsesToolSchema(child)
			}
		}
	}

	// Recurse into array items
	if items, ok := schema["items"].(map[string]any); ok {
		NormalizeResponsesToolSchema(items)
	}

	// Recurse into combinators
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schema[key].([]any); ok {
			for _, v := range arr {
				if child, ok := v.(map[string]any); ok {
					NormalizeResponsesToolSchema(child)
				}
			}
		}
	}
}

// ValidateResponsesToolSchemaStrict validates the OpenAI Responses strict subset invariants.
// Returns nil if schema is acceptable.
func ValidateResponsesToolSchemaStrict(schema map[string]any) error {
	if schema == nil {
		return fmt.Errorf("schema is nil")
	}
	return validateResponsesToolSchemaStrictAtPath(schema, "$")
}

// SanitizeChatToolSchema mutates a chat-completions tool schema into a safer
// OpenAI-compatible subset for gateway stacks that choke on boolean JSON Schema
// nodes (for example additionalProperties=false or items=false nested inside
// property definitions).
//
// Unlike NormalizeResponsesToolSchema, this is intentionally conservative:
// - root schema handling happens in ConvertTools
// - nested object/array/combinator structure is preserved where possible
// - boolean schema nodes are deleted or replaced with empty object schemas
func SanitizeChatToolSchema(schema map[string]any) {
	if schema == nil {
		return
	}
	for key, raw := range schema {
		switch key {
		case "additionalProperties":
			if _, ok := raw.(bool); ok {
				delete(schema, key)
				continue
			}
		case "items", "contains", "not", "if", "then", "else", "unevaluatedProperties", "propertyNames":
			if _, ok := raw.(bool); ok {
				schema[key] = map[string]any{}
				continue
			}
		}

		schema[key] = sanitizeChatToolSchemaValue(raw)
	}
}

func sanitizeChatToolSchemaValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		SanitizeChatToolSchema(v)
		return v
	case []any:
		for i, item := range v {
			if _, ok := item.(bool); ok {
				v[i] = map[string]any{}
				continue
			}
			v[i] = sanitizeChatToolSchemaValue(item)
		}
		return v
	default:
		return value
	}
}

func validateResponsesToolSchemaStrictAtPath(schema map[string]any, path string) error {
	if schema == nil {
		return fmt.Errorf("%s: schema is nil", path)
	}

	if schema["type"] == "object" || schema["properties"] != nil {
		ap, ok := schema["additionalProperties"]
		if !ok {
			return fmt.Errorf("%s: missing additionalProperties", path)
		}
		if ap != false {
			return fmt.Errorf("%s: additionalProperties must be false, got %#v", path, ap)
		}

		if props, ok := schema["properties"].(map[string]any); ok && props != nil {
			requiredSet := map[string]bool{}
			switch r := schema["required"].(type) {
			case []any:
				for _, v := range r {
					if s, ok := v.(string); ok {
						requiredSet[s] = true
					}
				}
			case []string:
				for _, s := range r {
					requiredSet[s] = true
				}
			default:
				return fmt.Errorf("%s: required must be present as array when properties exist", path)
			}

			for k := range props {
				if !requiredSet[k] {
					return fmt.Errorf("%s: required missing property %q", path, k)
				}
			}
		}
	}

	if props, ok := schema["properties"].(map[string]any); ok {
		for k, v := range props {
			if child, ok := v.(map[string]any); ok {
				if err := validateResponsesToolSchemaStrictAtPath(child, path+".properties."+k); err != nil {
					return err
				}
			}
		}
	}

	if rawItems, exists := schema["items"]; exists && rawItems != nil {
		items, ok := rawItems.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: items must be an object schema, got %T", path, rawItems)
		}
		if err := validateResponsesToolSchemaStrictAtPath(items, path+".items"); err != nil {
			return err
		}
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schema[key].([]any); ok {
			for i, v := range arr {
				if child, ok := v.(map[string]any); ok {
					if err := validateResponsesToolSchemaStrictAtPath(child, fmt.Sprintf("%s.%s[%d]", path, key, i)); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}