// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"reflect"
	"testing"
)

// deckPlanSchema mirrors the submit_deck_plan response tool shape from the
// production incident: 21 consecutive calls delivered `slides` as a
// JSON-encoded string instead of an array.
func deckPlanSchema() []byte {
	return []byte(`{
		"type": "object",
		"required": ["slides"],
		"properties": {
			"slides": {
				"type": "array",
				"items": {
					"type": "object",
					"required": ["title"],
					"properties": {
						"title":   {"type": "string"},
						"bullets": {"type": "array", "items": {"type": "string"}}
					}
				}
			}
		}
	}`)
}

func TestValidateJSONWithRepair(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		toolName   string
		input      string
		schema     []byte
		wantErr    bool
		wantOutput string // expected returned JSON (canonicalized); "" = expect input unchanged
	}{
		{
			name:       "valid input passes untouched",
			toolName:   "submit_deck_plan",
			input:      `{"slides": [{"title": "Intro"}]}`,
			schema:     deckPlanSchema(),
			wantErr:    false,
			wantOutput: `{"slides": [{"title": "Intro"}]}`,
		},
		{
			name:       "stringified array is repaired (the slides shape)",
			toolName:   "submit_deck_plan",
			input:      `{"slides": "[{\"title\": \"Intro\", \"bullets\": [\"a\", \"b\"]}]"}`,
			schema:     deckPlanSchema(),
			wantErr:    false,
			wantOutput: `{"slides": [{"title": "Intro", "bullets": ["a", "b"]}]}`,
		},
		{
			name:     "stringified object is repaired",
			toolName: "configure",
			input:    `{"config": "{\"retries\": 3}"}`,
			schema: []byte(`{
				"type": "object",
				"required": ["config"],
				"properties": {
					"config": {
						"type": "object",
						"properties": {"retries": {"type": "integer"}}
					}
				}
			}`),
			wantErr:    false,
			wantOutput: `{"config": {"retries": 3}}`,
		},
		{
			name:     "string that is not JSON still fails cleanly",
			toolName: "submit_deck_plan",
			input:    `{"slides": "definitely not json"}`,
			schema:   deckPlanSchema(),
			wantErr:  true,
		},
		{
			name:     "string that parses to the wrong JSON type still fails cleanly",
			toolName: "submit_deck_plan",
			input:    `{"slides": "{\"title\": \"an object, not an array\"}"}`,
			schema:   deckPlanSchema(),
			wantErr:  true,
		},
		{
			name:     "schema-expects-string is never repaired even when validation fails elsewhere",
			toolName: "submit_note",
			// `note` looks like JSON but the schema says string — it must stay
			// a string. Validation fails because required `slides` is missing.
			input:   `{"note": "[1, 2, 3]"}`,
			schema:  []byte(`{"type": "object", "required": ["note", "slides"], "properties": {"note": {"type": "string"}, "slides": {"type": "array"}}}`),
			wantErr: true,
		},
		{
			name:     "schema-expects-string value untouched when the rest validates",
			toolName: "submit_note",
			input:    `{"note": "[1, 2, 3]", "slides": []}`,
			schema:   []byte(`{"type": "object", "required": ["note", "slides"], "properties": {"note": {"type": "string"}, "slides": {"type": "array"}}}`),
			wantErr:  false,
			// No repair fires — the string stays a string.
			wantOutput: `{"note": "[1, 2, 3]", "slides": []}`,
		},
		{
			name:     "nested property repair",
			toolName: "submit_deck",
			input:    `{"deck": {"slides": "[{\"title\": \"Nested\"}]"}}`,
			schema: []byte(`{
				"type": "object",
				"required": ["deck"],
				"properties": {
					"deck": {
						"type": "object",
						"required": ["slides"],
						"properties": {
							"slides": {"type": "array", "items": {"type": "object", "properties": {"title": {"type": "string"}}}}
						}
					}
				}
			}`),
			wantErr:    false,
			wantOutput: `{"deck": {"slides": [{"title": "Nested"}]}}`,
		},
		{
			name:     "stringified value nested inside array items is repaired",
			toolName: "submit_deck_plan",
			// The array itself is real, but one slide's bullets arrived stringified.
			input:      `{"slides": [{"title": "Intro", "bullets": "[\"a\", \"b\"]"}]}`,
			schema:     deckPlanSchema(),
			wantErr:    false,
			wantOutput: `{"slides": [{"title": "Intro", "bullets": ["a", "b"]}]}`,
		},
		{
			name:     "double-encoded JSON (string containing a JSON string) repaired in one pass",
			toolName: "submit_deck_plan",
			// json-encode of the json-encode of the array.
			input:      `{"slides": "\"[{\\\"title\\\": \\\"Intro\\\"}]\""}`,
			schema:     deckPlanSchema(),
			wantErr:    false,
			wantOutput: `{"slides": [{"title": "Intro"}]}`,
		},
		{
			name:       "root-level stringified object is repaired",
			toolName:   "submit_deck_plan",
			input:      `"{\"slides\": [{\"title\": \"Intro\"}]}"`,
			schema:     deckPlanSchema(),
			wantErr:    false,
			wantOutput: `{"slides": [{"title": "Intro"}]}`,
		},
		{
			name:     "genuinely invalid input (missing required) is not masked by repair",
			toolName: "submit_deck_plan",
			input:    `{"something_else": true}`,
			schema:   deckPlanSchema(),
			wantErr:  true,
		},
		{
			name:     "repair fires but other validation errors still surface",
			toolName: "submit_deck_plan",
			// slides repairs to an array, but the slide is missing required title.
			input:   `{"slides": "[{\"bullets\": [\"a\"]}]"}`,
			schema:  deckPlanSchema(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateJSONWithRepair(tt.toolName, tt.input, tt.schema)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateJSONWithRepair() error = %v, wantErr %v (returned: %s)", err, tt.wantErr, got)
			}
			if tt.wantErr {
				// On failure the original input must come back unchanged.
				if got != tt.input {
					t.Errorf("ValidateJSONWithRepair() on error returned modified input:\n got: %s\nwant: %s", got, tt.input)
				}
				return
			}
			if tt.wantOutput != "" {
				assertJSONEqual(t, got, tt.wantOutput)
			}
		})
	}
}

