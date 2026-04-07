// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
)

// =============================================================================
// GET SCHEMA TOOL - Unified schema lookup (replaces get_schemas)
// =============================================================================

type GetSchemaParams struct {
	Name string `json:"name" jsonschema:"required,description=Type name to look up (e.g. 'call_llm'\\, 'ThreadConfig'\\, 'CallLLMOutput'\\, 'Workflow'\\, 'Edge')"`
}

type getSchemaTool struct{}

const (
	GetSchemaToolName        = "get_schema"
	getSchemaToolDescription = `Look up schema documentation for any workflow type by name.

WHEN TO USE:
- When you see a field type like "thread: ThreadConfig" and need details
- When you need to understand node output structure (e.g., CallLLMOutput)
- To explore top-level types (Workflow, Edge)
- To get full field documentation for any type

WHAT YOU CAN QUERY:
- Node types: call_llm, loop, workflow, run, execute_tools, join, etc.
- Input types: string, number, boolean, enum, model, message, etc.
- Config types: ThreadConfig, SaveMessageConfig, ProjectConfig, ResponseTool, etc.
- Output types: CallLLMOutput, ExecuteToolsOutput, RunOutput, LoopOutput, etc.
- Top-level: Workflow, Edge, EdgeCase

Type detection is automatic - just provide the name.

EXAMPLES:
- get_schema(name="call_llm")       // Node type
- get_schema(name="ThreadConfig")   // Config type  
- get_schema(name="CallLLMOutput")  // Output structure
- get_schema(name="Workflow")       // Top-level workflow structure
- get_schema(name="Edge")           // Edge routing structure`
)

func NewGetSchemaTool() Tool {
	tool := &getSchemaTool{}
	return NewToolWrapper[GetSchemaParams, ToolResponse](tool)
}

func (t *getSchemaTool) Name() string {
	return GetSchemaToolName
}

func (t *getSchemaTool) Description() string {
	return getSchemaToolDescription
}

func (t *getSchemaTool) RequiresPermission(args GetSchemaParams) (bool, error) {
	return false, nil
}

func (t *getSchemaTool) Execute(rctx *rctx.ToolContext, args GetSchemaParams) (ToolResponse, error) {
	if args.Name == "" {
		return NewTextErrorResponse("name parameter is required"), nil
	}

	name := args.Name

	// Try node type first (snake_case names like call_llm)
	if doc, err := getNodeTypeDoc(name); err == nil {
		return NewTextResponse(doc), nil
	}

	// Try input type (lowercase names like string, enum)
	if doc, err := getInputTypeDoc(strings.ToLower(name)); err == nil {
		return NewTextResponse(doc), nil
	}

	// Try shared/struct type (PascalCase names like ThreadConfig, CallLLMOutput)
	if doc, err := getSharedTypeDoc(name); err == nil {
		return NewTextResponse(doc), nil
	}

	// Not found - provide helpful suggestions
	return NewTextErrorResponse(buildNotFoundMessage(name)), nil
}

// buildNotFoundMessage creates a helpful error message with suggestions
func buildNotFoundMessage(name string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Type '%s' not found.\n\n", name)

	// Collect all available types for suggestions
	nodeTypes := reference.ListNodeTypes()
	inputTypes := reference.ListInputTypes()
	sharedTypes := reference.ListSharedTypes()

	// Find similar names (simple prefix/contains matching)
	var suggestions []string
	nameLower := strings.ToLower(name)

	for _, t := range nodeTypes {
		if strings.Contains(strings.ToLower(t), nameLower) || strings.Contains(nameLower, strings.ToLower(t)) {
			suggestions = append(suggestions, t+" (node)")
		}
	}
	for _, t := range inputTypes {
		if strings.Contains(strings.ToLower(t), nameLower) || strings.Contains(nameLower, strings.ToLower(t)) {
			suggestions = append(suggestions, t+" (input)")
		}
	}
	for _, t := range sharedTypes {
		if strings.Contains(strings.ToLower(t), nameLower) || strings.Contains(nameLower, strings.ToLower(t)) {
			suggestions = append(suggestions, t+" (type)")
		}
	}

	if len(suggestions) > 0 {
		sb.WriteString("Did you mean: ")
		sb.WriteString(strings.Join(suggestions, ", "))
		sb.WriteString("\n\n")
	}

	// List available types by category
	sort.Strings(nodeTypes)
	sort.Strings(inputTypes)
	sort.Strings(sharedTypes)

	sb.WriteString("**Available types:**\n\n")
	sb.WriteString("Node types: ")
	sb.WriteString(strings.Join(nodeTypes, ", "))
	sb.WriteString("\n\n")

	sb.WriteString("Input types: ")
	sb.WriteString(strings.Join(inputTypes, ", "))
	sb.WriteString("\n\n")

	sb.WriteString("Config/Output types: ")
	sb.WriteString(strings.Join(sharedTypes, ", "))

	return sb.String()
}

// =============================================================================
// SHARED DOCUMENTATION FORMATTERS
// =============================================================================

