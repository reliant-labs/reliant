package components

import (
	"strings"
	"testing"
)

// The Tooltip ships verbatim into every frontend and wraps a large share of
// any app's controls, so a regression in it is an accessibility regression
// everywhere at once. These guards pin the behaviour its header comment
// promises; the behaviour itself is exercised by the consuming app's tests
// (reliant: frontends/web/src/components/forge-ui/__tests__/tooltip.test.tsx).

func tooltipSource(t *testing.T) string {
	t.Helper()
	src, err := NewLibrary().Get("tooltip")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestTooltipTextIsAlwaysInTheDOM: aria-describedby pointing at a bubble
// that exists only while visible describes nothing the rest of the time,
// which is when a screen reader user lands on the control.
func TestTooltipTextIsAlwaysInTheDOM(t *testing.T) {
	t.Parallel()
	src := tooltipSource(t)
	for _, want := range []string{
		`<span id={textId} hidden>`,
		`"aria-labelledby" : "aria-describedby"`,
		`role="tooltip"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("tooltip.tsx no longer contains %q", want)
		}
	}
}

// TestTooltipMeetsContentOnHoverOrFocus pins WCAG 1.4.13's three conditions
// and the WAI-ARIA keyboard behaviour: dismissable (Escape), hoverable (a
// grace period to reach the bubble), persistent (no auto-hide timer), shown
// on keyboard focus only, never on touch.
func TestTooltipMeetsContentOnHoverOrFocus(t *testing.T) {
	t.Parallel()
	src := tooltipSource(t)
	for _, want := range []string{
		`e.key !== "Escape"`,
		`e.stopImmediatePropagation()`,
		`HOVER_GRACE_MS`,
		`bubble?.addEventListener("pointerenter"`,
		`":focus-visible"`,
		`e.pointerType === "touch"`,
		`getAttribute("aria-expanded") === "true"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("tooltip.tsx no longer contains %q", want)
		}
	}
}

// TestTooltipReachesDisabledTriggers: a disabled button receives no focus,
// so the wrapper must take the hover and, when nothing inside it can focus,
// take focus itself — otherwise "why is this disabled" is mouse-only.
func TestTooltipReachesDisabledTriggers(t *testing.T) {
	t.Parallel()
	src := tooltipSource(t)
	for _, want := range []string{
		`host.querySelector<HTMLElement>(FOCUSABLE)`,
		`host.tabIndex = 0`,
		`focus-visible:outline-accent`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("tooltip.tsx no longer contains %q", want)
		}
	}
}

// TestTooltipSurvivesForcedColors: forced-colors mode replaces the bubble's
// fill with the page's, so without a border the hint is text floating over
// the control it describes. A transparent border is drawn in forced colors.
func TestTooltipSurvivesForcedColors(t *testing.T) {
	t.Parallel()
	src := tooltipSource(t)
	if !strings.Contains(src, "border border-transparent bg-ink") {
		t.Error("tooltip.tsx's bubble must carry `border border-transparent` beside its fill, for forced-colors mode")
	}
}

// TestTooltipAttachesOnce: a tooltip wraps a large share of an app's controls
// (reliant: 226 sites), so its per-render cost is paid hundreds of times on
// every list re-render. The description is written to the DOM only when its
// target moves, and the target lookup tries the wrapper's first child before
// walking the subtree. Measured in jsdom over 600 tooltips × 5 re-renders,
// re-attaching every render made re-rendering 3.5× the cost of the old
// hand-rolled tooltip.
func TestTooltipAttachesOnce(t *testing.T) {
	t.Parallel()
	src := tooltipSource(t)
	for _, want := range []string{
		`if (cur && (cur.el !== target || cur.attr !== attr)) detach();`,
		`first.matches(FOCUSABLE)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("tooltip.tsx no longer contains %q", want)
		}
	}
}
