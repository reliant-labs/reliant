// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
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
- Load every tool carrying a tag in one call: {"name": "tag:workflow"}
  (each tool is still individually checked against what this agent may load)
- Search for tools by keyword or tag name: {"query": "workflow"}

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

	// Resolve permission from the store (set by call_llm based on preset/workflow
	// inputs). Keyed by scope, not chat: a spawned child shares the chat with its
	// parent and is distinguished only by thread, so a chat-keyed read would hand
	// the child whichever level was written last.
	scopeKey := Scope(GetChatID(rctx), rctx.Thread)
	permission := GetLoadedToolsStore().GetPermission(scopeKey)

	// Search mode
	if params.Query != "" {
		return t.searchTools(rctx, scopeKey, params.Query, permission), nil
	}

	// Load mode
	if tag, ok := strings.CutPrefix(params.Name, "tag:"); ok {
		return t.loadTag(rctx, tag, permission), nil
	}
	return t.loadTool(rctx, params.Name, permission), nil
}

// loadTag loads every registry tool carrying tag. Each tool goes through
// loadTool, so loadable_tools and the permission ladder apply per tool.
func (t *loadToolTool) loadTag(rctx *rctx.ToolContext, tag string, permission string) ToolResponse {
	if _, known := TagDescriptions[ToolTag(tag)]; !known {
		known := make([]string, 0, len(TagDescriptions))
		for k := range TagDescriptions {
			known = append(known, string(k))
		}
		sort.Strings(known)
		return NewTextErrorResponse(fmt.Sprintf("Unknown tag '%s'. Known tags: %s.", tag, strings.Join(known, ", ")))
	}

	var loaded, already, refused, names []string
	for _, def := range GetToolRegistry() {
		for _, dt := range def.Tags {
			if string(dt) == tag {
				names = append(names, def.Name)
				break
			}
		}
	}
	sort.Strings(names)

	if len(names) == 0 {
		msg := fmt.Sprintf("Tag '%s' is known but no built-in tools carry it, so nothing was loaded.", tag)
		if ToolTag(tag) == TagMCP {
			msg += " MCP tools load individually by their `mcp__...` name, e.g. load_tool(name=\"mcp__server__tool\")."
		}
		return NewTextResponse(msg)
	}

	store := GetLoadedToolsStore()
	scopeKey := Scope(GetChatID(rctx), rctx.Thread)
	for _, name := range names {
		wasLoaded := store.Has(scopeKey, name)
		resp := t.loadTool(rctx, name, permission)
		switch {
		case resp.IsError:
			refused = append(refused, fmt.Sprintf("%s (%s)", name, resp.Content))
		case wasLoaded:
			already = append(already, name)
		default:
			loaded = append(loaded, name)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Tag '%s': %d loaded, %d already loaded, %d refused.\n", tag, len(loaded), len(already), len(refused))
	if len(loaded) > 0 {
		fmt.Fprintf(&sb, "\nLoaded (available on your next turn): %s\n", strings.Join(loaded, ", "))
	}
	if len(already) > 0 {
		fmt.Fprintf(&sb, "\nAlready loaded: %s\n", strings.Join(already, ", "))
	}
	for i, r := range refused {
		if i == 0 {
			sb.WriteString("\nRefused:\n")
		}
		fmt.Fprintf(&sb, "- %s\n", r)
	}
	response := NewTextResponse(sb.String())
	if len(loaded) > 0 {
		return WithResponseMetadata(response, LoadToolMetadata{LoadedTools: loaded})
	}
	return response
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

	// A tool offered only to a run with no machine is handed to that run
	// directly, never loaded, so it cannot outlive the run's no-machine state.
	if OnlyWithoutMachine(name) {
		return NewTextErrorResponse(fmt.Sprintf(
			"Tool '%s' is not loadable: a chat with no machine already has it, and a chat on a machine never does.", name))
	}

	scopeKey := Scope(GetChatID(rctx), rctx.Thread)
	store := GetLoadedToolsStore()

	// What the workflow said load_tool may reach, checked independently of the
	// permission ladder — a tool must pass both.
	//
	// This is loadable_tools, NOT the preloaded bundle. An earlier version
	// checked the bundle, which read an omission as a refusal and made every
	// tool outside it unreachable; unset here means unrestricted, which is how
	// the product already behaves.
	if !store.CanLoadTool(scopeKey, name) {
		return NewTextErrorResponse(fmt.Sprintf(
			"Tool '%s' is not loadable in this workflow (see loadable_tools).", name))
	}

	// Check permission
	minPerm := MinimumPermissionForTool(name)
	if !PermissionAtLeast(permission, minPerm) {
		return NewTextErrorResponse(fmt.Sprintf(
			"Tool '%s' requires '%s' permission, but agent has '%s' permission.",
			name, minPerm, permission))
	}

	// Check if already loaded
	if store.Has(scopeKey, name) {
		return NewTextResponse(fmt.Sprintf("Tool '%s' is already loaded.", name))
	}

	// A media tool the user has no provider for would load fine and then fail
	// at call time. Refuse here, with the fix, instead.
	if message, _ := mediaToolUnavailable(rctx.Context, name); message != "" {
		return NewTextErrorResponse(fmt.Sprintf("Tool '%s' was not loaded. %s.", name, message))
	}

	// Add to loaded tools store
	store.Add(scopeKey, name)

	// Return confirmation with metadata for the runtime
	metadata := LoadToolMetadata{
		LoadedTools: []string{name},
	}

	response := NewTextResponse(fmt.Sprintf(
		"Tool '%s' has been loaded. It will be available on your next turn.", name))

	return WithResponseMetadata(response, metadata)
}

func (t *loadToolTool) loadMCPTool(rctx *rctx.ToolContext, name string) ToolResponse {
	scopeKey := Scope(GetChatID(rctx), rctx.Thread)
	store := GetLoadedToolsStore()

	if store.Has(scopeKey, name) {
		return NewTextResponse(fmt.Sprintf("Tool '%s' is already loaded.", name))
	}

	// loadable_tools covers MCP too. The ladder exemption below is deliberate
	// and stays, but availability alone was previously the only check, so a
	// workflow that wanted to bound what its agent could reach had no way to
	// include MCP in that. `tag:mcp` expands to the connected names.
	if !store.CanLoadTool(scopeKey, name) {
		return NewTextErrorResponse(fmt.Sprintf(
			"MCP tool '%s' is not loadable in this workflow (see loadable_tools).", name))
	}

	// Verify the MCP tool is actually connected in this environment before
	// loading it. Adding an unavailable name would be silently dropped by the
	// runtime next turn ("Tools in filter not found"), so fail loudly instead.
	if !mcpToolAvailable(store.GetAvailableMCPTools(scopeKey), name) {
		return NewTextErrorResponse(fmt.Sprintf(
			"MCP tool '%s' is not available in this environment. Use load_tool with a query to discover connected MCP tools.", name))
	}

	// MCP tools are gated by MCP configuration, not the agent permission ladder.
	store.Add(scopeKey, name)

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

func (t *loadToolTool) searchTools(rctx *rctx.ToolContext, scopeKey, query string, permission string) ToolResponse {
	store := GetLoadedToolsStore()
	mcpTools := store.GetAvailableMCPTools(scopeKey)
	results := SearchTools(query, permission, mcpTools)

	// Discovery must agree with enforcement. Advertising a tool that loadTool
	// will then refuse teaches the model to keep retrying something that cannot
	// work, and burns a turn each time.
	if !store.LoadableIsUnrestricted(scopeKey) {
		filtered := results[:0]
		for _, r := range results {
			if store.CanLoadTool(scopeKey, r.Name) {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

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
		// Still listed when unusable, so the agent can tell the user why.
		if message, modality := mediaToolUnavailable(rctx.Context, r.Name); message != "" {
			status = fmt.Sprintf("unavailable: no %s-capable provider configured. %s", modality, message)
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
