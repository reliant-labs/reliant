package reliant

import (
	"encoding/json"
	"fmt"
	"strings"

	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
)

type schemaMapProvider interface {
	ParamSchemaMap() map[string]any
}

func rawToolSchemaMap(tool llmtools.Tool) map[string]any {
	if provider, ok := tool.(schemaMapProvider); ok {
		if raw := provider.ParamSchemaMap(); raw != nil {
			return deepCopySchemaMap(raw)
		}
	}

	schema := tool.ParamSchema()
	if schema == nil {
		return map[string]any{}
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{}
	}

	var params map[string]any
	if err := json.Unmarshal(data, &params); err != nil {
		return map[string]any{}
	}
	return params
}

func deepCopySchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		copied := make(map[string]any, len(in))
		for k, v := range in {
			copied[k] = v
		}
		return copied
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		copied := make(map[string]any, len(in))
		for k, v := range in {
			copied[k] = v
		}
		return copied
	}
	return out
}

func (c *ReliantClient) isGeminiModel() bool {
	apiModel := strings.ToLower(c.Options.Model.APIModel)
	modelID := strings.ToLower(string(c.Options.Model.ID))
	return strings.Contains(apiModel, "gemini") || strings.Contains(modelID, "gemini")
}

func geminiFallbackToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             []any{},
	}
}

func geminiCompatibleToolParameters(tool llmtools.Tool) map[string]any {
	params := rawToolSchemaMap(tool)
	if params == nil {
		params = map[string]any{}
	}

	if schemaType, ok := params["type"].(string); ok && schemaType != "" && schemaType != "object" {
		logging.Warn("[Reliant Gemini] Invalid root tool schema; applying safe fallback",
			"tool", tool.Name(),
			"type", schemaType,
		)
		return geminiFallbackToolSchema()
	}

	params["type"] = "object"
	if params["properties"] == nil {
		params["properties"] = map[string]any{}
	}
	if params["required"] == nil {
		params["required"] = []any{}
	}

	if err := validateGeminiToolSchemaMap(params, "$", true); err != nil {
		logging.Warn("[Reliant Gemini] Unsupported tool schema; applying safe fallback",
			"tool", tool.Name(),
			"error", err,
		)
		return geminiFallbackToolSchema()
	}

	return params
}

func validateGeminiToolSchemaMap(schema map[string]any, path string, isRoot bool) error {
	if schema == nil {
		return fmt.Errorf("%s: schema is nil", path)
	}

	if isRoot {
		if schemaType, _ := schema["type"].(string); schemaType != "object" {
			return fmt.Errorf("%s: root schema type must be object, got %#v", path, schema["type"])
		}
	}

	if propsRaw, exists := schema["properties"]; exists && propsRaw != nil {
		props, ok := propsRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties: invalid type %T", path, propsRaw)
		}
		for name, childRaw := range props {
			child, ok := childRaw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s: invalid type %T", path, name, childRaw)
			}
			if err := validateGeminiToolSchemaMap(child, path+".properties."+name, false); err != nil {
				return err
			}
		}
	}

	if itemsRaw, exists := schema["items"]; exists && itemsRaw != nil {
		items, ok := itemsRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items: unsupported type %T", path, itemsRaw)
		}
		if err := validateGeminiToolSchemaMap(items, path+".items", false); err != nil {
			return err
		}
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if raw, exists := schema[key]; exists && raw != nil {
			arr, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("%s.%s: invalid type %T", path, key, raw)
			}
			for i, itemRaw := range arr {
				child, ok := itemRaw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s[%d]: invalid type %T", path, key, i, itemRaw)
				}
				if err := validateGeminiToolSchemaMap(child, fmt.Sprintf("%s.%s[%d]", path, key, i), false); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
