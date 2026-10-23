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
	"toast_notification": true, "row_actions_menu": true,
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
