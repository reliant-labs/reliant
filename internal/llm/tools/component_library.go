// Copyright (c) 2025 Reliant Labs
package tools

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
)

//go:embed components/*/*.tsx
var componentsFS embed.FS

// ComponentCategory groups components by type.
type ComponentCategory string

const (
	CategoryLayouts  ComponentCategory = "layouts"
	CategoryCharts   ComponentCategory = "charts"
	CategoryDiagrams ComponentCategory = "diagrams"
	CategoryDeck     ComponentCategory = "deck"
	CategoryUI       ComponentCategory = "ui"
)

// ComponentEntry describes a single component in the library.
type ComponentEntry struct {
	Name        string            `json:"name"`
	Category    ComponentCategory `json:"category"`
	Description string            `json:"description"`
	Tags        []string          `json:"tags"`
	FilePath    string            `json:"-"` // internal embed path
}

// componentRegistry holds metadata for every component.
// The actual file content is read from the embedded FS at runtime.
var componentRegistry = []ComponentEntry{
	// ── Layouts ──────────────────────────────────────────────────────────
	{Name: "hero_centered", Category: CategoryLayouts, Description: "Hero section with centered content, headline, CTA buttons, and gradient background", Tags: []string{"layout", "landing", "marketing", "hero"}},
	{Name: "sidebar_left", Category: CategoryLayouts, Description: "Fixed left sidebar with navigation and main content area", Tags: []string{"layout", "dashboard", "admin", "portal", "crm"}},
	{Name: "sidebar_right", Category: CategoryLayouts, Description: "Fixed right sidebar with main content and contextual panel", Tags: []string{"layout", "blog", "docs", "portal"}},
	{Name: "dashboard_grid", Category: CategoryLayouts, Description: "Responsive grid layout with metric cards and main content area", Tags: []string{"layout", "dashboard", "analytics", "admin", "crm"}},
	{Name: "card_grid", Category: CategoryLayouts, Description: "Responsive card grid with configurable columns and tags", Tags: []string{"layout", "gallery", "catalog", "marketing", "landing"}},
	{Name: "split_view", Category: CategoryLayouts, Description: "Two-pane layout with configurable ratio for comparison views", Tags: []string{"layout", "editor", "diff", "portal"}},
	{Name: "kanban_board", Category: CategoryLayouts, Description: "Multi-column board with cards for task management", Tags: []string{"layout", "kanban", "project", "crm", "portal"}},
	{Name: "form_wizard", Category: CategoryLayouts, Description: "Multi-step form with progress indicator", Tags: []string{"layout", "form", "onboarding", "wizard", "portal"}},
	{Name: "timeline", Category: CategoryLayouts, Description: "Vertical timeline with date markers and content blocks", Tags: []string{"layout", "timeline", "history", "marketing", "landing"}},
	{Name: "masonry", Category: CategoryLayouts, Description: "CSS columns masonry grid with variable-height items", Tags: []string{"layout", "gallery", "portfolio", "marketing"}},

	// ── Charts ───────────────────────────────────────────────────────────
	{Name: "quadrant_chart", Category: CategoryCharts, Description: "2x2 quadrant/matrix chart with positioned items, axis labels, and highlighted item. Items use 0-1 normalized coordinates — all pixel math is internal.", Tags: []string{"chart", "competitive", "matrix", "deck", "marketing", "comparison"}},
	{Name: "concentric_circles", Category: CategoryCharts, Description: "Nested concentric circles for TAM/SAM/SOM or layered metrics. Rings auto-space and labels position in visible bands.", Tags: []string{"chart", "market", "tam", "deck", "marketing"}},
	{Name: "funnel_chart", Category: CategoryCharts, Description: "Vertical funnel visualization with tapering stages, conversion annotations, and alert highlighting for problem stages.", Tags: []string{"chart", "funnel", "sales", "conversion", "deck", "marketing", "crm"}},
	{Name: "bar_chart", Category: CategoryCharts, Description: "Horizontal or vertical bar chart with stacked segments, auto-color, and value labels.", Tags: []string{"chart", "bar", "data", "dashboard", "analytics", "deck"}},
	{Name: "donut_chart", Category: CategoryCharts, Description: "Ring/donut chart with segments, center label, and legend. Uses SVG stroke-dasharray.", Tags: []string{"chart", "donut", "pie", "data", "dashboard", "analytics"}},
	{Name: "radar_chart", Category: CategoryCharts, Description: "Spider/radar chart with multiple overlaid datasets, configurable axes, and grid rings.", Tags: []string{"chart", "radar", "spider", "comparison", "dashboard", "analytics"}},

	// ── Diagrams ─────────────────────────────────────────────────────────
	{Name: "flow_horizontal", Category: CategoryDiagrams, Description: "Horizontal flow/pipeline with connected steps, status indicators, and optional loop-back arrow.", Tags: []string{"diagram", "flow", "pipeline", "process", "deck", "marketing"}},
	{Name: "comparison_matrix", Category: CategoryDiagrams, Description: "Feature comparison table with products as columns, grouped features, check/cross indicators, and highlighted column.", Tags: []string{"diagram", "comparison", "features", "pricing", "marketing", "landing"}},
	{Name: "process_steps", Category: CategoryDiagrams, Description: "Numbered process steps with completed/active/pending states. Supports horizontal and vertical layouts.", Tags: []string{"diagram", "process", "steps", "onboarding", "marketing", "landing"}},
	{Name: "architecture_diagram", Category: CategoryDiagrams, Description: "System architecture diagram with grouped service boxes and SVG arrow connections.", Tags: []string{"diagram", "architecture", "system", "technical", "docs"}},
	{Name: "org_chart", Category: CategoryDiagrams, Description: "Organizational hierarchy chart with recursive tree layout, avatar circles, and CSS connector lines.", Tags: []string{"diagram", "org", "hierarchy", "team", "portal"}},

	// ── Deck (Pitch Deck Slides) ─────────────────────────────────────────
	{Name: "slide_title", Category: CategoryDeck, Description: "Title/opening slide (1280x720) with centered company name, tagline, and optional logo.", Tags: []string{"deck", "slide", "title", "presentation"}},
	{Name: "slide_stat_hero", Category: CategoryDeck, Description: "Big statistic hero slide (1280x720) with giant gradient number, headline, and supporting text.", Tags: []string{"deck", "slide", "stat", "hero", "presentation"}},
	{Name: "slide_two_column", Category: CategoryDeck, Description: "Two-column content slide (1280x720) with title bar and equal left/right content areas.", Tags: []string{"deck", "slide", "two-column", "presentation"}},
	{Name: "slide_card_grid", Category: CategoryDeck, Description: "Card grid slide (1280x720) with 2-4 cards, icon areas, badges, and highlight borders.", Tags: []string{"deck", "slide", "cards", "grid", "presentation"}},
	{Name: "slide_comparison", Category: CategoryDeck, Description: "Before/After comparison slide (1280x720) with red 'before' and green 'after' panels.", Tags: []string{"deck", "slide", "comparison", "before-after", "presentation"}},
	{Name: "slide_quote", Category: CategoryDeck, Description: "Quote/testimonial slide (1280x720) with decorative quote marks and attribution.", Tags: []string{"deck", "slide", "quote", "testimonial", "presentation"}},
	{Name: "slide_metrics_grid", Category: CategoryDeck, Description: "Metrics/KPI grid slide (1280x720) with 2x3 metric cards, trend indicators, and highlight.", Tags: []string{"deck", "slide", "metrics", "kpi", "presentation"}},

	// ── UI Components ────────────────────────────────────────────────────
	{Name: "pricing_table", Category: CategoryUI, Description: "3-tier pricing comparison with highlighted tier, feature checklists, badges, and CTA buttons.", Tags: []string{"ui", "pricing", "saas", "marketing", "landing"}},
	{Name: "stat_grid", Category: CategoryUI, Description: "Statistics grid with large numbers, labels, icons, and trend indicators (up/down/flat).", Tags: []string{"ui", "stats", "metrics", "dashboard", "analytics"}},
	{Name: "feature_comparison", Category: CategoryUI, Description: "Product feature comparison table with sticky header, grouped features, and highlighted column.", Tags: []string{"ui", "comparison", "features", "pricing", "marketing", "landing"}},
	{Name: "testimonial_cards", Category: CategoryUI, Description: "Customer testimonial cards with quotes, star ratings, avatars, and attribution.", Tags: []string{"ui", "testimonials", "social-proof", "marketing", "landing"}},
	{Name: "navigation_header", Category: CategoryUI, Description: "Responsive navigation header with brand, links, CTA, and mobile hamburger menu.", Tags: []string{"ui", "navigation", "header", "landing", "portal", "dashboard"}},
	{Name: "footer", Category: CategoryUI, Description: "Multi-column site footer with link groups, social icons, and copyright.", Tags: []string{"ui", "footer", "landing", "portal", "marketing"}},
	{Name: "hero_section", Category: CategoryUI, Description: "Marketing hero section with headline, CTAs, and optional media area.", Tags: []string{"ui", "hero", "marketing", "landing"}},
	{Name: "login_form", Category: CategoryUI, Description: "Authentication form with email/password, social login, and sign-up link.", Tags: []string{"ui", "auth", "login", "form", "portal"}},
}

