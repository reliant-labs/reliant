package catalog

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolDefinition is a normalized tool view for Reliant mcp package adaptation.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

// NormalizeTools converts SDK tool models into map-backed schema definitions.
func NormalizeTools(tools []*mcp.Tool) []ToolDefinition {
	out := make([]ToolDefinition, 0, len(tools))
	for _, sdkTool := range tools {
		if sdkTool == nil {
			continue
		}
		schema, ok := normalizeSchema(sdkTool.InputSchema)
		if !ok {
			continue
		}
		out = append(out, ToolDefinition{
			Name:        sdkTool.Name,
			Description: sdkTool.Description,
			InputSchema: schema,
		})
	}
	return out
}

func normalizeSchema(input any) (map[string]interface{}, bool) {
	schemaBytes, err := json.Marshal(input)
	if err != nil {
		return nil, false
	}
	var schemaMap map[string]interface{}
	if err := json.Unmarshal(schemaBytes, &schemaMap); err != nil {
		return nil, false
	}
	return schemaMap, true
}
