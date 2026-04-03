package runtime

import (
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// routerWorkflowInfo holds loaded metadata about a candidate workflow for routing.
type routerWorkflowInfo struct {
	Ref         string              // The workflow ref (e.g., "builtin://agent")
	Workflow    *reliantv1.Workflow // The parsed workflow proto
	RawYAML     string              // The raw YAML source
	Presets     []*preset.Preset    // Valid presets (filtered by candidate config)
	Description string              // Override description, or workflow description
}

// buildRoutingSystemPrompt constructs the system prompt for the routing LLM.
// It includes rich metadata about each candidate workflow.
func buildRoutingSystemPrompt(candidates []routerWorkflowInfo, customSystemPrompt string) string {
	var sb strings.Builder

	if customSystemPrompt != "" {
		sb.WriteString(customSystemPrompt)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString(defaultRoutingSystemPrompt)
		sb.WriteString("\n\n")
	}

	sb.WriteString("# Available Workflows\n\n")

	for i, c := range candidates {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		sb.WriteString(fmt.Sprintf("## Workflow: `%s`\n\n", c.Ref))

		// Description
		desc := c.Description
		if desc == "" && c.Workflow != nil {
			desc = c.Workflow.GetDescription()
		}
		if desc != "" {
			sb.WriteString(fmt.Sprintf("**Description:** %s\n\n", desc))
		}

		// Inputs
		if c.Workflow != nil && len(c.Workflow.GetInputs()) > 0 {
			sb.WriteString("### Inputs\n\n")
			writeInputSchema(&sb, c.Workflow.GetInputs())
			sb.WriteString("\n")
		}

		// Presets
		if len(c.Presets) > 0 {
			sb.WriteString("### Available Presets\n\n")
			for _, p := range c.Presets {
				sb.WriteString(fmt.Sprintf("- **`%s`**", p.Name))
				if p.Description != "" {
					sb.WriteString(fmt.Sprintf(": %s", p.Description))
				}
				sb.WriteString("\n")
			}
			sb.WriteString("\n")
		}

		// Raw YAML
		if c.RawYAML != "" {
			sb.WriteString("### Full Workflow Definition (YAML)\n\n")
			sb.WriteString("```yaml\n")
			sb.WriteString(c.RawYAML)
			if !strings.HasSuffix(c.RawYAML, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("```\n")
		}
	}

	return sb.String()
}

// writeInputSchema writes a human-readable input schema description.
func writeInputSchema(sb *strings.Builder, inputs map[string]*reliantv1.Input) {
	// Sort input names for deterministic output
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		input := inputs[name]
		if input == nil {
			continue
		}

		inputType := model.GetInputType(input)
		desc := getInputDescription(input)
		required := !model.IsInputRequired(input) // IsInputRequired returns true if has default

		sb.WriteString(fmt.Sprintf("- **`%s`** (%s)", name, inputType))
		if required {
			sb.WriteString(" *required*")
		}
		if desc != "" {
			sb.WriteString(fmt.Sprintf(" — %s", desc))
		}

		// Type-specific details
		details := getInputTypeDetails(input)
		if details != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", details))
		}

		sb.WriteString("\n")

		// Recurse into group inputs
		if model.IsGroupInput(input) {
			if groupInputs := model.GetGroupInputs(input); groupInputs != nil {
				// Indent group children
				groupNames := make([]string, 0, len(groupInputs))
				for gn := range groupInputs {
					groupNames = append(groupNames, gn)
				}
				sort.Strings(groupNames)
				for _, gn := range groupNames {
					gi := groupInputs[gn]
					giType := model.GetInputType(gi)
					giDesc := getInputDescription(gi)
					sb.WriteString(fmt.Sprintf("  - **`%s.%s`** (%s)", name, gn, giType))
					if giDesc != "" {
						sb.WriteString(fmt.Sprintf(" — %s", giDesc))
					}
					sb.WriteString("\n")
				}
			}
		}
	}
}

// buildRoutingResponseSchema returns the JSON schema for the routing decision.
func buildRoutingResponseSchema(candidates []routerWorkflowInfo) map[string]interface{} {
	// Build enum of workflow refs
	workflowRefs := make([]interface{}, 0, len(candidates))
	for _, c := range candidates {
		workflowRefs = append(workflowRefs, c.Ref)
	}

	// Build enum of all available preset names (deduplicated)
	presetSet := make(map[string]bool)
	for _, c := range candidates {
		for _, p := range c.Presets {
			presetSet[p.Name] = true
		}
	}
	presetNames := make([]interface{}, 0, len(presetSet))
	for name := range presetSet {
		presetNames = append(presetNames, name)
	}
	// Sort for determinism
	sort.Slice(presetNames, func(i, j int) bool {
		return presetNames[i].(string) < presetNames[j].(string)
	})

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"workflow": map[string]interface{}{
				"type":        "string",
				"enum":        workflowRefs,
				"description": "The workflow reference to route to",
			},
			"preset": map[string]interface{}{
				"type":        "string",
				"enum":        presetNames,
				"description": "The preset to use for the selected workflow",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The refined/rewritten prompt for the selected workflow. Provide the full user request, adapted for the selected workflow's purpose.",
			},
			"reasoning": map[string]interface{}{
				"type":        "string",
				"description": "Brief explanation of why this workflow and preset were selected",
			},
		},
		"required": []interface{}{"workflow", "preset", "prompt", "reasoning"},
	}

	return schema
}

