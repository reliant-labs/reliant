// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// LoadToolParams defines the parameters for the load_tool tool.
type LoadToolParams struct {
	Name  string `json:"name,omitempty" jsonschema:"description=Tool name to load (exact name from the available tools list)"`
	Query string `json:"query,omitempty" jsonschema:"description=Search for tools by keyword"`
}

// LoadToolMetadata is what load_tool returns to the runtime: the tools it
// granted. execute_tools unions it across the batch into
// ExecuteToolsOutput.granted_tools, the workflow records that per thread, and
// the thread's next call_llm offers them. There is no other record of a grant.
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

	caps := capabilitiesFor(rctx)

	if params.Query != "" {
		return t.searchTools(rctx, caps, params.Query), nil
	}
	if tag, ok := strings.CutPrefix(params.Name, "tag:"); ok {
		return t.loadTag(rctx, caps, tag), nil
	}
	return t.loadTool(rctx, caps, params.Name), nil
}

// capabilitiesFor is the turn's capability set, handed over by execute_tools
// on the context. A batch whose call_llm recorded none (a run that predates
// the set) gets the legacy policy, bounded by the chat row's machine fact,
// which execute_tools marks on the same context.
func capabilitiesFor(rctx *rctx.ToolContext) *Capabilities {
	if caps := CapabilitiesFrom(rctx.Context); caps != nil {
		return caps
	}
	return LegacyCapabilities(nomachine.Is(rctx.Context))
}

// loadTag loads every registry tool carrying tag. Each tool goes through
// loadTool, so loadable_tools and the permission ladder apply per tool.
func (t *loadToolTool) loadTag(rctx *rctx.ToolContext, caps *Capabilities, tag string) ToolResponse {
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

	for _, name := range names {
		if caps.Offers(name) {
			already = append(already, name)
			continue
		}
		resp := t.loadTool(rctx, caps, name)
		if resp.IsError {
			refused = append(refused, fmt.Sprintf("%s (%s)", name, resp.Content))
			continue
		}
		loaded = append(loaded, name)
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

// loadTool grants one tool. The decision is the turn's capability set — the
// same set the menu was built from and execution enforces — so a tool this
// grants is one the next call_llm will offer.
func (t *loadToolTool) loadTool(rctx *rctx.ToolContext, caps *Capabilities, name string) ToolResponse {
	if caps.Offers(name) {
		return NewTextResponse(fmt.Sprintf("Tool '%s' is already loaded.", name))
	}
	if refusal := caps.LoadRefusal(name); refusal != "" {
		return NewTextErrorResponse(refusal)
	}

	// A media tool the user has no provider for would load fine and then fail
	// at call time. Refuse here, with the fix, instead.
	if message, _ := mediaToolUnavailable(rctx.Context, name); message != "" {
		return NewTextErrorResponse(fmt.Sprintf("Tool '%s' was not loaded. %s.", name, message))
	}

	label := "Tool"
	if strings.HasPrefix(name, MCPToolPrefix) {
		label = "MCP tool"
	}
	response := NewTextResponse(fmt.Sprintf(
		"%s '%s' has been loaded. It will be available on your next turn.", label, name))
	return WithResponseMetadata(response, LoadToolMetadata{LoadedTools: []string{name}})
}

func (t *loadToolTool) searchTools(rctx *rctx.ToolContext, caps *Capabilities, query string) ToolResponse {
	results := SearchTools(query, caps)

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
		switch {
		case caps.Offers(r.Name):
			status = "already loaded"
		case !r.PermissionAllowed:
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

// SearchTools searches the built-in registry and the turn's connected MCP
// tools by keyword. Registry tools match on name or tag; MCP tools on name,
// which carries the server name (mcp__chrome-devtools__take_screenshot) —
// their descriptions are not part of the capability set, which carries names
// only.
//
// Discovery must agree with enforcement (Capabilities.Searchable): a tool the
// set could never grant — outside loadable_tools, needing a machine on a
// no-machine run, an integration the owner has not connected, request_machine
// anywhere — is left out, because advertising what loading will refuse teaches
// the model to retry something that cannot work. A tool above the tier stays
// listed with that status, so the agent can tell the user why it cannot have it.
func SearchTools(query string, caps *Capabilities) []ToolSearchResult {
	q := strings.ToLower(query)

	var results []ToolSearchResult
	for _, def := range GetToolRegistry() {
		matched := strings.Contains(strings.ToLower(def.Name), q)
		for _, tag := range def.Tags {
			if strings.Contains(strings.ToLower(string(tag)), q) {
				matched = true
			}
		}
		if !matched || !caps.Searchable(def.Name) {
			continue
		}
		minPerm := MinimumPermissionForTool(def.Name)
		results = append(results, ToolSearchResult{
			Name:              def.Name,
			Tags:              def.Tags,
			MinPermission:     minPerm,
			PermissionAllowed: PermissionAtLeast(caps.Permission, minPerm),
		})
	}

	// MCP tools are gated by MCP configuration rather than the permission
	// ladder, so they always report as allowed once connected.
	for _, name := range caps.MCPTools {
		if !strings.Contains(strings.ToLower(name), q) || !caps.Searchable(name) {
			continue
		}
		results = append(results, ToolSearchResult{
			Name:              name,
			Tags:              []ToolTag{TagMCP},
			MinPermission:     PermissionMutating,
			PermissionAllowed: true,
		})
	}

	return results
}

// ToolSearchResult represents a search result for tool discovery.
type ToolSearchResult struct {
	Name              string    `json:"name"`
	Tags              []ToolTag `json:"tags"`
	MinPermission     string    `json:"min_permission"`
	PermissionAllowed bool      `json:"allowed"`
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
