package components

import (
	"strings"
	"testing"
)

// The popover ships verbatim into every frontend. These guards pin how it
// decides "inside" for dismissal, under -short; the behaviour itself is
// proven in a real DOM by testdata/behavior/popover.test.tsx
// (TestComponentBehaviour, full tier).

// TestPopoverTreatsPortalledDescendantsAsInside: a control in the panel may
// portal its own layer to <body> (a nested popover, a full-screen picker
// sheet on touch), which is outside the panel's DOM but inside its React
// tree. React events bubble through portals, so the panel's capture handlers
// record the native press and focus events they saw there (the event itself,
// so one React never dispatched cannot leave a stale mark), and the native
// outside-press and focusout listeners skip the close when they did (focusout
// hands the decision to the document focusin that follows, so the close stays
// synchronous) — otherwise a tap on the portalled layer dismisses the panel
// and unmounts the control that opened it.
func TestPopoverTreatsPortalledDescendantsAsInside(t *testing.T) {
	t.Parallel()
	src, err := NewLibrary().Get("popover")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`onPointerDownCapture={(e) => {`,
		`pressInTree.current = e.nativeEvent;`,
		`onFocusCapture={(e) => {`,
		`focusInTree.current = e.nativeEvent;`,
		// the document pointerdown listener reads and resets the record
		`const inTree = pressInTree.current === e;`,
		`if (inTree || triggerRef.current?.contains(target) || panelRef.current?.contains(target))`,
		// focusout only marks that focus left; the document focusin that follows,
		// which runs after React's listeners, decides synchronously
		`focusLeft = true;`,
		`document.addEventListener("focusin", onFocusIn);`,
		`const inTree = focusInTree.current === e;`,
		`if (!inTree && !triggerRef.current?.contains(document.activeElement)) close(false);`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("popover.tsx no longer contains %q", want)
		}
	}
}