// componentsByName provides O(1) lookup.
var componentsByName map[string]*ComponentEntry

func init() {
	componentsByName = make(map[string]*ComponentEntry, len(componentRegistry))
	for i := range componentRegistry {
		c := &componentRegistry[i]
		c.FilePath = fmt.Sprintf("components/%s/%s.tsx", c.Category, c.Name)
		componentsByName[c.Name] = c
	}
}

// ── Tool params ──────────────────────────────────────────────────────────

type ComponentLibraryParams struct {
	Action   string `json:"action" jsonschema:"required,enum=search,enum=get,enum=list,description=Action: 'search' finds components by query/tags/category. 'get' retrieves a specific component's source code. 'list' returns all components with metadata."`
	Name     string `json:"name,omitempty" jsonschema:"description=Component name to retrieve (required for 'get' action)"`
	Query    string `json:"query,omitempty" jsonschema:"description=Search query — matches against component names and descriptions (for 'search' action)"`
	Tag      string `json:"tag,omitempty" jsonschema:"description=Filter by tag (e.g. 'deck' or 'chart' or 'landing' or 'dashboard'). For 'search' or 'list' actions."`
	Category string `json:"category,omitempty" jsonschema:"enum=layouts,enum=charts,enum=diagrams,enum=deck,enum=ui,description=Filter by category (for 'search' or 'list' actions)"`
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
	return `Component library with 36 production-ready React/TypeScript components for building UIs, dashboards, landing pages, pitch decks, charts, and diagrams.

WHEN TO USE:
- Building any UI — search for relevant components first
- Creating charts or diagrams — components handle all coordinate math
- Building pitch deck slides — use deck components as templates
- Need a layout pattern — search by use case (dashboard, landing, portal, crm)

ACTIONS:
- search: Find components by keyword, tag, or category
  Examples: search(query="chart"), search(tag="deck"), search(category="charts")
- get: Retrieve full source code for a specific component
  Example: get(name="quadrant_chart")
- list: Browse all components (optionally filtered by tag or category)

CATEGORIES: layouts (10), charts (6), diagrams (5), deck (7), ui (8)

TAGS: layout, chart, diagram, deck, ui, landing, marketing, dashboard, analytics, admin, portal, crm, comparison, pricing, hero, form, auth, slide, presentation, saas, funnel, competitive, market, pipeline, process, team, docs, technical

CHARTS handle all coordinate math internally — pass data, get pixels. No spatial reasoning required.`
}

