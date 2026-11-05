// Copyright (c) 2025 Reliant Labs
package components

import (
	"regexp"
	"strings"
	"testing"
)

// The console visual system (card.tsx's header comment is its statement):
// sentence-case labels, one surface level, shadows only on overlays. These
// guards pin the rules that a later edit most easily undoes, because each
// violation looks harmless in the one component it lands in.

// uppercaseLabel matches the small-caps label the old heading ladder used for
// every table header, nav section and dropdown group.
var uppercaseLabel = regexp.MustCompile(`\buppercase\b[^"` + "`" + `]*\btracking-wide`)

// TestChromeLabelsAreSentenceCase: an `uppercase tracking-wider` label makes
// the chrome shout over the data. Marketing and diagram categories keep their
// eyebrows; the app chrome (ui/, layouts/) does not.
func TestChromeLabelsAreSentenceCase(t *testing.T) {
	lib := NewLibrary()
	checked := 0
	for _, entry := range lib.Registry() {
		if entry.Category != CategoryUI && entry.Category != CategoryLayouts {
			continue
		}
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		checked++
		for i, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if uppercaseLabel.MatchString(line) {
				t.Errorf("%s.tsx:%d: `uppercase tracking-wide*` label — use sentence case in text-xs text-ink-muted (see card.tsx's heading ladder):\n  %s",
					entry.Name, i+1, trimmed)
			}
		}
	}
	if checked == 0 {
		t.Fatal("checked zero ui/layout components — the category filter broke")
	}
}

// surfaceShadow matches a box-shadow utility, including hover/focus variants.
var surfaceShadow = regexp.MustCompile(`(^|[\s"` + "`" + `:])shadow-(sm|md|lg|xl|2xl)\b`)

// overlays are the components that sit ON TOP of the page and so may cast a
// shadow. Everything else is a surface, told apart by border and fill.
var overlays = map[string]bool{
	"modal": true, "confirmation_dialog": true, "dropdown_menu": true, "command_bar": true,
	"toast_notification": true, "row_actions_menu": true, "popover": true, "context_menu": true, "tooltip": true, "combobox": true,
	// The knob of a switch is a physical affordance, not a panel.
	"toggle_switch": true,
	// Marketing pages, not app chrome.
	"hero_section": true, "pricing_table": true, "login_form": true,
}

// TestOnlyOverlaysCastShadows: a shadow on every card makes panels float over
// each other, and then a shadow can no longer say "this is on top" for the
// menu that really is.
func TestOnlyOverlaysCastShadows(t *testing.T) {
	lib := NewLibrary()
	for _, entry := range lib.Registry() {
		if entry.Category != CategoryUI && entry.Category != CategoryLayouts {
			continue
		}
		if overlays[entry.Name] {
			continue
		}
		src, err := lib.Get(entry.Name)
		if err != nil {
			t.Fatalf("get %s: %v", entry.Name, err)
		}
		for i, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if surfaceShadow.MatchString(line) {
				t.Errorf("%s.tsx:%d: a surface casts a shadow — only overlays do (card.tsx's elevation rule):\n  %s",
					entry.Name, i+1, trimmed)
			}
		}
	}
}

// TestSidebarNavContract pins the props the console's nav is built from, so a
// refactor that drops one fails here rather than in a downstream app's build.
func TestSidebarNavContract(t *testing.T) {
	src, err := NewLibrary().Get("sidebar_layout")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"children?: SidebarNavItem[]", // sub-pages, expanded inline
		"badge?: SidebarBadge",        // counts, dots, "New"
		"footerItems?: SidebarNavItem[]",
		`type: "new"`,
		"containsActive", // open state is derived from the route, never stored
	} {
		if !strings.Contains(src, want) {
			t.Errorf("sidebar_layout.tsx no longer contains %q", want)
		}
	}
	if strings.Contains(src, "uppercase") {
		t.Error("sidebar_layout.tsx section labels must be plain sentence-case text")
	}
}

// TestSidebarLayoutBecomesADrawerOnPhones pins the narrow-screen contract: a
// fixed sidebar column on a 390px screen left the page about 150px. Below
// `md` the nav is an off-canvas drawer behind a menu button, and its rows are
// touch-sized on a coarse pointer.
func TestSidebarLayoutBecomesADrawerOnPhones(t *testing.T) {
	src, err := NewLibrary().Get("sidebar_layout")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`aria-label="Open navigation"`,       // the way in, in the top bar
		`aria-controls="sidebar-layout-nav"`, // which names the drawer
		"max-md:fixed",                       // the nav leaves the layout
		"max-md:-translate-x-full",           // and is off-canvas until opened
		`role="presentation"`,                // the scrim is a mouse convenience, not a control
		`event.key !== "Escape"`,             // Escape closes it
		"[@media(pointer:coarse)]:min-h-[44px]",
		"env(safe-area-inset-top)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("sidebar_layout.tsx no longer contains %q", want)
		}
	}
}

// TestDataTableStacksOnPhones pins `stacked`: below `md` a console list is a
// list of cards — header kept for screen readers only, each non-title cell
// labelled by its column header — instead of eight columns squeezed to 390px.
func TestDataTableStacksOnPhones(t *testing.T) {
	src, err := NewLibrary().Get("data_table")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"stacked?: boolean",
		"stackTitle?: boolean",
		`head: "max-md:sr-only"`,                   // the header stays readable to a screen reader
		"max-md:before:content-[attr(data-label)]", // each line led by its column's name
		"data-label={stacked && !isTitle ? col.header : undefined}",
		"getRowLabel?: (row: T, index: number) => string | undefined",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("data_table.tsx no longer contains %q", want)
		}
	}
}

// TestDataTableScrollerContainsHiddenHeaders pins the scroller's `relative`.
// The visually hidden header labels (`sr-only`, e.g. an Actions column) are
// absolutely positioned; with no positioned ancestor inside the
// `overflow-x-auto` wrapper they escaped it, and at 390px the whole page
// scrolled sideways (Settings → Machines measured scrollWidth 617).
func TestDataTableScrollerContainsHiddenHeaders(t *testing.T) {
	src, err := NewLibrary().Get("data_table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, `<div className="relative overflow-x-auto">`) {
		t.Error(`data_table.tsx's scroller must be "relative overflow-x-auto" so absolutely positioned sr-only headers stay inside it`)
	}
}
