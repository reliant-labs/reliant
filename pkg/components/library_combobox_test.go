package components

import (
	"strings"
	"testing"
)

// The Combobox ships verbatim into every frontend. These guards pin the
// WAI-ARIA combobox contract its header comment promises; the behaviour
// itself is exercised by the consuming app's tests (reliant:
// frontends/web/src/components/forge-ui/__tests__/combobox.test.tsx).

func comboboxSource(t *testing.T) string {
	t.Helper()
	src, err := NewLibrary().Get("combobox")
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestComboboxKeepsFocusInTheSearch: the options are named by
// aria-activedescendant and never focused, so a press on one must not move
// focus out of the search box (the delegated mousedown preventDefault), and
// the panel opens with the search box focused.
func TestComboboxKeepsFocusInTheSearch(t *testing.T) {
	t.Parallel()
	src := comboboxSource(t)
	for _, want := range []string{
		`role="combobox"`,
		`aria-activedescendant=`,
		`aria-controls={listboxId}`,
		`role="listbox"`,
		`role="option"`,
		`aria-selected={isSelected}`,
		`aria-multiselectable=`,
		`if (optionAt(e.target)) e.preventDefault();`,
		`input[role="combobox"]')?.focus(`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("combobox.tsx no longer contains %q", want)
		}
	}
}

// TestComboboxTriggerReadsItsChoice: a <Label> names the trigger button, which
// replaces its content as the name, so the chosen value is attached as the
// description — otherwise a screen reader hears the field and never its value.
func TestComboboxTriggerReadsItsChoice(t *testing.T) {
	t.Parallel()
	src := comboboxSource(t)
	for _, want := range []string{
		`aria-describedby={valueId}`,
		`<span id={valueId}`,
		`FormFieldContext`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("combobox.tsx no longer contains %q", want)
		}
	}
}

// TestComboboxActiveOptionSurvivesForcedColors: forced-colors mode replaces
// the active option's fill, so the keyboard position needs an outline there.
func TestComboboxActiveOptionSurvivesForcedColors(t *testing.T) {
	t.Parallel()
	if !strings.Contains(comboboxSource(t), "forced-colors:outline") {
		t.Error("combobox.tsx's active option must carry a forced-colors outline")
	}
}
