package reliant

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestGeminiCompatibleToolParameters_FallsBackForBooleanItemsUnderAnyOf(t *testing.T) {
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
	if params["type"] != "object" {
		t.Fatalf("expected fallback root type=object, got %#v", params["type"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %#v", params["additionalProperties"])
	}
	props, _ := params["properties"].(map[string]any)
	if len(props) != 0 {
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
	if params["additionalProperties"] != false {
		t.Fatalf("expected fallback additionalProperties=false, got %#v", params["additionalProperties"])
	}
	props, _ := params["properties"].(map[string]any)
	if len(props) != 0 {
		t.Fatalf("expected fallback empty properties, got %#v", props)
	}
}
