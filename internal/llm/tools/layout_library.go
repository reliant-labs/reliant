// Copyright (c) 2025 Reliant Labs
package tools

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
)

//go:embed layouts/hero_centered.html
var herocenteredLayout string

//go:embed layouts/sidebar_left.html
var sidebarLeftLayout string

//go:embed layouts/sidebar_right.html
var sidebarRightLayout string

//go:embed layouts/dashboard_grid.html
var dashboardGridLayout string

//go:embed layouts/card_grid.html
var cardGridLayout string

//go:embed layouts/split_view.html
var splitViewLayout string

//go:embed layouts/kanban_board.html
var kanbanBoardLayout string

//go:embed layouts/form_wizard.html
var formWizardLayout string

//go:embed layouts/timeline.html
var timelineLayout string

//go:embed layouts/masonry.html
var masonryLayout string

type LayoutName string

const (
	LayoutHeroCentered  LayoutName = "hero_centered"
	LayoutSidebarLeft   LayoutName = "sidebar_left"
	LayoutSidebarRight  LayoutName = "sidebar_right"
	LayoutDashboardGrid LayoutName = "dashboard_grid"
	LayoutCardGrid      LayoutName = "card_grid"
	LayoutSplitView     LayoutName = "split_view"
	LayoutKanbanBoard   LayoutName = "kanban_board"
	LayoutFormWizard    LayoutName = "form_wizard"
	LayoutTimeline      LayoutName = "timeline"
	LayoutMasonry       LayoutName = "masonry"
)

type LayoutParams struct {
	Action string     `json:"action" jsonschema:"required,enum=get,enum=list,description=Action to perform: 'get' retrieves a specific layout, 'list' returns all available layouts"`
	Layout LayoutName `json:"layout,omitempty" jsonschema:"enum=hero_centered,enum=sidebar_left,enum=sidebar_right,enum=dashboard_grid,enum=card_grid,enum=split_view,enum=kanban_board,enum=form_wizard,enum=timeline,enum=masonry,description=The layout to retrieve (required when action is 'get')"`
}

type layoutLibraryTool struct{}

type LayoutInfo struct {
	Name        LayoutName `json:"name"`
	Description string     `json:"description"`
	UseCase     string     `json:"use_case"`
}

var layoutRegistry = map[LayoutName]struct {
	content     string
	description string
	useCase     string
}{
	LayoutHeroCentered: {
		content:     herocenteredLayout,
		description: "Hero section with centered content, headline, subheadline, and CTA buttons",
		useCase:     "Landing pages, marketing sites, product announcements",
	},
	LayoutSidebarLeft: {
		content:     sidebarLeftLayout,
		description: "Fixed left sidebar with main content area and responsive navigation",
		useCase:     "Admin panels, documentation sites, dashboards",
	},
	LayoutSidebarRight: {
		content:     sidebarRightLayout,
		description: "Fixed right sidebar with main content area and contextual information",
		useCase:     "Blog layouts, help panels, supplementary content",
	},
	LayoutDashboardGrid: {
		content:     dashboardGridLayout,
		description: "Responsive grid layout with metric cards and charts",
		useCase:     "Analytics dashboards, monitoring interfaces, data visualization",
	},
	LayoutCardGrid: {
		content:     cardGridLayout,
		description: "Responsive card grid with equal-height cards and hover effects",
		useCase:     "Product catalogs, galleries, team directories",
	},
	LayoutSplitView: {
		content:     splitViewLayout,
		description: "Two-pane layout with resizable divider for comparison views",
		useCase:     "Code editors, diff viewers, before/after comparisons",
	},
	LayoutKanbanBoard: {
		content:     kanbanBoardLayout,
		description: "Multi-column board with draggable cards for task management",
		useCase:     "Project management, workflow visualization, task tracking",
	},
	LayoutFormWizard: {
		content:     formWizardLayout,
		description: "Multi-step form with progress indicator and validation",
		useCase:     "Onboarding flows, checkout processes, complex forms",
	},
	LayoutTimeline: {
		content:     timelineLayout,
		description: "Vertical timeline with alternating content blocks",
		useCase:     "Company history, project milestones, event schedules",
	},
	LayoutMasonry: {
		content:     masonryLayout,
		description: "Pinterest-style masonry grid with variable height items",
		useCase:     "Image galleries, portfolio showcases, content feeds",
	},
}

func NewLayoutLibraryTool() Tool {
	tool := &layoutLibraryTool{}
	return NewToolWrapper[LayoutParams, ToolResponse](tool)
}

func (t *layoutLibraryTool) RequiresPermission(params LayoutParams) (bool, error) {
	// layout_library tool doesn't require permissions as it's read-only
	return false, nil
}

func (t *layoutLibraryTool) Name() string {
	return "layout_library"
}

func (t *layoutLibraryTool) Description() string {
	return `Layout library tool that provides pre-built, accessible, and responsive HTML/CSS layout templates for UX design.

WHEN TO USE:
- Creating new UI layouts quickly
- Getting responsive, accessible layout templates
- Prototyping user interfaces
- Establishing consistent layout patterns

HOW TO USE:
- action: "list" - Get all available layouts with descriptions
- action: "get" with layout name - Retrieve specific layout HTML/CSS

FEATURES:
- 10 pre-built responsive layouts
- Accessibility-first design
- Mobile-responsive
- Semantic HTML structure`
}

func (t *layoutLibraryTool) Execute(rctx *rctx.ToolContext, params LayoutParams) (ToolResponse, error) {
	switch params.Action {
	case "list":
		return t.listLayouts()
	case "get":
		if params.Layout == "" {
			return NewTextErrorResponse("Layout name is required when action is 'get'"), nil
		}
		return t.getLayout(params.Layout)
	default:
		return NewTextErrorResponse(fmt.Sprintf("Unknown action: %s. Use 'list' or 'get'", params.Action)), nil
	}
}

func (t *layoutLibraryTool) listLayouts() (ToolResponse, error) {
	var layouts []LayoutInfo
	for name, info := range layoutRegistry {
		layouts = append(layouts, LayoutInfo{
			Name:        name,
			Description: info.description,
			UseCase:     info.useCase,
		})
	}

	var sb strings.Builder
	sb.WriteString("Available Layouts:\n\n")
	for _, layout := range layouts {
		fmt.Fprintf(&sb, "• %s\n", layout.Name)
		fmt.Fprintf(&sb, "  Description: %s\n", layout.Description)
		fmt.Fprintf(&sb, "  Use Case: %s\n\n", layout.UseCase)
	}

	response := NewTextResponse(sb.String())
	return WithResponseMetadata(response, map[string]interface{}{
		"layouts": layouts,
	}), nil
}

func (t *layoutLibraryTool) getLayout(name LayoutName) (ToolResponse, error) {
	layout, exists := layoutRegistry[name]
	if !exists {
		return NewTextErrorResponse(fmt.Sprintf("Layout '%s' not found", name)), nil
	}

	response := NewTextResponse(layout.content)
	return WithResponseMetadata(response, map[string]interface{}{
		"layout_name": name,
		"description": layout.description,
		"use_case":    layout.useCase,
	}), nil
}
