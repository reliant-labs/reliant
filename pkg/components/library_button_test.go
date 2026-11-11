package components

import (
	"regexp"
	"strings"
	"testing"
)

// The Button is the most-used component in every frontend forge scaffolds,
// and it ships verbatim: a project cannot patch it without losing the edit
// on the next install. These guards pin the behaviour its header comment
// promises. Contrast across both scaffold palettes is checked separately in
// internal/templates/frontend_button_contrast_test.go, which can read the
// themes.

// buttonSource is the Button's two files: the component and the classes it
// composes (button_classes.tsx, its own module for React Fast Refresh).
func buttonSource(t *testing.T) string {
	t.Helper()
	var src strings.Builder
	for _, name := range []string{"button", "button_classes"} {
		s, err := NewLibrary().Get(name)
		if err != nil {
			t.Fatal(err)
		}
		src.WriteString(s)
		src.WriteString("\n")
	}
	return src.String()
}

// TestButtonExportsOnlyAComponent: a file that exports a component and a
// function defeats React Fast Refresh, and a scaffolded vite-spa's eslint
// (react-refresh/only-export-components) warns on it in every new project.
// buttonClasses lives in button_classes.tsx; button.tsx re-exports types only.
func TestButtonExportsOnlyAComponent(t *testing.T) {
	t.Parallel()
	src, err := NewLibrary().Get("button")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^export (?:function|const|let|class) (\w+)`).FindAllStringSubmatch(src, -1) {
		t.Errorf("button.tsx exports %s, which is not a component; move it to button_classes.tsx", m[1])
	}
	if !strings.Contains(src, `from "./button_classes"`) {
		t.Error(`button.tsx must import its classes from "./button_classes"`)
	}
}

// TestButtonSizesShareOneScale: every size declares a height, a square for
// icon-only use and an icon size, so a toolbar of mixed buttons lines up.
func TestButtonSizesShareOneScale(t *testing.T) {
	t.Parallel()
	src := buttonSource(t)
	if !strings.Contains(src, `export type ButtonSize = "xs" | "sm" | "md" | "lg" | "xl";`) {
		t.Fatal("ButtonSize must be exactly xs | sm | md | lg | xl")
	}
	for _, want := range []struct{ size, height, square, icon string }{
		{"xs", "h-6", "size-6", "[&_svg]:size-3"},
		{"sm", "h-7", "size-7", "[&_svg]:size-3.5"},
		{"md", "h-8", "size-8", "[&_svg]:size-4"},
		{"lg", "h-9", "size-9", "[&_svg]:size-4"},
		{"xl", "h-10", "size-10", "[&_svg]:size-5"},
	} {
		block := regexp.MustCompile(`(?s)\n  ` + want.size + `: \{(.*?)\n  \},`).FindStringSubmatch(src)
		if block == nil {
			t.Errorf("sizeStyles has no %s entry", want.size)
			continue
		}
		for _, cls := range []string{want.height, want.square, want.icon} {
			if !strings.Contains(block[1], `"`+cls) && !strings.Contains(block[1], " "+cls) {
				t.Errorf("size %s: want %q in %s", want.size, cls, strings.TrimSpace(block[1]))
			}
		}
	}
}

// TestButtonLoadingHoldsItsWidth: a button that changes width when clicked
// moves the thing under the pointer. The label fades in place (it keeps its
// box and stays the accessible name) instead of being swapped for a spinner.
func TestButtonLoadingHoldsItsWidth(t *testing.T) {
	t.Parallel()
	src := buttonSource(t)
	for _, want := range []string{
		"loading?: boolean",
		"aria-busy={loading || undefined}",
		`const fade = spinnerOverlay ? "opacity-0" : "";`,
		"absolute inset-0", // the spinner overlays; it does not take a flex slot
		"disabled={inert}",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("button.tsx no longer contains %q", want)
		}
	}
	if strings.Contains(src, "isLoading") {
		t.Error("button.tsx: the loading prop is `loading`; `isLoading` was renamed (see the migrations/v1.0.0 upgrade playbook)")
	}
}

// TestButtonFocusIsAnOutline: forced-colors mode (Windows high contrast)
// drops box-shadow, so a `ring-*` focus indicator disappears for exactly the
// users who need it most. The indicator is a 2px outline offset from the
// edge, keyboard only.
func TestButtonFocusIsAnOutline(t *testing.T) {
	t.Parallel()
	src := buttonSource(t)
	for _, want := range []string{"focus-visible:outline-2", "focus-visible:outline-offset-2", "focus-visible:outline-accent"} {
		if !strings.Contains(src, want) {
			t.Errorf("button.tsx no longer contains %q", want)
		}
	}
	if regexp.MustCompile(`focus(-visible)?:ring-`).MatchString(src) {
		t.Error("button.tsx draws focus with a ring (box-shadow); forced-colors mode removes it — use an outline")
	}
	if strings.Contains(src, `"focus:outline-none`) || strings.Contains(src, " focus:outline-none") {
		t.Error("button.tsx removes the outline on :focus; that hides the indicator in browsers without :focus-visible heuristics")
	}
}

// TestButtonIconOnlyRequiresAName: the type, not a lint rule, makes an
// icon-only button carry an aria-label.
func TestButtonIconOnlyRequiresAName(t *testing.T) {
	t.Parallel()
	src := buttonSource(t)
	block := regexp.MustCompile(`(?s)interface IconOnlyContent \{(.*?)\n\}`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("button.tsx has no IconOnlyContent interface")
	}
	if !strings.Contains(block[1], `"aria-label": string;`) {
		t.Errorf("IconOnlyContent must require `\"aria-label\": string;`, got:\n%s", block[1])
	}
}