// getNodeTypeDoc returns formatted documentation for a node type
func getNodeTypeDoc(typeName string) (string, error) {
	info, ok := reference.GetNodeType(typeName)
	if !ok {
		return "", fmt.Errorf("unknown node type: %s", typeName)
	}

	var sb strings.Builder

	// Header
	fmt.Fprintf(&sb, "# Node Type: %s\n\n", info.TypeName)

	// Description
	sb.WriteString("## Description\n\n")
	sb.WriteString(info.Description)
	sb.WriteString("\n\n")

	// Fields
	if len(info.Fields) > 0 {
		sb.WriteString("## Fields\n\n")
		sb.WriteString("| Field | Type | Required | Description |\n")
		sb.WriteString("|-------|------|----------|-------------|\n")
		for _, field := range info.Fields {
			required := "No"
			if field.Required {
				required = "Yes"
			}
			// Escape pipes and newlines for markdown table
			desc := strings.ReplaceAll(field.Description, "|", "\\|")
			desc = strings.ReplaceAll(desc, "\n", " ")
			fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", field.Name, field.Type, required, desc)
		}
		sb.WriteString("\n")

		// Detailed field descriptions
		sb.WriteString("### Field Details\n\n")
		for _, field := range info.Fields {
			fmt.Fprintf(&sb, "**%s** (%s)", field.Name, field.Type)
			if field.Required {
				sb.WriteString(" - *required*")
			}
			sb.WriteString("\n")
			if field.Description != "" {
				sb.WriteString(field.Description)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}

	// Output fields
	if len(info.OutputFields) > 0 {
		sb.WriteString("## Output Fields\n\n")
		sb.WriteString("Access via `nodes.<id>.*` in CEL expressions:\n\n")
		for _, field := range info.OutputFields {
			fmt.Fprintf(&sb, "- `%s` (%s)\n", field.Name, field.Type)
		}
		sb.WriteString("\n")
	}

	// Example
	if info.Example != "" {
		sb.WriteString("## Example\n\n")
		sb.WriteString("```yaml\n")
		sb.WriteString(info.Example)
		sb.WriteString("\n```\n")
	}

	return sb.String(), nil
}

// getInputTypeDoc returns formatted documentation for an input type
func getInputTypeDoc(typeName string) (string, error) {
	typeName = strings.ToLower(typeName)

	info, ok := reference.GetInputType(typeName)
	if !ok {
		return "", fmt.Errorf("unknown input type: %s", typeName)
	}

	var sb strings.Builder

	// Header
	fmt.Fprintf(&sb, "# Input Type: %s\n\n", info.TypeName)

	// Description
	if info.Description != "" {
		sb.WriteString("## Description\n\n")
		sb.WriteString(info.Description)
		sb.WriteString("\n\n")
	}

	// Fields
	if len(info.Fields) > 0 {
		sb.WriteString("## Fields\n\n")
		sb.WriteString("| Field | Type | Required | Description |\n")
		sb.WriteString("|-------|------|----------|-------------|\n")
		for _, field := range info.Fields {
			required := "No"
			if field.Required {
				required = "Yes"
			}
			// Escape pipes and newlines for markdown table
			desc := strings.ReplaceAll(field.Description, "|", "\\|")
			desc = strings.ReplaceAll(desc, "\n", " ")
			fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", field.Name, field.Type, required, desc)
		}
		sb.WriteString("\n")
	}

	// Example
	if info.Example != "" {
		sb.WriteString("## Example\n\n")
		sb.WriteString("```yaml\n")
		sb.WriteString(info.Example)
		sb.WriteString("\n```\n")
	}

	return sb.String(), nil
}

// getSharedTypeDoc returns formatted documentation for a shared/helper type
func getSharedTypeDoc(typeName string) (string, error) {
	info, ok := reference.GetSharedType(typeName)
	if !ok {
		return "", fmt.Errorf("unknown shared type: %s", typeName)
	}

	var sb strings.Builder

	// Header - categorize based on name suffix
	category := "Type"
	if strings.HasSuffix(typeName, "Output") {
		category = "Output Type"
	} else if strings.HasSuffix(typeName, "Config") {
		category = "Config Type"
	}
	fmt.Fprintf(&sb, "# %s: %s\n\n", category, info.Name)

	// Description
	if info.Description != "" {
		sb.WriteString("## Description\n\n")
		sb.WriteString(info.Description)
		sb.WriteString("\n\n")
	}

	// Fields
	if len(info.Fields) > 0 {
		sb.WriteString("## Fields\n\n")
		sb.WriteString("| Field | Type | Required | Description |\n")
		sb.WriteString("|-------|------|----------|-------------|\n")
		for _, field := range info.Fields {
			required := "No"
			if field.Required {
				required = "Yes"
			}
			// Escape pipes and newlines for markdown table
			desc := strings.ReplaceAll(field.Description, "|", "\\|")
			desc = strings.ReplaceAll(desc, "\n", " ")
			fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", field.Name, field.Type, required, desc)
		}
		sb.WriteString("\n")

		// Detailed field descriptions
		sb.WriteString("### Field Details\n\n")
		for _, field := range info.Fields {
			fmt.Fprintf(&sb, "**%s** (%s)", field.Name, field.Type)
			if field.Required {
				sb.WriteString(" - *required*")
			}
			sb.WriteString("\n")
			if field.Description != "" {
				sb.WriteString(field.Description)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}
	}

	// Example
	if info.Example != "" {
		sb.WriteString("## Example\n\n")
		sb.WriteString("```yaml\n")
		sb.WriteString(info.Example)
		sb.WriteString("\n```\n")
	}

	return sb.String(), nil
}
