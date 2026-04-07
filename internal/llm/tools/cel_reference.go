// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
)

// titleFirst capitalizes the first letter of a string.
func titleFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// =============================================================================
// GET CEL REFERENCE TOOL
// =============================================================================

type GetCELReferenceParams struct {
	// No parameters needed - returns full reference
}

type getCELReferenceTool struct{}

const (
	GetCELReferenceToolName        = "get_cel_reference"
	getCELReferenceToolDescription = `Gets the CEL expression reference for workflow development.

WHEN TO USE:
- When writing conditions, args, or dynamic values in workflows
- To understand available namespaces and their fields
- To see available custom functions

RETURNS:
Complete CEL reference including:
- All namespaces (inputs, workflow, nodes, iter, output, outputs)
- Field documentation for each namespace
- Custom functions (parseJson, coalesce, etc.)
- Common patterns and examples`
)

func NewGetCELReferenceTool() Tool {
	tool := &getCELReferenceTool{}
	return NewToolWrapper(tool)
}

func (t *getCELReferenceTool) Name() string {
	return GetCELReferenceToolName
}

func (t *getCELReferenceTool) Description() string {
	return getCELReferenceToolDescription
}

func (t *getCELReferenceTool) RequiresPermission(args GetCELReferenceParams) (bool, error) {
	return false, nil
}

func (t *getCELReferenceTool) Execute(rctx *rctx.ToolContext, args GetCELReferenceParams) (ToolResponse, error) {
	var sb strings.Builder

	sb.WriteString("# CEL Expression Reference\n\n")
	sb.WriteString("CEL (Common Expression Language) is used throughout workflows for conditions, dynamic values, and expressions.\n\n")

	sb.WriteString("## Syntax\n\n")
	sb.WriteString("- Template syntax: `{{expression}}` - for interpolation in strings\n")
	sb.WriteString("- Direct syntax: `expression` - for condition fields, while clauses\n\n")

	// Namespaces section - from generated data
	sb.WriteString("## Namespaces\n\n")

	for _, ns := range reference.CELNamespaces {
		fmt.Fprintf(&sb, "### %s.*\n\n", ns.Name)

		// Clean up description (remove prefix like "CELInputs provides")
		desc := ns.Description
		if idx := strings.Index(desc, "Usage:"); idx > 0 {
			desc = strings.TrimSpace(desc[:idx])
			// Remove "CEL<Name> provides" prefix
			desc = strings.TrimPrefix(desc, "CEL"+titleFirst(ns.Name)+" provides ")
			desc = strings.TrimPrefix(desc, "CEL"+titleFirst(ns.Name)+"s provides ")
		}
		sb.WriteString(desc + "\n\n")

		if len(ns.Fields) > 0 {
			sb.WriteString("| Field | Type | Description |\n")
			sb.WriteString("|-------|------|-------------|\n")
			for _, f := range ns.Fields {
				fmt.Fprintf(&sb, "| `%s.%s` | %s | %s |\n", ns.Name, f.Name, f.Type, f.Description)
			}
			sb.WriteString("\n")
		} else if ns.IsDynamic {
			// Add helpful examples for dynamic namespaces
			switch ns.Name {
			case "inputs":
				sb.WriteString("Examples:\n")
				sb.WriteString("- `inputs.model` - Access string/enum input\n")
				sb.WriteString("- `inputs.max_turns` - Access number input\n")
				sb.WriteString("- `inputs.MyGroup.field` - Access grouped input\n\n")
			case "nodes":
				sb.WriteString("Access output from completed nodes. Fields depend on node type:\n\n")
				sb.WriteString("| Node Type | Key Output Fields |\n")
				sb.WriteString("|-----------|-------------------|\n")
				sb.WriteString("| `call_llm` | message, tool_calls, stop_reason, thinking, input_tokens, output_tokens |\n")
				sb.WriteString("| `execute_tools` | results (array of tool results) |\n")
				sb.WriteString("| `run` | stdout, stderr, exit_code, success |\n")
				sb.WriteString("| `loop` | outputs, iterations, stopped_reason |\n")
				sb.WriteString("| `workflow` | outputs (sub-workflow outputs) |\n")
				sb.WriteString("| `approval` | approved, message |\n")
				sb.WriteString("| `compact` | success, tokens_before, tokens_after |\n\n")
				sb.WriteString("Example: `nodes.call_llm.stop_reason == 'tool_use'`\n\n")
			case "output":
				sb.WriteString("Example: `output.message.content`\n\n")
			case "outputs":
				sb.WriteString("Example: `outputs.stop_reason != 'end_turn'`\n\n")
			}
		}
	}

	// Functions section - from generated data
	sb.WriteString("## Custom Functions\n\n")
	sb.WriteString("| Function | Description | Example |\n")
	sb.WriteString("|----------|-------------|--------|\n")
	for _, fn := range reference.CELFunctions {
		fmt.Fprintf(&sb, "| `%s` | %s | `%s` |\n", fn.Signature, fn.Description, fn.Example)
	}
	sb.WriteString("\n")

	// Standard operators (static content - these are CEL standard, won't change)
	sb.WriteString("## Standard CEL Operators\n\n")
	sb.WriteString("| Category | Operators |\n")
	sb.WriteString("|----------|----------|\n")
	sb.WriteString("| Comparison | `==`, `!=`, `<`, `>`, `<=`, `>=` |\n")
	sb.WriteString("| Logical | `&&`, `||`, `!` |\n")
	sb.WriteString("| Arithmetic | `+`, `-`, `*`, `/`, `%` |\n")
	sb.WriteString("| Ternary | `condition ? true_val : false_val` |\n")
	sb.WriteString("| Membership | `x in [1,2,3]`, `key in map` |\n")
	sb.WriteString("| String | `.contains()`, `.startsWith()`, `.endsWith()`, `.size()` |\n")
	sb.WriteString("| List | `.size()`, `.map()`, `.filter()`, `.exists()`, `.all()` |\n\n")

	// Common patterns (static content - useful examples)
	sb.WriteString("## Common Patterns\n\n")
	sb.WriteString("```yaml\n")
	sb.WriteString("# Edge condition - check LLM wants to use tools\n")
	sb.WriteString("condition: \"nodes.llm.stop_reason == 'tool_use'\"\n\n")
	sb.WriteString("# Loop until done or max iterations\n")
	sb.WriteString("while: \"outputs.stop_reason != 'end_turn' && iter.iteration < 50\"\n\n")
	sb.WriteString("# Dynamic model selection\n")
	sb.WriteString("model: \"{{inputs.model}}\"\n\n")
	sb.WriteString("# Conditional with fallback\n")
	sb.WriteString("temperature: \"{{inputs.temperature > 0 ? inputs.temperature : 0.7}}\"\n\n")
	sb.WriteString("# String interpolation\n")
	sb.WriteString("content: \"Processing item {{iter.index + 1}}: {{iter.value.name}}\"\n\n")
	sb.WriteString("# Check if array is empty\n")
	sb.WriteString("condition: \"size(nodes.execute_tools.results) > 0\"\n\n")
	sb.WriteString("# Access nested field safely\n")
	sb.WriteString("value: \"{{getOrDefault(nodes.run, 'exit_code', -1)}}\"\n")
	sb.WriteString("```\n")

	return NewTextResponse(sb.String()), nil
}
