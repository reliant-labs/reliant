// Copyright (c) 2025 Reliant Labs
package components

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed components/*/*.tsx
var componentsFS embed.FS

// Category groups components by type.
type Category string

// Category enum values. Each value tags a component template with the
// kind of artifact it produces (HTML layout, SVG chart, diagram, deck
// slide, or generic UI primitive).
const (
	CategoryLayouts  Category = "layouts"
	CategoryCharts   Category = "charts"
	CategoryDiagrams Category = "diagrams"
	CategoryDeck     Category = "deck"
	CategoryUI       Category = "ui"
)

// Entry describes a single component in the library.
type Entry struct {
	Name        string   `json:"name"`
	Category    Category `json:"category"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	FilePath    string   `json:"-"` // internal embed path
}

// registry holds metadata for every component.
var registry = []Entry{
	// ── Layouts ──────────────────────────────────────────────────────────
	{Name: "sidebar_left", Category: CategoryLayouts, Description: "Fixed left sidebar with navigation and main content area", Tags: []string{"layout", "dashboard", "admin", "portal", "crm"}},
	{Name: "sidebar_right", Category: CategoryLayouts, Description: "Fixed right sidebar with main content and contextual panel", Tags: []string{"layout", "blog", "docs", "portal"}},
	{Name: "dashboard_grid", Category: CategoryLayouts, Description: "Responsive grid layout with metric cards and main content area", Tags: []string{"layout", "dashboard", "analytics", "admin", "crm"}},
	{Name: "card_grid", Category: CategoryLayouts, Description: "Responsive card grid with configurable columns and tags", Tags: []string{"layout", "gallery", "catalog", "marketing", "landing"}},
	{Name: "split_view", Category: CategoryLayouts, Description: "Two-pane layout with configurable ratio for comparison views", Tags: []string{"layout", "editor", "diff", "portal"}},
	{Name: "kanban_board", Category: CategoryLayouts, Description: "Multi-column board with cards for task management", Tags: []string{"layout", "kanban", "project", "crm", "portal"}},
	{Name: "form_wizard", Category: CategoryLayouts, Description: "Multi-step form with progress indicator", Tags: []string{"layout", "form", "onboarding", "wizard", "portal"}},
	{Name: "timeline", Category: CategoryLayouts, Description: "Vertical timeline with date markers and content blocks", Tags: []string{"layout", "timeline", "history", "marketing", "landing"}},
	{Name: "masonry", Category: CategoryLayouts, Description: "CSS columns masonry grid with variable-height items", Tags: []string{"layout", "gallery", "portfolio", "marketing"}},
	{Name: "sidebar_layout", Category: CategoryLayouts, Description: "App shell with a Cloudflare-style left nav: uniform rows (icon, label, optional count / dot / dashed \"New\" badge), a chevron only on entries with sub-pages, parents that navigate to their overview AND expand their children inline (accordion derived from the active route), plain muted section labels, bottom-pinned footer entries above the user row, and a 56px icon rail when collapsed. Optional top bar; padded or fill content.", Tags: []string{"layout", "admin", "dashboard", "navigation", "sidebar", "console", "shell"}},
	{Name: "variation_grid", Category: CategoryLayouts, Description: "Side-by-side comparison grid for 2-4 design variations. Each variation gets a labeled artboard with optional note. Use when exploring alternative layouts or directions for a single design problem.", Tags: []string{"layout", "design", "comparison", "variations", "artboard", "explore"}},
	{Name: "dashboard_analytics", Category: CategoryLayouts, Description: "Analytics page shell — title + filters header, KPI strip, hero chart, optional drilldown table. Slot-based so the parent provides the chart and table content (typically Recharts + data_table).", Tags: []string{"layout", "dashboard", "analytics", "admin", "portal"}},
	{Name: "dashboard_ops", Category: CategoryLayouts, Description: "Ops/SRE dashboard layout — three independently-scrolling columns: alerts left, status grid center, log stream right. Stacks on narrow viewports.", Tags: []string{"layout", "dashboard", "ops", "sre", "incidents", "admin"}},
	{Name: "inbox_layout", Category: CategoryLayouts, Description: "Two-pane list + preview shell (Gmail/Linear pattern). Parent owns selection state; component just positions list and preview slots. Collapses to single column on narrow viewports.", Tags: []string{"layout", "inbox", "list", "preview", "mail", "portal"}},
	{Name: "editor_layout", Category: CategoryLayouts, Description: "Three-pane editor shell (Figma/VS Code pattern) — top toolbar, left tools panel, center canvas, right properties panel, optional bottom panel. All side regions are slots.", Tags: []string{"layout", "editor", "ide", "design", "canvas"}},

	// ── Charts ───────────────────────────────────────────────────────────
	{Name: "quadrant_chart", Category: CategoryCharts, Description: "2x2 quadrant/matrix chart with positioned items, axis labels, and highlighted item. Items use 0-1 normalized coordinates — all pixel math is internal.", Tags: []string{"chart", "competitive", "matrix", "deck", "marketing", "comparison"}},
	{Name: "concentric_circles", Category: CategoryCharts, Description: "Nested concentric circles for TAM/SAM/SOM or layered metrics. Rings auto-space and labels position in visible bands.", Tags: []string{"chart", "market", "tam", "deck", "marketing"}},
	{Name: "funnel_chart", Category: CategoryCharts, Description: "Vertical funnel visualization with tapering stages, conversion annotations, and alert highlighting for problem stages.", Tags: []string{"chart", "funnel", "sales", "conversion", "deck", "marketing", "crm"}},
	// NOTE: For commodity data viz (bar, line, area, donut, pie, scatter), install
	// Recharts (`npm i recharts`) — do NOT hand-roll. The component library only
	// ships *narrative* charts (quadrant, concentric_circles, funnel) where heavy
	// customization matters more than interactivity. The frontend skill carries
	// the full guidance.

	// ── Diagrams ─────────────────────────────────────────────────────────
	{Name: "flow_horizontal", Category: CategoryDiagrams, Description: "Horizontal flow/pipeline with connected steps, status indicators, and optional loop-back arrow.", Tags: []string{"diagram", "flow", "pipeline", "process", "deck", "marketing"}},
	{Name: "process_steps", Category: CategoryDiagrams, Description: "Numbered process steps with completed/active/pending states. Supports horizontal and vertical layouts.", Tags: []string{"diagram", "process", "steps", "onboarding", "marketing", "landing"}},
	{Name: "architecture_diagram", Category: CategoryDiagrams, Description: "System architecture diagram with grouped service boxes and SVG arrow connections.", Tags: []string{"diagram", "architecture", "system", "technical", "docs"}},
	{Name: "org_chart", Category: CategoryDiagrams, Description: "Organizational hierarchy chart with recursive tree layout, avatar circles, and CSS connector lines.", Tags: []string{"diagram", "org", "hierarchy", "team", "portal"}},
	{Name: "bus_bar", Category: CategoryDiagrams, Description: "Pub/sub bus diagram — producers on one side, consumers on the other, labeled bus in the middle. Communicates decoupling. Uses shared coordinate space (W=880, configurable rows) for clean SVG+DOM alignment.", Tags: []string{"diagram", "pub-sub", "kafka", "nats", "events", "architecture"}},
	{Name: "pub_sub_matrix", Category: CategoryDiagrams, Description: "Subscription matrix — topics as rows, consumers as columns, cells marking who subscribes to what. Pairs with bus_bar to make routing rules scannable. Pure table, no coordinate math.", Tags: []string{"diagram", "pub-sub", "matrix", "routing", "events", "architecture"}},

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
	{Name: "data_table", Category: CategoryUI, Description: "Dense data table: 36px sentence-case headers, 40px rows, hairline dividers, whole-row links (getRowHref) with hover, numeric columns right-aligned in tabular numerals, sorting, row selection, pagination only when wired, loading skeleton, empty state, and a flush mode for sitting inside a Card.", Tags: []string{"ui", "table", "crud", "admin", "dashboard", "list", "console"}},
	{Name: "detail_view", Category: CategoryUI, Description: "Structured detail/show view for a single entity with field groups, multiple field types, and action buttons.", Tags: []string{"ui", "crud", "admin", "detail"}},
	{Name: "crud_form", Category: CategoryUI, Description: "Form component for create/edit operations with typed fields, validation errors, and submit/cancel buttons.", Tags: []string{"ui", "crud", "admin", "form"}},
	{Name: "command_bar", Category: CategoryUI, Description: "Command palette / search bar (⌘K style) with filterable results grouped by category and keyboard navigation.", Tags: []string{"ui", "admin", "search", "navigation"}},
	{Name: "confirmation_dialog", Category: CategoryUI, Description: "Confirmation dialog for destructive actions with danger/warning variants, backdrop overlay, and loading state.", Tags: []string{"ui", "crud", "admin", "modal", "dialog"}},
	{Name: "empty_state", Category: CategoryUI, Description: "Empty state placeholder with icon, title, description, and call-to-action button.", Tags: []string{"ui", "crud", "admin"}},
	{Name: "breadcrumb", Category: CategoryUI, Description: "Breadcrumb navigation component with separators and current page indicator.", Tags: []string{"ui", "navigation", "admin"}},
	{Name: "filter_bar", Category: CategoryUI, Description: "Filter/search bar for list pages with search input, filter dropdowns, active filter chips, and clear all. Controlled when given `values` (pass useTypedSearchParams state so the URL stays the single truth), uncontrolled otherwise.", Tags: []string{"ui", "crud", "admin", "search", "filter", "controlled", "url"}},
	{Name: "page_header", Category: CategoryUI, Description: "Page title block: breadcrumb, title with an optional status beside it (titleAccessory), ONE meta line of facts (meta), and action buttons. title/subtitle/meta take composed ReactNode, so a header can carry a Status, a Tag or a Mono id without reimplementing the typography.", Tags: []string{"ui", "crud", "admin", "header", "navigation", "breadcrumb", "title", "meta"}},
	{Name: "toast_notification", Category: CategoryUI, Description: "Toast notification system with success, error, warning, and info variants. Auto-dismiss with configurable duration.", Tags: []string{"ui", "notification", "toast", "feedback", "admin"}},
	{Name: "dropdown_menu", Category: CategoryUI, Description: "Context menu / action dropdown with grouped items, icons, keyboard dismiss, and danger variant.", Tags: []string{"ui", "menu", "dropdown", "actions", "admin", "crud"}},
	{Name: "avatar", Category: CategoryUI, Description: "User avatar with image, initials fallback, and online/offline/busy status indicator.", Tags: []string{"ui", "avatar", "user", "profile", "admin"}},
	{Name: "badge", Category: CategoryUI, Description: "Filled status pill with success, warning, error, info, and neutral variants, dot indicator and removable mode. Reserve it for the one thing per screen that must shout: a state is a Status (dot + word), a kind or env label is a Tag.", Tags: []string{"ui", "badge", "status", "tag", "admin", "crud"}},
	{Name: "tabs", Category: CategoryUI, Description: "Tab navigation: underline (default — ink underline, sentence case, plain counts), pills, and boxed variants. Supports icons, badges, and render-prop children. Controlled when given `activeTab` (pass useTypedSearchParams state so the URL stays the single truth), uncontrolled otherwise.", Tags: []string{"ui", "tabs", "navigation", "admin", "dashboard", "controlled", "url"}},
	{Name: "key_value_list", Category: CategoryUI, Description: "Labeled field display for detail views with grouped Key: Value pairs, clipboard copy, and multi-column layouts.", Tags: []string{"ui", "detail", "crud", "admin", "fields"}},
	{Name: "pagination", Category: CategoryUI, Description: "Standalone pagination controls with page numbers, prev/next, and item count display.", Tags: []string{"ui", "pagination", "table", "crud", "admin"}},
	{Name: "search_input", Category: CategoryUI, Description: "Search input with magnifying glass icon, clear button, keyboard shortcut support, and multiple sizes.", Tags: []string{"ui", "search", "input", "filter", "admin"}},
	{Name: "modal", Category: CategoryUI, Description: "Generic modal dialog with header, body, footer sections. Supports ESC close, overlay click, and multiple sizes.", Tags: []string{"ui", "modal", "dialog", "overlay", "admin", "crud"}},
	{Name: "skeleton_loader", Category: CategoryUI, Description: "Configurable skeleton loading states for text, cards, table rows, list items, form fields, and custom shapes.", Tags: []string{"ui", "skeleton", "loading", "placeholder", "admin"}},
	{Name: "alert_banner", Category: CategoryUI, Description: "Info, success, warning, and error banner for top of page. Dismissible with optional action button. title/message take composed ReactNode, so a banner can carry a link or emphasis inside the sentence.", Tags: []string{"ui", "alert", "banner", "notification", "admin"}},
	{Name: "toggle_switch", Category: CategoryUI, Description: "Toggle/switch input with label, description, disabled state, and multiple sizes.", Tags: []string{"ui", "toggle", "switch", "input", "form", "admin"}},
	{Name: "activity_feed", Category: CategoryUI, Description: "Timeline of recent activity/events with user avatars, relative timestamps, and connecting lines.", Tags: []string{"ui", "activity", "feed", "timeline", "admin", "dashboard"}},
	{Name: "metric_card", Category: CategoryUI, Description: "Single metric display with trend indicator, sparkline chart, and optional link.", Tags: []string{"ui", "metric", "stat", "dashboard", "analytics", "admin"}},

	// ── UI Primitives (low-level building blocks consumed by frontend packs) ──
	{Name: "link", Category: CategoryUI, Description: "Navigation primitive other components route through (PageHeader actions, RowActionsMenu hrefs). Framework-neutral anchor by default; forge scaffolds overwrite it with a next/link or tanstack-router aware version so internal hrefs get client routing + basePath handling. External http(s)/mailto/tel hrefs always render a plain <a>.", Tags: []string{"ui", "primitive", "link", "navigation", "routing"}},
	{Name: "button", Category: CategoryUI, Description: "Generic button primitive with primary/secondary/outline/ghost/danger variants, sizes, and loading state.", Tags: []string{"ui", "primitive", "button", "form", "action"}},
	{Name: "input", Category: CategoryUI, Description: "Generic text input primitive with sizes, invalid state, and forwarded refs. Pair with the Label primitive.", Tags: []string{"ui", "primitive", "input", "form"}},
	{Name: "label", Category: CategoryUI, Description: "Form field label primitive with optional required-asterisk affordance.", Tags: []string{"ui", "primitive", "label", "form"}},
	{Name: "form", Category: CategoryUI, Description: "Form structural primitives — Form root plus FormField, FormError, FormActions for consistent layout and error display.", Tags: []string{"ui", "primitive", "form", "validation"}},
	{Name: "form_field_context", Category: CategoryUI, Description: "FormFieldContext — the id a FormField shares with the Label and control inside it. Imported by form, label, input and select; read it to make a custom control auto-bind the same way.", Tags: []string{"ui", "primitive", "form", "context"}},
	{Name: "card", Category: CategoryUI, Description: "Generic Card surface primitive (border, no shadow) with CardHeader (no rule under the title), CardBody, CardFooter and CardInset; its header comment is the library's elevation rule and sentence-case heading ladder. Distinct from MetricCard/StatGrid (domain components).", Tags: []string{"ui", "primitive", "card", "surface", "container"}},
	{Name: "table", Category: CategoryUI, Description: "Bare structural table primitives — Table, TableHeader, TableBody, TableRow, TableHead, TableCell. Pair with @tanstack/react-table for headless sort/filter.", Tags: []string{"ui", "primitive", "table", "data"}},
	{Name: "select", Category: CategoryUI, Description: "Generic select primitive with options array, sizes, and invalid state.", Tags: []string{"ui", "primitive", "select", "dropdown", "form"}},
	{Name: "chip", Category: CategoryUI, Description: "Removable chip/tag primitive for filter pills and selected tokens. Distinct from Badge (status-shaped).", Tags: []string{"ui", "primitive", "chip", "tag", "filter"}},
	{Name: "row_actions_menu", Category: CategoryUI, Description: "Kebab-trigger dropdown of row-scoped actions (suspend/resume/delete) for admin list pages. Specialised shape on top of DropdownMenu.", Tags: []string{"ui", "primitive", "menu", "actions", "table", "admin"}},
	{Name: "progress_bar", Category: CategoryUI, Description: "Value/max bar with optional label and auto-warning tint at >80%. Used in billing usage / quota / capacity displays.", Tags: []string{"ui", "primitive", "progress", "billing", "quota"}},
	{Name: "status", Category: CategoryUI, Description: "Status as a dot plus a word — the word tinted only when the state is not fine (warning/danger), neutral states in-progress via pulse. The one way to show a state in a table cell, a header or a meta line. Distinct from Tag (a kind label) and Badge (a filled pill).", Tags: []string{"ui", "primitive", "status", "indicator", "table", "health"}},
	{Name: "tag", Category: CategoryUI, Description: "Bordered, unfilled 20px label for a kind or a scope (an environment, a workload kind, a source), with neutral/warning/danger tones and an optional icon. Distinct from Status (a state) and Badge (a filled pill).", Tags: []string{"ui", "primitive", "tag", "label", "env", "kind"}},
	{Name: "mono", Category: CategoryUI, Description: "Machine identifier (sha, digest, pod name, request id) in monospace: truncated by shape (sha 8, digest 19), the full value in title, and a copy button. Use for ids only; names and versions stay in the body face.", Tags: []string{"ui", "primitive", "mono", "id", "sha", "digest", "copy", "clipboard"}},
}

// byName provides O(1) lookup.
var byName map[string]*Entry

func init() {
	byName = make(map[string]*Entry, len(registry))
	for i := range registry {
		c := &registry[i]
		c.FilePath = fmt.Sprintf("components/%s/%s.tsx", c.Category, c.Name)
		byName[c.Name] = c
	}
}

// Library provides access to the component library.
type Library struct{}

// NewLibrary creates a new component library instance.
func NewLibrary() *Library {
	return &Library{}
}

// Registry returns all component entries.
func (l *Library) Registry() []Entry {
	return registry
}

// ByName returns the name-to-entry lookup map.
func (l *Library) ByName() map[string]*Entry {
	return byName
}

// Search finds components using unified keyword search. See SearchDetailed
// for the semantics; this is the entries-only convenience wrapper.
func (l *Library) Search(query string) []Entry {
	entries, _, _ := l.SearchDetailed(query)
	return entries
}

// SearchDetailed finds components by keyword and reports how much of the
// query it was able to honour. The query is split into words; each word is
// matched against the component's name, tags, category, and description.
//
// Semantics are BEST-MATCH, not bag-of-words AND. A component's match count
// is how many query words it hits; the result set is every component whose
// count equals the BEST count any component achieved, ordered by a
// field-weighted score (name > tag > category > description).
//
// Why not AND: a search that gets WORSE as you describe your need more
// precisely is backwards, and that is exactly what AND does. `dashboard
// metric stat tiles` returned nothing while `dashboard metric stat` returned
// metric_card — one extra, more specific word emptied the library. Under
// best-match, adding a word that nothing carries cannot destroy a result set;
// it can only re-rank one. Everything a caller could want is preserved:
//
//   - one word — identical to the old behaviour;
//   - all words matched by something — identical to AND (best == len(words));
//   - nothing matched at all — still empty, so "no such component" is still
//     distinguishable from "here is the closest thing".
//
// Returns (entries, matched, total): `total` is the number of query words,
// `matched` the number the returned entries actually hit. matched < total is
// the caller's cue to say so rather than present a partial hit as exact.
func (l *Library) SearchDetailed(query string) (entries []Entry, matched, total int) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return registry, 0, 0
	}

	words := strings.Fields(query)

	type scored struct {
		entry Entry
		hits  int
		score int
	}
	best := 0
	all := make([]scored, 0, len(registry))
	for _, c := range registry {
		hits, score := scoreEntry(c, words)
		if hits == 0 {
			continue
		}
		if hits > best {
			best = hits
		}
		all = append(all, scored{entry: c, hits: hits, score: score})
	}
	if best == 0 {
		return nil, 0, len(words)
	}

	results := make([]scored, 0, len(all))
	for _, s := range all {
		if s.hits == best {
			results = append(results, s)
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].entry.Name < results[j].entry.Name
	})

	entries = make([]Entry, 0, len(results))
	for _, s := range results {
		entries = append(entries, s.entry)
	}
	return entries, best, len(words)
}

// Get retrieves the source code for a component by name.
func (l *Library) Get(name string) (string, error) {
	entry, exists := byName[name]
	if !exists {
		return "", fmt.Errorf("component '%s' not found", name)
	}

	content, err := componentsFS.ReadFile(entry.FilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read component: %w", err)
	}

	return string(content), nil
}

// GetEntry retrieves the metadata entry for a component by name.
func (l *Library) GetEntry(name string) (*Entry, bool) {
	entry, exists := byName[name]
	return entry, exists
}

// List returns all components, optionally filtered by tag and/or category.
func (l *Library) List(tag, category string) []Entry {
	tag = strings.ToLower(strings.TrimSpace(tag))
	category = strings.ToLower(strings.TrimSpace(category))

	if tag == "" && category == "" {
		return registry
	}

	var filtered []Entry
	for _, c := range registry {
		if category != "" && string(c.Category) != category {
			continue
		}
		if tag != "" && !hasTag(c.Tags, tag) {
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered
}

// FindSimilar returns up to 5 component names similar to the given name.
func (l *Library) FindSimilar(name string) []string {
	name = strings.ToLower(name)
	var matches []string
	for _, c := range registry {
		cName := strings.ToLower(c.Name)
		if strings.Contains(cName, name) || strings.Contains(name, cName) {
			matches = append(matches, c.Name)
		}
		prefix := commonPrefix(name, cName)
		if len(prefix) >= 3 {
			matches = append(matches, c.Name)
		}
	}
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

// FormatComponentList formats a list of entries into a human-readable string
// grouped by category.
func FormatComponentList(entries []Entry) string {
	grouped := make(map[Category][]Entry)
	for _, e := range entries {
		grouped[e.Category] = append(grouped[e.Category], e)
	}

	order := []Category{CategoryLayouts, CategoryCharts, CategoryDiagrams, CategoryDeck, CategoryUI}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d components:\n", len(entries))

	for _, cat := range order {
		items, ok := grouped[cat]
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, "\n## %s (%d)\n", strings.ToUpper(string(cat)), len(items))
		for _, item := range items {
			fmt.Fprintf(&sb, "  • %s — %s\n", item.Name, item.Description)
			fmt.Fprintf(&sb, "    Tags: %s\n", strings.Join(item.Tags, ", "))
		}
	}

	sb.WriteString("\nUse action='get' with name='<component_name>' to retrieve the full source code.")
	return sb.String()
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

// Field weights for search ranking. A word found in the component's NAME is
// a far stronger signal of intent than the same word buried in a prose
// description, which is why "metric" should surface metric_card ahead of
// every component whose description happens to mention metrics.
const (
	weightName        = 8
	weightTag         = 4
	weightCategory    = 2
	weightDescription = 1
)

// scoreEntry returns how many of `words` the entry matches (hits) and a
// field-weighted relevance score used to order entries within the same hit
// count. Each word contributes its BEST field weight once, so a word in both
// the name and the description doesn't outrank a word in two names.
func scoreEntry(c Entry, words []string) (hits, score int) {
	nameLower := strings.ToLower(c.Name)
	descLower := strings.ToLower(c.Description)
	catLower := string(c.Category)

	for _, word := range words {
		w := wordWeight(c, word, nameLower, descLower, catLower)
		if w == 0 {
			continue
		}
		hits++
		score += w
	}
	return hits, score
}

// wordWeight returns the strongest field weight at which `word` matches the
// entry, or 0 when it doesn't match at all.
func wordWeight(c Entry, word, nameLower, descLower, catLower string) int {
	if strings.Contains(nameLower, word) {
		return weightName
	}
	for _, tag := range c.Tags {
		if strings.Contains(strings.ToLower(tag), word) {
			return weightTag
		}
	}
	if strings.Contains(catLower, word) {
		return weightCategory
	}
	if strings.Contains(descLower, word) {
		return weightDescription
	}
	return 0
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