func (t *componentLibraryTool) RequiresPermission(_ ComponentLibraryParams) (bool, error) {
	return false, nil
}

func (t *componentLibraryTool) Execute(_ *rctx.ToolContext, params ComponentLibraryParams) (ToolResponse, error) {
	switch params.Action {
	case "search":
		return t.search(params.Query, params.Tag, params.Category)
	case "get":
		if params.Name == "" {
			return NewTextErrorResponse("'name' is required when action is 'get'"), nil
		}
		return t.get(params.Name)
	case "list":
		return t.list(params.Tag, params.Category)
	default:
		return NewTextErrorResponse(fmt.Sprintf("Unknown action: %s. Use 'search', 'get', or 'list'", params.Action)), nil
	}
}

func (t *componentLibraryTool) search(query, tag, category string) (ToolResponse, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	tag = strings.ToLower(strings.TrimSpace(tag))
	category = strings.ToLower(strings.TrimSpace(category))

	if query == "" && tag == "" && category == "" {
		return t.list("", "")
	}

	var results []ComponentEntry
	for _, c := range componentRegistry {
		if category != "" && string(c.Category) != category {
			continue
		}
		if tag != "" && !hasTag(c.Tags, tag) {
			continue
		}
		if query != "" && !matchesQuery(c, query) {
			continue
		}
		results = append(results, c)
	}

	if len(results) == 0 {
		return NewTextResponse("No components found matching your criteria. Try a broader search or use action='list' to see all components."), nil
	}

	return NewTextResponse(formatComponentList(results)), nil
}