// getInputDescription extracts the human-readable description from an Input proto.
func getInputDescription(input *reliantv1.Input) string {
	if input == nil {
		return ""
	}
	switch cfg := input.GetConfig().(type) {
	case *reliantv1.Input_StringInput:
		return cfg.StringInput.GetBase().GetDescription()
	case *reliantv1.Input_NumberInput:
		return cfg.NumberInput.GetBase().GetDescription()
	case *reliantv1.Input_IntegerInput:
		return cfg.IntegerInput.GetBase().GetDescription()
	case *reliantv1.Input_BooleanInput:
		return cfg.BooleanInput.GetBase().GetDescription()
	case *reliantv1.Input_EnumInput:
		return cfg.EnumInput.GetBase().GetDescription()
	case *reliantv1.Input_ModelInput:
		return cfg.ModelInput.GetBase().GetDescription()
	case *reliantv1.Input_MessageInput:
		return cfg.MessageInput.GetBase().GetDescription()
	case *reliantv1.Input_AttachmentsInput:
		return cfg.AttachmentsInput.GetBase().GetDescription()
	case *reliantv1.Input_ToolsInput:
		return cfg.ToolsInput.GetBase().GetDescription()
	case *reliantv1.Input_ArrayInput:
		return cfg.ArrayInput.GetBase().GetDescription()
	case *reliantv1.Input_ObjectInput:
		return cfg.ObjectInput.GetBase().GetDescription()
	case *reliantv1.Input_AnyInput:
		return cfg.AnyInput.GetBase().GetDescription()
	case *reliantv1.Input_GroupInput:
		return cfg.GroupInput.GetBase().GetDescription()
	case *reliantv1.Input_PresetInput:
		return cfg.PresetInput.GetBase().GetDescription()
	}
	return ""
}

// getInputTypeDetails returns type-specific constraints as a human-readable string.
func getInputTypeDetails(input *reliantv1.Input) string {
	if input == nil {
		return ""
	}
	switch cfg := input.GetConfig().(type) {
	case *reliantv1.Input_EnumInput:
		if vals := cfg.EnumInput.GetEnumValues(); len(vals) > 0 {
			return "values: " + strings.Join(vals, ", ")
		}
	case *reliantv1.Input_NumberInput:
		parts := []string{}
		if cfg.NumberInput.GetMin() != 0 {
			parts = append(parts, fmt.Sprintf("min: %g", cfg.NumberInput.GetMin()))
		}
		if cfg.NumberInput.GetMax() != 0 {
			parts = append(parts, fmt.Sprintf("max: %g", cfg.NumberInput.GetMax()))
		}
		if len(parts) > 0 {
			return strings.Join(parts, ", ")
		}
	case *reliantv1.Input_IntegerInput:
		parts := []string{}
		if cfg.IntegerInput.GetMin() != 0 {
			parts = append(parts, fmt.Sprintf("min: %d", cfg.IntegerInput.GetMin()))
		}
		if cfg.IntegerInput.GetMax() != 0 {
			parts = append(parts, fmt.Sprintf("max: %d", cfg.IntegerInput.GetMax()))
		}
		if len(parts) > 0 {
			return strings.Join(parts, ", ")
		}
	}
	return ""
}

// filterPresetsByAllowed filters presets to only those in the allowed list.
// If allowedNames is empty, all presets are returned.
func filterPresetsByAllowed(presets []*preset.Preset, allowedNames []string) []*preset.Preset {
	if len(allowedNames) == 0 {
		return presets
	}

	allowed := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = true
	}

	filtered := make([]*preset.Preset, 0, len(presets))
	for _, p := range presets {
		if allowed[p.Name] {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

const defaultRoutingSystemPrompt = `You are a workflow router. Your job is to analyze the user's request and select the most appropriate workflow and preset to handle it.

Consider:
1. The nature of the task (research, implementation, review, debugging, etc.)
2. The available workflows and what each is designed for
3. The available presets and their specializations
4. Match the user's intent to the best workflow + preset combination

Select the workflow and preset that best matches the user's needs. Rewrite the prompt to be clear and specific for the selected workflow.`
