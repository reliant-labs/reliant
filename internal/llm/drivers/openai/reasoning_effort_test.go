// Copyright (c) 2025 Reliant Labs
package openai

import "testing"

// Efforts above the SDK's enum have to reach the wire verbatim. This driver
// previously mapped effort with three separate switch statements that each fell
// back to medium for anything past xhigh, so selecting gpt-6-astra at "max"
// silently ran it at medium — a quiet 2-tier downgrade with no error to notice.
func TestReasoningEffort_PassesThroughLevelsAboveSDKEnum(t *testing.T) {
	for _, level := range []string{"xhigh", "max", "ultra"} {
		t.Run(level, func(t *testing.T) {
			if got := string(reasoningEffort(level)); got != level {
				t.Errorf("reasoningEffort(%q) = %q, want %q", level, got, level)
			}
		})
	}
}

// An unset effort still has to land on a usable default rather than sending an
// empty string, which the API rejects.
func TestReasoningEffort_DefaultsEmptyToMedium(t *testing.T) {
	if got := string(reasoningEffort("")); got != "medium" {
		t.Errorf(`reasoningEffort("") = %q, want "medium"`, got)
	}
}

func TestReasoningEffort_MapsKnownLevels(t *testing.T) {
	for _, level := range []string{"low", "medium", "high"} {
		if got := string(reasoningEffort(level)); got != level {
			t.Errorf("reasoningEffort(%q) = %q, want %q", level, got, level)
		}
	}
}
