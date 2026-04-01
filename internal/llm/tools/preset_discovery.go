// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// LIST PRESETS TOOL
// =============================================================================

type ListPresetsParams struct{}

type listPresetsTool struct{}

const (
	ListPresetsToolName        = "list_presets"
	listPresetsToolDescription = `Lists available presets for agent nodes.

WHEN TO USE:
- To discover available presets for agent configurations
- To find the right preset for a specific task
- Before using get_preset to view details

RETURNS:
List of preset names with descriptions.`
)

func NewListPresetsTool() Tool {
	tool := &listPresetsTool{}
	return NewToolWrapper[ListPresetsParams, ToolResponse](tool)
}

func (t *listPresetsTool) Name() string {
	return ListPresetsToolName
}

func (t *listPresetsTool) Description() string {
	return listPresetsToolDescription
}

func (t *listPresetsTool) RequiresPermission(args ListPresetsParams) (bool, error) {
	return false, nil
}

func (t *listPresetsTool) Execute(rctx *rctx.ToolContext, args ListPresetsParams) (ToolResponse, error) {
	var sb strings.Builder
	sb.WriteString("# Available Presets\n\n")

	// Read builtin presets
	entries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to list presets: %v", err)), nil
	}

	type presetInfo struct {
		name        string
		description string
		tag         string
	}
	var presets []presetInfo

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		// Read and parse to get description
		data, err := builtin.BuiltinPresetsFS.ReadFile("presets/" + entry.Name())
		if err != nil {
			continue
		}

		var preset struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
			Tag         string `yaml:"tag"`
		}
		if err := yaml.Unmarshal(data, &preset); err != nil {
			continue
		}

		desc := preset.Description
		if desc == "" {
			desc = "(no description)"
		}

		presets = append(presets, presetInfo{
			name:        preset.Name,
			description: desc,
			tag:         preset.Tag,
		})
	}

	// Sort by name
	sort.Slice(presets, func(i, j int) bool {
		return presets[i].name < presets[j].name
	})

	sb.WriteString("| Preset | Tag | Description |\n")
	sb.WriteString("|--------|-----|-------------|\n")
	for _, p := range presets {
		// Truncate long descriptions
		desc := p.description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		tag := p.tag
		if tag == "" {
			tag = "-"
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", p.name, tag, desc))
	}

	sb.WriteString("\nUse `get_preset(name=\"...\")` to view the full preset configuration.\n")

	return NewTextResponse(sb.String()), nil
}

// =============================================================================
// GET PRESET TOOL
// =============================================================================

type GetPresetParams struct {
	Name string `json:"name" jsonschema:"required,description=Preset name to retrieve"`
}

type getPresetTool struct{}

const (
	GetPresetToolName        = "get_preset"
	getPresetToolDescription = `Gets the full configuration of a preset.

WHEN TO USE:
- To view preset configurations
- To understand what a preset provides
- To copy and adapt preset settings

PARAMETERS:
- name: The preset name (from list_presets)

RETURNS:
The complete preset YAML configuration.`
)

func NewGetPresetTool() Tool {
	tool := &getPresetTool{}
	return NewToolWrapper[GetPresetParams, ToolResponse](tool)
}

func (t *getPresetTool) Name() string {
	return GetPresetToolName
}

func (t *getPresetTool) Description() string {
	return getPresetToolDescription
}

func (t *getPresetTool) RequiresPermission(args GetPresetParams) (bool, error) {
	return false, nil
}

func (t *getPresetTool) Execute(rctx *rctx.ToolContext, args GetPresetParams) (ToolResponse, error) {
	if args.Name == "" {
		return NewTextErrorResponse("name parameter is required"), nil
	}

	// Try to find the preset file
	filename := "presets/" + args.Name + ".yaml"

	data, err := builtin.BuiltinPresetsFS.ReadFile(filename)
	if err != nil {
		// Try to find similar names
		entries, _ := builtin.BuiltinPresetsFS.ReadDir("presets")
		var available []string
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
				available = append(available, strings.TrimSuffix(entry.Name(), ".yaml"))
			}
		}
		sort.Strings(available)

		return NewTextErrorResponse(fmt.Sprintf(
			"Preset not found: %s\n\nAvailable presets: %s\n\nUse list_presets to see all options.",
			args.Name,
			strings.Join(available, ", "),
		)), nil
	}

	var preset struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(data, &preset); err != nil {
		return ToolResponse{}, fmt.Errorf("failed to unmarshal preset: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Preset: %s\n\n", preset.Name))
	if preset.Description != "" {
		sb.WriteString(fmt.Sprintf("**Description:** %s\n\n", preset.Description))
	}
	sb.WriteString("```yaml\n")
	sb.WriteString(string(data))
	sb.WriteString("\n```\n")

	return NewTextResponse(sb.String()), nil
}
