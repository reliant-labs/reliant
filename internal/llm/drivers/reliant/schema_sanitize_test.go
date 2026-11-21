package reliant

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
)

// A boolean `items: true` under a one-branch anyOf used to fail validation and
// throw the whole schema away. Normalization repairs it instead: the union of
// one is its branch, and `true` (any value) is an untyped items schema, as
// hermes-agent's legacy Gemini translator sends it.
func TestGeminiCompatibleToolParameters_RepairsBooleanItemsUnderAnyOf(t *testing.T) {
	broken := llmtools.NewSchemaOnlyTool(
		"mcp__broken__tool",
		"Broken tool",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"params": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"application/json": map[string]any{
							"anyOf": []any{
								map[string]any{
									"type":  "array",
									"items": true,
								},
							},
						},
					},
				},
			},
		},
	)

	params := geminiCompatibleToolParameters(broken)
	got, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	want := `{"properties":{"params":{"properties":{"application/json":{"items":{},"type":"array"}},"type":"object"}},"required":[],"type":"object"}`
	if string(got) != want {
		t.Fatalf("params = %s, want %s", got, want)
	}
}

// What the gateway cannot get from the typed schema model: a $ref, a type
// array with null and a required naming an undeclared property all reach
// LiteLLM already in Gemini's shapes.
func TestGeminiCompatibleToolParameters_NormalizesMCPSchema(t *testing.T) {
	tool := llmtools.NewSchemaOnlyTool("mcp__x__tool", "x", map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
		"properties": map[string]any{
			"when":  map[string]any{"type": []any{"string", "null"}, "format": "date-time"},
			"point": map[string]any{"$ref": "#/definitions/Point"},
		},
		"required":    []any{"point", "ghost"},
		"definitions": map[string]any{"Point": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "number"}}}},
	})
	got, err := json.Marshal(geminiCompatibleToolParameters(tool))
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	want := `{"properties":{"point":{"properties":{"x":{"type":"number"}},"type":"object"},"when":{"format":"date-time","nullable":true,"type":"string"}},"required":["point"],"type":"object"}`
	if string(got) != want {
		t.Fatalf("params = %s, want %s", got, want)
	}
}

// A root that is not an object cannot be a function's parameters; it still
// falls back to the safe empty object.
func TestGeminiCompatibleToolParameters_FallsBackForNonObjectRoot(t *testing.T) {
	tool := llmtools.NewSchemaOnlyTool("mcp__x__tool", "x", map[string]any{"type": "string"})
	params := geminiCompatibleToolParameters(tool)
	if params["additionalProperties"] != false {
		t.Fatalf("expected fallback additionalProperties=false, got %#v", params["additionalProperties"])
	}
	if props, _ := params["properties"].(map[string]any); len(props) != 0 {
		t.Fatalf("expected fallback empty properties, got %#v", props)
	}
}

func TestReliantConvertTools_UsesGeminiSanitizer(t *testing.T) {
	client := NewClient(llm.DriverOptions{
		Model: models.Model{
			ID:       models.ModelID("gemini-2.5-pro"),
			APIModel: "gemini-2.5-pro",
		},
	})

	broken := llmtools.NewSchemaOnlyTool(
		"mcp__broken__tool",
		"Broken tool",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"params": map[string]any{
					"anyOf": []any{
						map[string]any{
							"type":  "array",
							"items": true,
						},
					},
				},
			},
		},
	)

	converted := client.ConvertTools([]llmtools.Tool{broken})
	if len(converted) != 1 || converted[0].OfFunction == nil {
		t.Fatalf("expected one function tool")
	}

	data, err := json.Marshal(converted[0].OfFunction.Function.Parameters)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(data, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	props, _ := params["properties"].(map[string]any)
	param, _ := props["params"].(map[string]any)
	if param["type"] != "array" {
		t.Fatalf("expected the sanitized array param, got %#v", props)
	}
}