// TestRepairStringifiedJSON_RepairedAtMostOncePerPath exercises the walk
// directly: a double-encoded value must be fixed by exactly ONE repair event
// (unwrapToType peels the layers inside a single repair), never a
// repair-of-a-repair.
func TestRepairStringifiedJSON_RepairedAtMostOncePerPath(t *testing.T) {
	t.Parallel()
	var schemaMap map[string]interface{}
	if err := json.Unmarshal(deckPlanSchema(), &schemaMap); err != nil {
		t.Fatal(err)
	}

	var value interface{}
	if err := json.Unmarshal([]byte(`{"slides": "\"[{\\\"title\\\": \\\"Intro\\\"}]\""}`), &value); err != nil {
		t.Fatal(err)
	}

	repaired, paths := repairStringifiedJSON(value, schemaMap)
	if len(paths) != 1 || paths[0] != "$.slides" {
		t.Fatalf("repairStringifiedJSON() repaired paths = %v, want exactly [$.slides]", paths)
	}
	m, ok := repaired.(map[string]interface{})
	if !ok {
		t.Fatalf("repaired value has type %T, want object", repaired)
	}
	if _, ok := m["slides"].([]interface{}); !ok {
		t.Fatalf("slides after repair has type %T, want array", m["slides"])
	}
}

// TestRepairStringifiedJSON_NeverRepairsStringSchemas exercises the "never
// repair when the schema expects string" rule directly at the walk level.
func TestRepairStringifiedJSON_NeverRepairsStringSchemas(t *testing.T) {
	t.Parallel()
	schemaMap := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"note":      map[string]interface{}{"type": "string"},
			"stringish": map[string]interface{}{"type": []interface{}{"string", "null"}},
			"untyped":   map[string]interface{}{},
		},
	}
	original := map[string]interface{}{
		"note":      `["looks", "like", "json"]`,
		"stringish": `{"also": "json"}`,
		"untyped":   `[1, 2]`,
	}
	value := map[string]interface{}{}
	for k, v := range original {
		value[k] = v
	}

	repaired, paths := repairStringifiedJSON(value, schemaMap)
	if len(paths) != 0 {
		t.Fatalf("repairStringifiedJSON() repaired paths = %v, want none", paths)
	}
	if !reflect.DeepEqual(repaired, original) {
		t.Errorf("repairStringifiedJSON() modified string-typed values:\n got: %#v\nwant: %#v", repaired, original)
	}
}

// assertJSONEqual compares two JSON documents structurally.
func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var gotVal, wantVal interface{}
	if err := json.Unmarshal([]byte(got), &gotVal); err != nil {
		t.Fatalf("got is not valid JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantVal); err != nil {
		t.Fatalf("want is not valid JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Errorf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}
