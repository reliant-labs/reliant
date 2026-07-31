// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/pkg/components"
	"github.com/reliant-labs/reliant/internal/rctx"
)

var componentLib = components.NewLibrary()

// ── Tool params ──────────────────────────────────────────────────────────

type ComponentLibraryParams struct {
	Action          string `json:"action" jsonschema:"required,enum=search,enum=get,enum=list,enum=install,description=Action: 'search' finds components by query/tags/category. 'get' retrieves a specific component's source code. 'list' returns all components with metadata. 'install' writes a component to disk."`
	Name            string `json:"name,omitempty" jsonschema:"description=Component name to retrieve (required for 'get' and 'install' actions)"`
	Query           string `json:"query,omitempty" jsonschema:"description=Search query — matches against component names and descriptions (for 'search' action)"`
	Tag             string `json:"tag,omitempty" jsonschema:"description=Filter by tag (e.g. 'deck' or 'chart' or 'landing' or 'dashboard'). For 'search' or 'list' actions."`
	Category        string `json:"category,omitempty" jsonschema:"enum=layouts,enum=charts,enum=diagrams,enum=deck,enum=ui,description=Filter by category (for 'search' or 'list' actions)"`
	Path            string `json:"path,omitempty" jsonschema:"description=Full file path including filename where the component should be written (required for 'install' action). Example: src/components/layouts/sidebar_left.tsx"`
	ForgeIntegrated bool   `json:"forge_integrated,omitempty" jsonschema:"description=When true, returned components include forge system integration notes (useUiStore, useEventBus, useAuth). When false (default), components are standalone. Set to true when working in a forge-generated project with EventBusProvider and Zustand stores set up."`
	Repo            string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo the install path is relative to: 'root' for the project root\\, or a repo name (e.g. 'api'\\, 'web'). Used as the base for relative paths. Omit in single-repo projects or when path is absolute."`
}

// ── Tool implementation ──────────────────────────────────────────────────

type componentLibraryTool struct{}

func NewComponentLibraryTool() Tool {
	return NewToolWrapper[ComponentLibraryParams, ToolResponse](&componentLibraryTool{})
}

func (t *componentLibraryTool) Name() string {
	return "component_library"
}

func (t *componentLibraryTool) Description() string {
	return `Component library with 61 production-ready React/TypeScript components for building UIs, dashboards, landing pages, pitch decks, charts, and diagrams.

WHEN TO USE:
- Building any UI — search for relevant components first
- Creating charts or diagrams — components handle all coordinate math
- Building pitch deck slides — use deck components as templates
- Need a layout pattern — search by use case (dashboard, landing, portal, crm)
- Building CRUD/admin interfaces — badge, modal, tabs, pagination, toast, etc.

ACTIONS:
- search: Find components with unified keyword search
  Examples: search(query="crud table admin"), search(query="chart dashboard"), search(tag="deck")
- get: Retrieve full source code for a specific component
  Example: get(name="quadrant_chart")
- install: Write a component file to disk at the given path
  Example: install(name="sidebar_left", path="src/components/layouts/sidebar_left.tsx")
- list: Browse all components (optionally filtered by tag or category)

CATEGORIES: layouts (11), charts (6), diagrams (5), deck (7), ui (32)

TAGS: layout, chart, diagram, deck, ui, landing, marketing, dashboard, analytics, admin, portal, crm, comparison, pricing, hero, form, auth, slide, presentation, saas, funnel, competitive, market, pipeline, process, team, docs, technical, crud, table, stats, detail, search, navigation, modal, dialog, filter, badge, status, tabs, pagination, toast, notification, avatar, dropdown, menu, skeleton, loading, toggle, switch, alert, banner, activity, feed, metric, breadcrumb

OPTIONS:
- forge_integrated: Set to true in forge-generated projects to get integration guidance for useUiStore, useEventBus, and useAuth

CHARTS handle all coordinate math internally — pass data, get pixels. No spatial reasoning required.`
}

func (t *componentLibraryTool) RequiresPermission(params ComponentLibraryParams) (bool, error) {
	return params.Action == "install", nil
}

