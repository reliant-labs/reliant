// Copyright (c) 2025 Reliant Labs
package openai

import (
	"reflect"
	"sort"
	"testing"
)

// evaluateScriptSchema mirrors chrome-devtools-mcp's evaluate_script input
// schema: one required parameter and three optional ones, two of which the
// server treats as "absent means do nothing" (filePath) or "absent means use
// my own default" (dialogAction).
//
// A payload of {"filePath": ""} is NOT the same request as one that omits
// filePath: the server resolves "" against its own cwd and denies the write.
func evaluateScriptSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"function":     map[string]any{"type": "string"},
			"args":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"filePath":     map[string]any{"type": "string"},
			"dialogAction": map[string]any{"type": "string"},
		},
		"required": []any{"function"},
	}
}

func requiredNamesForTest(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, ok := schema["required"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("required should be []any, got %T (%#v)", raw, raw)
	}
	names := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("required entry should be a string, got %T", v)
		}
		names = append(names, s)
	}
	sort.Strings(names)
	return names
}

// The model can only omit a parameter if we told it the parameter is optional.
// Advertising every property as required is how "no filePath" became
// "filePath": "" on the wire.
func TestNormalizeResponsesToolSchema_KeepsOptionalParametersOptional(t *testing.T) {
	schema := evaluateScriptSchema()

	NormalizeResponsesToolSchema(schema)

	got := requiredNamesForTest(t, schema)
	want := []string{"function"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %v, want %v (optional parameters must stay optional)", got, want)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) != 4 {
		t.Fatalf("properties must survive normalization, got %#v", schema["properties"])
	}
}

// Same contract for the other two types that have a meaningful zero: an
// optional boolean must not be forced to false, an optional number must not be
// forced to 0.
func TestNormalizeResponsesToolSchema_KeepsOptionalNumberAndBooleanOptional(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verbose":  map[string]any{"type": "boolean"},
			"timeout":  map[string]any{"type": "number"},
			"filePath": map[string]any{"type": "string"},
		},
		"required": []any{},
	}

	NormalizeResponsesToolSchema(schema)

	if got := requiredNamesForTest(t, schema); len(got) != 0 {
		t.Fatalf("required = %v, want [] (a schema with no required parameters must stay that way)", got)
	}
}

// A nested object's declared optionality matters just as much as the root's.
func TestNormalizeResponsesToolSchema_KeepsNestedOptionalParametersOptional(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"enabled": map[string]any{"type": "boolean"},
					"label":   map[string]any{"type": "string"},
				},
				"required": []any{"enabled"},
			},
		},
		"required": []any{"config"},
	}

	NormalizeResponsesToolSchema(schema)

	config := schema["properties"].(map[string]any)["config"].(map[string]any)
	got := requiredNamesForTest(t, config)
	want := []string{"enabled"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nested required = %v, want %v", got, want)
	}
}

// The one real constraint the old blanket-strip was protecting against:
// OpenAI rejects a schema whose required names a property that does not exist.
// Filter those out instead of throwing the whole list away.
func TestNormalizeResponsesToolSchema_DropsRequiredNamingUnknownProperty(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"function": map[string]any{"type": "string"},
		},
		"required": []any{"function", "ghost"},
	}

	NormalizeResponsesToolSchema(schema)

	got := requiredNamesForTest(t, schema)
	want := []string{"function"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %v, want %v", got, want)
	}
	if err := ValidateResponsesToolSchema(schema); err != nil {
		t.Fatalf("normalized schema must validate: %v", err)
	}
}

// The validator gates a fallback that replaces the tool's schema with an empty
// object — every parameter gone. It must not fire on the ordinary case of a
// tool that has optional parameters.
func TestValidateResponsesToolSchema_AcceptsPartiallyRequiredObject(t *testing.T) {
	schema := evaluateScriptSchema()
	NormalizeResponsesToolSchema(schema)

	if err := ValidateResponsesToolSchema(schema); err != nil {
		t.Fatalf("a tool with optional parameters must validate, got: %v", err)
	}
}

func TestValidateResponsesToolSchema_RejectsRequiredNamingUnknownProperty(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"function": map[string]any{"type": "string"},
		},
		"required": []any{"ghost"},
	}

	if err := ValidateResponsesToolSchema(schema); err == nil {
		t.Fatal("expected required naming an unknown property to be rejected")
	}
}