func (t *componentLibraryTool) get(name string) (ToolResponse, error) {
	entry, exists := componentsByName[name]
	if !exists {
		// Try fuzzy match
		suggestions := findSimilar(name)
		if len(suggestions) > 0 {
			return NewTextErrorResponse(fmt.Sprintf("Component '%s' not found. Did you mean: %s?", name, strings.Join(suggestions, ", "))), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("Component '%s' not found. Use action='list' to see available components.", name)), nil
	}

	content, err := componentsFS.ReadFile(entry.FilePath)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to read component: %v", err)), nil
	}

	header := fmt.Sprintf("# %s (%s)\n# %s\n# Tags: %s\n#\n# Copy this component into your project and customize the props.\n# All coordinate math is handled internally — just pass your data.\n\n",
		entry.Name,
		entry.Category,
		entry.Description,
		strings.Join(entry.Tags, ", "),
	)

	response := NewTextResponse(header + string(content))
	return WithResponseMetadata(response, map[string]interface{}{
		"name":        entry.Name,
		"category":    entry.Category,
		"description": entry.Description,
		"tags":        entry.Tags,
	}), nil
}

func (t *componentLibraryTool) list(tag, category string) (ToolResponse, error) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	category = strings.ToLower(strings.TrimSpace(category))

	var filtered []ComponentEntry
	for _, c := range componentRegistry {
		if category != "" && string(c.Category) != category {
			continue
		}
		if tag != "" && !hasTag(c.Tags, tag) {
			continue
		}
		filtered = append(filtered, c)
	}

	if len(filtered) == 0 {
		return NewTextResponse("No components match the filter. Available categories: layouts, charts, diagrams, deck, ui"), nil
	}

	return NewTextResponse(formatComponentList(filtered)), nil
}

// ── Helpers ──────────────────────────────────────────────────────────────

func hasTag(tags []string, target string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, target) {
			return true
		}
	}
	return false
}

func matchesQuery(c ComponentEntry, query string) bool {
	if strings.Contains(strings.ToLower(c.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(c.Description), query) {
		return true
	}
	for _, tag := range c.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

func findSimilar(name string) []string {
	name = strings.ToLower(name)
	var matches []string
	for _, c := range componentRegistry {
		cName := strings.ToLower(c.Name)
		if strings.Contains(cName, name) || strings.Contains(name, cName) {
			matches = append(matches, c.Name)
		}
		// Check for common prefix
		prefix := commonPrefix(name, cName)
		if len(prefix) >= 3 {
			matches = append(matches, c.Name)
		}
	}
	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			unique = append(unique, m)
		}
	}
	if len(unique) > 5 {
		unique = unique[:5]
	}
	return unique
}

func commonPrefix(a, b string) string {
	maxLen := len(a)
	if len(b) < maxLen {
		maxLen = len(b)
	}
	i := 0
	for i < maxLen && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func formatComponentList(entries []ComponentEntry) string {
	// Group by category
	grouped := make(map[ComponentCategory][]ComponentEntry)
	for _, e := range entries {
		grouped[e.Category] = append(grouped[e.Category], e)
	}

	// Ordered categories
	order := []ComponentCategory{CategoryLayouts, CategoryCharts, CategoryDiagrams, CategoryDeck, CategoryUI}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d components:\n", len(entries)))

	for _, cat := range order {
		items, ok := grouped[cat]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("\n## %s (%d)\n", strings.ToUpper(string(cat)), len(items)))
		for _, item := range items {
			ext := filepath.Ext(item.FilePath)
			if ext == "" {
				ext = ".tsx"
			}
			sb.WriteString(fmt.Sprintf("  • %s — %s\n", item.Name, item.Description))
			sb.WriteString(fmt.Sprintf("    Tags: %s\n", strings.Join(item.Tags, ", ")))
		}
	}

	sb.WriteString("\nUse action='get' with name='<component_name>' to retrieve the full source code.")
	return sb.String()
}