func (t *componentLibraryTool) Execute(rctx *rctx.ToolContext, params ComponentLibraryParams) (ToolResponse, error) {
	switch params.Action {
	case "search":
		return t.search(params.Query, params.Tag, params.Category)
	case "get":
		if params.Name == "" {
			return NewTextErrorResponse("'name' is required when action is 'get'"), nil
		}
		return t.get(params.Name, params.ForgeIntegrated)
	case "install":
		if params.Name == "" {
			return NewTextErrorResponse("'name' is required when action is 'install'"), nil
		}
		if params.Path == "" {
			return NewTextErrorResponse("'path' is required when action is 'install'"), nil
		}
		return t.install(rctx, params.Name, params.Path, params.Repo)
	case "list":
		return t.list(params.Tag, params.Category)
	default:
		return NewTextErrorResponse(fmt.Sprintf("Unknown action: %s. Use 'search', 'get', 'install', or 'list'", params.Action)), nil
	}
}

func (t *componentLibraryTool) search(query, tag, category string) (ToolResponse, error) {
	// Build unified search string from all provided params
	var parts []string
	if query != "" {
		parts = append(parts, query)
	}
	if tag != "" {
		parts = append(parts, tag)
	}
	if category != "" {
		parts = append(parts, category)
	}
	unified := strings.Join(parts, " ")
	results := componentLib.Search(unified)
	if len(results) == 0 {
		return NewTextResponse("No components found matching your criteria. Try a broader search or use action='list' to see all components."), nil
	}
	return NewTextResponse(components.FormatComponentList(results)), nil
}

func (t *componentLibraryTool) get(name string, forgeIntegrated bool) (ToolResponse, error) {
	entry, exists := componentLib.GetEntry(name)
	if !exists {
		suggestions := componentLib.FindSimilar(name)
		if len(suggestions) > 0 {
			return NewTextErrorResponse(fmt.Sprintf("Component '%s' not found. Did you mean: %s?", name, strings.Join(suggestions, ", "))), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Component '%s' not found. Use action='list' to see available components.", name)), nil
	}

	content, err := componentLib.Get(name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to read component: %v", err)), nil
	}

	header := fmt.Sprintf("# %s (%s)\n# %s\n# Tags: %s\n#\n# Copy this component into your project and customize the props.\n# All coordinate math is handled internally — just pass your data.\n#\n",
		entry.Name,
		entry.Category,
		entry.Description,
		strings.Join(entry.Tags, ", "),
	)

	if forgeIntegrated {
		header += "# FORGE INTEGRATION\n" +
			"# This project uses forge's built-in providers. When applicable:\n" +
			"# - Use useUiStore from @/stores/ui-store for shared UI state (sidebar, modals)\n" +
			"# - Use useEventBus/useEvent from @/lib/event-context for imperative actions\n" +
			"# - Use useAuth from @/lib/auth/context for authentication state\n" +
			"# - Components with controlled props (collapsed, onToggle) can be wired to stores\n" +
			"#   Example: <SidebarLayout collapsed={useUiStore(s => s.sidebarCollapsed)} onToggle={useUiStore(s => s.toggleSidebar)} />\n" +
			"#\n"
	}

	header += "\n"

	response := NewTextResponse(header + content)
	return WithResponseMetadata(response, map[string]interface{}{
		"name":        entry.Name,
		"category":    entry.Category,
		"description": entry.Description,
		"tags":        entry.Tags,
	}), nil
}

func (t *componentLibraryTool) install(rctx *rctx.ToolContext, name, path, repo string) (ToolResponse, error) {
	entry, exists := componentLib.GetEntry(name)
	if !exists {
		suggestions := componentLib.FindSimilar(name)
		if len(suggestions) > 0 {
			return NewTextErrorResponse(fmt.Sprintf("Component '%s' not found. Did you mean: %s?", name, strings.Join(suggestions, ", "))), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Component '%s' not found. Use action='list' to see available components.", name)), nil
	}

	content, err := componentLib.Get(name)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to read component: %v", err)), nil
	}

	if rctx.Daemon == nil {
		return NewTextErrorResponse("filesystem access requires a connected daemon"), nil
	}

	filePath := path
	if !filepath.IsAbs(filePath) {
		workingDir, err := ResolveRepoPath(rctx, repo)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf("couldn't determine working directory: %v", err)), nil
		}
		filePath = filepath.Join(workingDir, filePath)
	}

	_, err = rctx.Daemon.WriteFile(rctx.Context, filePath, content)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to write component to %s: %v", path, err)), nil
	}

	return NewTextResponse(fmt.Sprintf("Component '%s' (%s) installed to %s", entry.Name, entry.Category, path)), nil
}

func (t *componentLibraryTool) list(tag, category string) (ToolResponse, error) {
	results := componentLib.List(tag, category)
	if len(results) == 0 {
		return NewTextResponse("No components match the filter. Available categories: layouts, charts, diagrams, deck, ui"), nil
	}
	return NewTextResponse(components.FormatComponentList(results)), nil
}
