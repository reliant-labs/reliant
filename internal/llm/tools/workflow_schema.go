// Copyright (c) 2025 Reliant Labs
package tools

import (
	"sort"
	"strings"

	"github.com/invopop/jsonschema"
	orderedmap "github.com/pb33f/ordered-map/v2"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
)

// =============================================================================
// WORKFLOW SCHEMA BUILDER
// =============================================================================
// Builds JSON schemas from proto descriptors at runtime using the reference package.
// No hardcoded type knowledge - everything comes from proto annotations.

var (
	// Cached schemas - built once on first access
	cachedNodeSchema     *jsonschema.Schema
	cachedInputSchema    *jsonschema.Schema
	cachedEdgeSchema     *jsonschema.Schema
	cachedWorkflowSchema *jsonschema.Schema
)

// GetNodeSchema returns a flat union JSON schema for all node types.
// Built from proto descriptor metadata in the reference package.
func GetNodeSchema() *jsonschema.Schema {
	if cachedNodeSchema != nil {
		return cachedNodeSchema
	}

	nodeTypeNames := reference.ListNodeTypes()
	sort.Strings(nodeTypeNames)

	// Build properties from reference package
	props := orderedmap.New[string, *jsonschema.Schema]()

	// Common fields
	props.Set("id", &jsonschema.Schema{
		Type:        "string",
		Description: "Unique node identifier",
	})
	props.Set("type", &jsonschema.Schema{
		Type:        "string",
		Description: "Node type",
		Enum:        toInterfaceSlice(nodeTypeNames),
	})

	// Collect fields from all node types
	seenFields := map[string]bool{"id": true, "type": true}

	for _, typeName := range nodeTypeNames {
		info, ok := reference.GetNodeType(typeName)
		if !ok {
			continue
		}

		for _, field := range info.Fields {
			if seenFields[field.Name] {
				continue
			}
			seenFields[field.Name] = true

			schema := referenceFieldToSchema(field.Name, field.Type, field.Description, typeName)
			if schema != nil {
				props.Set(field.Name, schema)
			}
		}
	}

	cachedNodeSchema = &jsonschema.Schema{
		Type:        "object",
		Description: "A workflow node. The 'type' field determines which fields apply.",
		Properties:  props,
		Required:    []string{"id", "type"},
	}

	return cachedNodeSchema
}

// GetInputSchema returns a flat union JSON schema for all input types.
// Built from proto descriptor metadata in the reference package.
func GetInputSchema() *jsonschema.Schema {
	if cachedInputSchema != nil {
		return cachedInputSchema
	}

	inputTypeNames := reference.ListInputTypes()
	sort.Strings(inputTypeNames)

	// Build properties
	props := orderedmap.New[string, *jsonschema.Schema]()

	props.Set("type", &jsonschema.Schema{
		Type:        "string",
		Description: "Input type",
		Enum:        toInterfaceSlice(inputTypeNames),
	})
	props.Set("description", &jsonschema.Schema{
		Type:        "string",
		Description: "Help text for this input",
	})
	props.Set("default", &jsonschema.Schema{
		Description: "Default value if not provided",
	})

	// Collect fields from all input types
	seenFields := map[string]bool{"type": true, "description": true, "default": true}

	for _, typeName := range inputTypeNames {
		info, ok := reference.GetInputType(typeName)
		if !ok {
			continue
		}

		for _, field := range info.Fields {
			if seenFields[field.Name] {
				continue
			}
			seenFields[field.Name] = true

			schema := referenceFieldToSchema(field.Name, field.Type, field.Description, typeName)
			if schema != nil {
				props.Set(field.Name, schema)
			}
		}
	}

	cachedInputSchema = &jsonschema.Schema{
		Type:        "object",
		Description: "Input definition. The 'type' field determines the input kind.",
		Properties:  props,
		Required:    []string{"type"},
	}

	return cachedInputSchema
}

// GetEdgeSchema returns the JSON schema for workflow edges.
func GetEdgeSchema() *jsonschema.Schema {
	if cachedEdgeSchema != nil {
		return cachedEdgeSchema
	}

	caseProps := orderedmap.New[string, *jsonschema.Schema]()
	caseProps.Set("condition", &jsonschema.Schema{
		Type:        "string",
		Description: "CEL condition expression (required, no {{}}).",
	})
	caseProps.Set("to", &jsonschema.Schema{
		Description: "Destination node ID or array for fan-out",
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
		},
	})
	caseProps.Set("label", &jsonschema.Schema{
		Type:        "string",
		Description: "Human-readable label",
	})

	edgeProps := orderedmap.New[string, *jsonschema.Schema]()
	edgeProps.Set("from", &jsonschema.Schema{
		Type:        "string",
		Description: "Source node ID",
	})
	edgeProps.Set("cases", &jsonschema.Schema{
		Type:        "array",
		Description: "Conditional routing cases evaluated in order. All cases must have a condition.",
		Items: &jsonschema.Schema{
			Type:       "object",
			Properties: caseProps,
			Required:   []string{"condition", "to"},
		},
	})
	edgeProps.Set("default", &jsonschema.Schema{
		Description: "Fallback destination when no case matches (string or array of strings)",
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
		},
	})

	cachedEdgeSchema = &jsonschema.Schema{
		Type:        "object",
		Description: "An edge connecting nodes with conditional routing. Must have at least cases or default.",
		Properties:  edgeProps,
		Required:    []string{"from"},
	}

	return cachedEdgeSchema
}

