// Copyright (c) 2025 Reliant Labs
package openai

import (
	"fmt"
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

// requiredNames reads a JSON Schema 'required' value in either shape it can
// arrive in: []any after a JSON round-trip, or []string when built in Go.
func requiredNames(raw any) []string {
	switch r := raw.(type) {
	case []any:
		names := make([]string, 0, len(r))
		for _, v := range r {
			if s, ok := v.(string); ok {
				names = append(names, s)
			}
		}
		return names
	case []string:
		return append([]string(nil), r...)
	default:
		return nil
	}
}

// pruneRequired keeps the schema's declared 'required' list but drops entries
// that do not name a real property. That is the only constraint OpenAI Responses
// actually imposes here: it rejects a schema whose 'required' references a key
// absent from 'properties'.
//
// It deliberately does NOT invent entries. Which parameters are optional is the
// tool author's decision and the only channel through which a model can be told
// it may omit one. A model that is told every parameter is mandatory answers
// with a zero value — "" for a string, false for a boolean, 0 for a number —
// and a zero value is a different request from an absent key for any server
// that treats "not provided" as its own case.
func pruneRequired(schema map[string]any) {
	if schema == nil {
		return
	}
	raw, ok := schema["required"]
	if !ok {
		return
	}

	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		// Nothing a required entry could legitimately name.
		delete(schema, "required")
		return
	}

	kept := make([]any, 0, len(props))
	for _, name := range requiredNames(raw) {
		if _, exists := props[name]; exists {
			kept = append(kept, name)
		}
	}
	schema["required"] = kept
}

// NormalizeResponsesToolSchema mutates the given JSON-schema-ish map into the
// shape OpenAI Responses accepts for function tools.
//
// Known constraints enforced here:
// - Root parameters must be an object schema
// - Every object schema sets additionalProperties=false, so the model does not invent keys
// - Map schemas (additionalProperties with no properties) become {key,value} arrays
// - required must not include keys that are not present in properties
//
// It preserves the tool's declared optionality: a parameter the tool did not
// mark required stays optional, so the model can leave it out of the call. See
// pruneRequired for why that distinction is load-bearing.
//
// This function is intentionally conservative and schema-shape-agnostic; it only
// touches object/array/combinator nodes and recurses.
func NormalizeResponsesToolSchema(schema map[string]any) {
	if schema == nil {
		return
	}

	// Keep the declared required list, minus entries naming no real property.
	// A map schema's stray 'required' is irrelevant — rewriteMapSchemaToKVArray
	// clears the node entirely.
	pruneRequired(schema)

	// Map schemas must be rewritten before we enforce object rules.
	if rewriteMapSchemaToKVArray(schema) {
		// We rewrote to array form; do not treat it as an object schema.
		// Still recurse into nested nodes below.
	} else if schema["type"] == "object" || schema["properties"] != nil {
		// Always close objects.
		schema["additionalProperties"] = false
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

// ValidateResponsesToolSchema validates the invariants OpenAI Responses imposes
// on function tool parameters. Returns nil if schema is acceptable.
//
// Failing this check makes the caller drop the tool's entire parameter list, so
// it must assert only what the API actually rejects. In particular a tool with
// optional parameters is valid: 'required' is a subset of 'properties', not a
// copy of it.
func ValidateResponsesToolSchema(schema map[string]any) error {
	if schema == nil {
		return fmt.Errorf("schema is nil")
	}
	return validateResponsesToolSchemaAtPath(schema, "$")
}

func validateResponsesToolSchemaAtPath(schema map[string]any, path string) error {
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
			for _, name := range requiredNames(schema["required"]) {
				if _, exists := props[name]; !exists {
					return fmt.Errorf("%s: required names %q which is not a property", path, name)
				}
			}
		}
	}

	if props, ok := schema["properties"].(map[string]any); ok {
		for k, v := range props {
			if child, ok := v.(map[string]any); ok {
				if err := validateResponsesToolSchemaAtPath(child, path+".properties."+k); err != nil {
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
		if err := validateResponsesToolSchemaAtPath(items, path+".items"); err != nil {
			return err
		}
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schema[key].([]any); ok {
			for i, v := range arr {
				if child, ok := v.(map[string]any); ok {
					if err := validateResponsesToolSchemaAtPath(child, fmt.Sprintf("%s.%s[%d]", path, key, i)); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}
