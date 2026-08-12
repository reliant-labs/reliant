// Copyright (c) 2025 Reliant Labs
package models

import "testing"

// Adding a level to a model's thinking_levels is useless if the validation
// gates upstream of the driver don't recognize it — they reject or silently
// downgrade the request before it is ever built. This pins the vocabulary so a
// new level has to be added here, in the one place every gate consults.
func TestIsKnownThinkingLevel(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
		if !IsKnownThinkingLevel(level) {
			t.Errorf("expected %q to be a known thinking level", level)
		}
	}

	for _, level := range []string{"", "auto", "off", "bogus", "MAX"} {
		if IsKnownThinkingLevel(level) {
			t.Errorf("expected %q not to be a known thinking level", level)
		}
	}
}

// A known level must never become the auto-selected default just by existing —
// "prefer medium" has to win first, or adding `ultra` to a model would quietly
// make every unpinned request maximally expensive.
func TestPreferredThinkingLevelPrefersMediumOverNewHighTiers(t *testing.T) {
	all := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	if got := PreferredThinkingLevel(all); got != "medium" {
		t.Errorf("expected medium to win when available, got %q", got)
	}

	// Without medium, fall to the highest conventional tier rather than the
	// most expensive one.
	if got := PreferredThinkingLevel([]string{"low", "high", "xhigh", "max", "ultra"}); got != "xhigh" {
		t.Errorf("expected xhigh, got %q", got)
	}

	if got := PreferredThinkingLevel([]string{"max", "ultra"}); got != "ultra" {
		t.Errorf("expected ultra when it is the only tier available, got %q", got)
	}
}