// GetWorkflowSchema returns the complete workflow JSON schema.
func GetWorkflowSchema() *jsonschema.Schema {
	if cachedWorkflowSchema != nil {
		return cachedWorkflowSchema
	}

	props := orderedmap.New[string, *jsonschema.Schema]()

	props.Set("name", &jsonschema.Schema{
		Type:        "string",
		Description: "Workflow name. Used in refs like 'builtin://name'.",
	})
	props.Set("apiVersion", &jsonschema.Schema{
		Type:        "string",
		Description: "API version for compatibility",
	})
	props.Set("description", &jsonschema.Schema{
		Type:        "string",
		Description: "Human-readable description",
	})
	props.Set("tag", &jsonschema.Schema{
		Type:        "string",
		Description: "Tag for preset matching (e.g., 'agent')",
	})
	props.Set("entry", &jsonschema.Schema{
		Description: "Entry point node ID or array of IDs for parallel start",
		OneOf: []*jsonschema.Schema{
			{Type: "string"},
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
		},
	})
	props.Set("nodes", &jsonschema.Schema{
		Type:        "array",
		Description: "All nodes in the workflow",
		Items:       GetNodeSchema(),
	})
	props.Set("edges", &jsonschema.Schema{
		Type:        "array",
		Description: "Edges connecting nodes",
		Items:       GetEdgeSchema(),
	})
	props.Set("inputs", &jsonschema.Schema{
		Type:                 "object",
		Description:          "Workflow inputs. Keys are input names.",
		AdditionalProperties: GetInputSchema(),
	})
	props.Set("outputs", &jsonschema.Schema{
		Type:        "object",
		Description: "Output mappings as CEL expressions",
	})

	cachedWorkflowSchema = &jsonschema.Schema{
		Type:        "object",
		Description: "A complete workflow definition",
		Properties:  props,
		Required:    []string{"name", "entry", "nodes"},
	}

	return cachedWorkflowSchema
}

// =============================================================================
// HELPERS
// =============================================================================

// referenceFieldToSchema converts a reference field type string to a JSON schema.
func referenceFieldToSchema(name, fieldType, description, typePrefix string) *jsonschema.Schema {
	schema := &jsonschema.Schema{}

	if description != "" {
		schema.Description = "[" + typePrefix + "] " + description
	} else {
		schema.Description = "[" + typePrefix + "] " + name
	}

	// Map common proto field types to JSON schema types
	typeLower := strings.ToLower(fieldType)
	switch {
	case typeLower == "string" || strings.Contains(typeLower, "cel"):
		schema.Type = "string"
	case typeLower == "int32" || typeLower == "int64" || typeLower == "integer":
		schema.Type = "integer"
	case typeLower == "float" || typeLower == "double" || typeLower == "number":
		schema.Type = "number"
	case typeLower == "bool" || typeLower == "boolean":
		schema.Type = "boolean"
	case strings.HasPrefix(typeLower, "repeated") || strings.HasPrefix(typeLower, "[]") || typeLower == "array":
		schema.Type = "array"
	case strings.HasPrefix(typeLower, "map") || typeLower == "object":
		schema.Type = "object"
	case typeLower == "threadconfig":
		threadProps := orderedmap.New[string, *jsonschema.Schema]()
		threadProps.Set("mode", &jsonschema.Schema{Type: "string", Description: "Thread mode: inherit, new, fork"})
		threadProps.Set("memo", &jsonschema.Schema{Type: "boolean", Description: "Reuse thread across iterations"})
		return &jsonschema.Schema{
			Type:        "object",
			Description: "[" + typePrefix + "] Thread configuration",
			Properties:  threadProps,
		}
	case typeLower == "savemessageconfig":
		smProps := orderedmap.New[string, *jsonschema.Schema]()
		smProps.Set("role", &jsonschema.Schema{Type: "string", Description: "Message role (CEL)"})
		smProps.Set("content", &jsonschema.Schema{Type: "string", Description: "Message content (CEL)"})
		smProps.Set("tool_calls", &jsonschema.Schema{Type: "string", Description: "Tool calls (CEL)"})
		smProps.Set("tool_results", &jsonschema.Schema{Type: "string", Description: "Tool results (CEL)"})
		return &jsonschema.Schema{
			Type:        "object",
			Description: "[" + typePrefix + "] Auto-save message config",
			Properties:  smProps,
		}
	default:
		schema.Type = "string" // Default to string for unknown types
	}

	return schema
}

func toInterfaceSlice(ss []string) []interface{} {
	result := make([]interface{}, len(ss))
	for i, s := range ss {
		result[i] = s
	}
	return result
}
