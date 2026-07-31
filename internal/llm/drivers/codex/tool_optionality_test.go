// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
)

// The schema codex actually puts on the wire is what decides whether the model
// is allowed to omit an optional parameter. chrome-devtools' evaluate_script
// has exactly one required parameter; if we ship it with four, the model fills
// the other three with zero values and the MCP server rejects the call.
func TestConvertTools_PreservesDeclaredOptionality(t *testing.T) {
	tool := llmtools.NewSchemaOnlyTool(
		"mcp__chrome-devtools__evaluate_script",
		"Evaluate a JavaScript function inside the currently selected page.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"function":     map[string]interface{}{"type": "string"},
				"args":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"filePath":     map[string]interface{}{"type": "string"},
				"dialogAction": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"function"},
		},
	)

	client := &CodexClient{}
	converted, err := client.convertTools([]llmtools.Tool{tool})
	if err != nil {
		t.Fatalf("convertTools: %v", err)
	}
	if len(converted) != 1 {
		t.Fatalf("expected 1 converted tool, got %d", len(converted))
	}

	raw, err := json.Marshal(converted[0].OfFunction.Parameters)
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}

	props, _ := params["properties"].(map[string]any)
	if len(props) != 4 {
		t.Fatalf("all four parameters must reach the model, got %v", params["properties"])
	}

	got := make([]string, 0)
	if arr, ok := params["required"].([]any); ok {
		for _, v := range arr {
			got = append(got, v.(string))
		}
	}
	sort.Strings(got)
	want := []string{"function"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %v, want %v — optional parameters must be advertised as optional", got, want)
	}
}
