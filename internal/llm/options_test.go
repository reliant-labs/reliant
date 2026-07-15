// Copyright (c) 2025 Reliant Labs
package llm

import "testing"

func TestWithReasoningEffort(t *testing.T) {
	tests := []struct {
		name     string
		effort   string
		expected string
	}{
		// Explicit valid levels pass through.
		{name: "low", effort: "low", expected: "low"},
		{name: "medium", effort: "medium", expected: "medium"},
		{name: "high", effort: "high", expected: "high"},
		{name: "xhigh", effort: "xhigh", expected: "xhigh"},

		// Normalization: case and whitespace.
		{name: "uppercase", effort: "HIGH", expected: "high"},
		{name: "whitespace", effort: " medium ", expected: "medium"},

		// Auto/unset: preferences without a pinned level store "" (the UI's
		// "Auto" option) — this must silently default, never warn.
		{name: "empty means auto", effort: "", expected: "medium"},
		{name: "auto keyword", effort: "auto", expected: "medium"},

		// Off vocabulary normalizes to the drivers' recognized off value.
		{name: "off", effort: "off", expected: "disabled"},
		{name: "none", effort: "none", expected: "disabled"},
		{name: "disabled", effort: "disabled", expected: "disabled"},

		// Genuinely invalid values fall back to medium.
		{name: "garbage", effort: "banana", expected: "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts DriverOptions
			WithReasoningEffort(tt.effort)(&opts)
			if opts.ReasoningEffort != tt.expected {
				t.Errorf("WithReasoningEffort(%q) = %q, want %q",
					tt.effort, opts.ReasoningEffort, tt.expected)
			}
		})
	}
}
