// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// LoadToolParams defines the parameters for the load_tool tool.
type LoadToolParams struct {
	Name  string `json:"name,omitempty" jsonschema:"description=Tool name to load (exact name from the available tools list)"`
	Query string `json:"query,omitempty" jsonschema:"description=Search for tools by keyword"`
}

// LoadToolMetadata is the metadata returned by load_tool to signal the runtime.
type LoadToolMetadata struct {
	LoadedTools []string `json:"loaded_tools,omitempty"`
}

type loadToolTool struct{}

const loadToolDescription = `Dynamically load a tool by name or search for available tools.

Use this when you need a tool that isn't currently loaded. You can:
- Load a specific tool by name: {"name": "sourcegraph"}
- Search for tools by keyword: {"query": "workflow"}

Loaded tools become available immediately on the next turn.`

func NewLoadToolTool() Tool {
	return NewToolWrapper[LoadToolParams, ToolResponse](&loadToolTool{})
}

func (t *loadToolTool) Name() string {
	return ToolLoadTool
}

func (t *loadToolTool) Description() string {
	return loadToolDescription
}

func (t *loadToolTool) RequiresPermission(params LoadToolParams) (bool, error) {
	return false, nil
}

func (t *loadToolTool) Execute(rctx *rctx.ToolContext, params LoadToolParams) (ToolResponse, error) {
	if params.Name == "" && params.Query == "" {
		return NewTextErrorResponse("Either 'name' or 'query' is required"), nil
	}

	// Resolve permission from the store (set by call_llm based on preset/workflow inputs)
	chatID := GetChatID(rctx)
	permission := GetLoadedToolsStore().GetPermission(chatID)

	// Search mode
	if params.Query != "" {
		return t.searchTools(params.Query, permission), nil
	}

	// Load mode
	return t.loadTool(rctx, params.Name, permission), nil
}

func (t *loadToolTool) loadTool(rctx *rctx.ToolContext, name string, permission string) ToolResponse {
	// Check if tool exists in registry
	registry := GetToolRegistry()
	var found *ToolDefinition
	for _, def := range registry {
		if def.Name == name {
			found = &def
			break
		}
	}

	if found == nil {
		// Check MCP tools
		if strings.HasPrefix(name, "mcp__") {
			return t.loadMCPTool(rctx, name)
		}
		return NewTextErrorResponse(fmt.Sprintf(
			"Tool '%s' not found in the registry. Use load_tool with query to search for available tools.", name))
	}

	// Check permission
	minPerm := MinimumPermissionForTool(name)
	if !PermissionAtLeast(permission, minPerm) {
		return NewTextErrorResponse(fmt.Sprintf(
			"Tool '%s' requires '%s' permission, but agent has '%s' permission.",
			name, minPerm, permission))
	}

	// Check if already loaded
	chatID := GetChatID(rctx)
	store := GetLoadedToolsStore()
	if store.Has(chatID, name) {
		return NewTextResponse(fmt.Sprintf("Tool '%s' is already loaded.", name))
	}

	// Add to loaded tools store
	store.Add(chatID, name)

	// Return confirmation with metadata for the runtime
	metadata := LoadToolMetadata{
		LoadedTools: []string{name},
	}

	response := NewTextResponse(fmt.Sprintf(
		"Tool '%s' has been loaded. It will be available on your next turn.", name))

	return WithResponseMetadata(response, metadata)
}

func (t *loadToolTool) loadMCPTool(rctx *rctx.ToolContext, name string) ToolResponse {
	chatID := GetChatID(rctx)
	store := GetLoadedToolsStore()

	if store.Has(chatID, name) {
		return NewTextResponse(fmt.Sprintf("Tool '%s' is already loaded.", name))
	}

	// MCP tools are always allowed (they're managed by the MCP configuration)
	store.Add(chatID, name)

	metadata := LoadToolMetadata{
		LoadedTools: []string{name},
	}

	response := NewTextResponse(fmt.Sprintf(
		"MCP tool '%s' has been loaded. It will be available on your next turn.", name))

	return WithResponseMetadata(response, metadata)
}

func (t *loadToolTool) searchTools(query string, permission string) ToolResponse {
	results := SearchTools(query, permission)

	if len(results) == 0 {
		return NewTextResponse(fmt.Sprintf("No tools found matching '%s'.", query))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d tools matching '%s':\n\n", len(results), query))

	for _, r := range results {
		tags := make([]string, len(r.Tags))
		for i, tag := range r.Tags {
			tags[i] = string(tag)
		}
		status := "available"
		if !r.PermissionAllowed {
			status = fmt.Sprintf("requires %s permission", r.MinPermission)
		}
		sb.WriteString(fmt.Sprintf("- **%s** [%s] (%s)\n", r.Name, strings.Join(tags, ", "), status))
	}

	sb.WriteString("\nUse load_tool with name to load a specific tool.")
	return NewTextResponse(sb.String())
}

// FormatDeferredToolsAnnouncement creates the system prompt section announcing
// tools that can be loaded via load_tool.
func FormatDeferredToolsAnnouncement(chatID string, permission string, currentToolNames []string) string {
	deferred := DeferredToolNames(chatID, permission, currentToolNames)
	if len(deferred) == 0 {
		return ""
	}

	// Serialize the list
	namesJSON, err := json.Marshal(deferred)
	if err != nil {
		return ""
	}

	return fmt.Sprintf(`
<system-reminder>
Additional tools available (use load_tool to enable):
%s

Use load_tool(name="tool_name") to load a specific tool, or load_tool(query="keyword") to search.
</system-reminder>`, string(namesJSON))
}
