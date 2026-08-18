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

type loadToolTool struct {
	deferredTools []string
}

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
	if len(t.deferredTools) == 0 {
		return loadToolDescription
	}
	namesJSON, err := json.Marshal(t.deferredTools)
	if err != nil {
		return loadToolDescription
	}
	return loadToolDescription + fmt.Sprintf(`

Additional tools available (use load_tool to enable):
%s

Use load_tool(name="tool_name") to load a specific tool, or load_tool(query="keyword") to search.`, string(namesJSON))
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
		return t.searchTools(chatID, params.Query, permission), nil
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

	// Verify the MCP tool is actually connected in this environment before
	// loading it. Adding an unavailable name would be silently dropped by the
	// runtime next turn ("Tools in filter not found"), so fail loudly instead.
	if !mcpToolAvailable(store.GetAvailableMCPTools(chatID), name) {
		return NewTextErrorResponse(fmt.Sprintf(
			"MCP tool '%s' is not available in this environment. Use load_tool with a query to discover connected MCP tools.", name))
	}

	// MCP tools are gated by MCP configuration, not the agent permission ladder.
	store.Add(chatID, name)

	metadata := LoadToolMetadata{
		LoadedTools: []string{name},
	}

	response := NewTextResponse(fmt.Sprintf(
		"MCP tool '%s' has been loaded. It will be available on your next turn.", name))

	return WithResponseMetadata(response, metadata)
}

// mcpToolAvailable reports whether name is present in the recorded set of
// connected/available MCP tools.
func mcpToolAvailable(available []MCPToolInfo, name string) bool {
	for _, m := range available {
		if m.Name == name {
			return true
		}
	}
	return false
}

func (t *loadToolTool) searchTools(chatID, query string, permission string) ToolResponse {
	mcpTools := GetLoadedToolsStore().GetAvailableMCPTools(chatID)
	results := SearchTools(query, permission, mcpTools)

	if len(results) == 0 {
		return NewTextResponse(fmt.Sprintf("No tools found matching '%s'.", query))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d tools matching '%s':\n\n", len(results), query)

	for _, r := range results {
		tags := make([]string, len(r.Tags))
		for i, tag := range r.Tags {
			tags[i] = string(tag)
		}
		status := "available"
		if !r.PermissionAllowed {
			status = fmt.Sprintf("requires %s permission", r.MinPermission)
		}
		fmt.Fprintf(&sb, "- **%s** [%s] (%s)\n", r.Name, strings.Join(tags, ", "), status)
	}

	sb.WriteString("\nUse load_tool with name to load a specific tool.")
	return NewTextResponse(sb.String())
}

// DeferredToolsAware is implemented by tools that can receive the list of
// deferred (not-yet-loaded) tools so they can advertise them in their description.
type DeferredToolsAware interface {
	SetDeferredTools(names []string)
}

// SetDeferredTools sets the list of available-but-not-loaded tools so the
// description can advertise them to the LLM.
func (t *loadToolTool) SetDeferredTools(names []string) {
	t.deferredTools = names
}
