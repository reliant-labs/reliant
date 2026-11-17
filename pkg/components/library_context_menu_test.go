package components

import (
	"strings"
	"testing"
)

// The context menu is dropdown_menu anchored to a point, so the menu
// contract (roles, keyboard, focus hand-back) is pinned once, on
// dropdown_menu and popover. These guards pin what the point anchor adds;
// the behaviour is exercised by the consuming app's tests (reliant:
// frontends/web/src/components/forge-ui/__tests__/context_menu.test.tsx).

func TestContextMenuIsADropdownAtAPoint(t *testing.T) {
	t.Parallel()
	src, err := NewLibrary().Get("context_menu")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`from "./dropdown_menu"`, // one menu implementation, not two
		`anchor={position}`,
		`addEventListener("contextmenu"`,
		`e.preventDefault()`,   // the browser's menu yields only where ours opens
		`event.detail === 0`,   // Shift+F10 / the ContextMenu key: no pointer position
		`via: "keyboard"`,      // ...so focus lands on the first item
		`className="contents"`, // the region wrapper adds no box
	} {
		if !strings.Contains(src, want) {
			t.Errorf("context_menu.tsx no longer contains %q", want)
		}
	}
}

// TestPopoverHandsFocusBackWithoutATrigger: a panel opened at a point has no
// trigger to return focus to, so it returns it to what had focus when it
// opened — otherwise Escape drops a keyboard user at the top of the page.
func TestPopoverHandsFocusBackWithoutATrigger(t *testing.T) {
	t.Parallel()
	src, err := NewLibrary().Get("popover")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`(triggerRef.current ?? restoreFocusRef.current)?.focus()`,
		`restoreFocusRef.current =`,
		`anchor?: { x: number; y: number };`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("popover.tsx no longer contains %q", want)
		}
	}
}
