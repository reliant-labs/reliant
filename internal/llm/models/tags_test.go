// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"
)

func TestIsModelTag(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"@smart", true},
		{"@default", true},
		{"@fast", true},
		{"@unknown", true}, // IsModelTag only checks prefix, not validity
		{"smart", false},
		{"claude-4.5-sonnet", false},
		{"", false},
		{"model@driver", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsModelTag(tt.input)
			if got != tt.want {
				t.Errorf("IsModelTag(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsValidModelTag(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Valid tags
		{"@smart", true},
		{"@default", true},
		{"@fast", true},

		// Invalid tags
		{"@unknown", false},
		{"@SMART", false},   // case sensitive
		{"@Default", false}, // case sensitive
		{"@Fast", false},    // case sensitive
		{"smart", false},    // missing @
		{"", false},
		{"claude-4.5-sonnet", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsValidModelTag(tt.input)
			if got != tt.want {
				t.Errorf("IsValidModelTag(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetValidTagsList(t *testing.T) {
	list := GetValidTagsList()

	// Should contain all valid tags
	if list == "" {
		t.Error("GetValidTagsList() returned empty string")
	}

	// Verify it contains the known tags
	for _, tag := range ValidModelTags {
		if !contains(list, string(tag)) {
			t.Errorf("GetValidTagsList() missing %q", tag)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsString(s, substr))
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestModelTagConstants(t *testing.T) {
	// Verify tag constants have correct format
	tags := []ModelTag{UserTagSmart, UserTagDefault, UserTagFast}

	for _, tag := range tags {
		if !IsModelTag(string(tag)) {
			t.Errorf("tag constant %q doesn't start with @", tag)
		}
		if !IsValidModelTag(string(tag)) {
			t.Errorf("tag constant %q not in ValidModelTags", tag)
		}
	}
}

func TestResolveTag(t *testing.T) {
	// Create a mock available models map
	availableModels := map[ModelID]Model{
		Claude46Sonnet: {ID: Claude46Sonnet, Name: "Claude 4.6 Sonnet"},
		Claude45Sonnet: {ID: Claude45Sonnet, Name: "Claude 4.5 Sonnet"},
		Claude46Opus:   {ID: Claude46Opus, Name: "Claude 4.6 Opus"},
		GPT55:          {ID: GPT55, Name: "GPT-5.5"},
		GPT54:          {ID: GPT54, Name: "GPT-5.4"},
		GPT54Mini:      {ID: GPT54Mini, Name: "GPT-5.4 Mini"},
		GPT54Pro:       {ID: GPT54Pro, Name: "GPT-5.4 Pro"},
		GPT52:          {ID: GPT52, Name: "GPT-5.2"},
	}
	tests := []struct {
		name string
		tag  string
		want ModelID
	}{
		{"@smart resolves to opus", "@smart", Claude46Opus},
		{"@default resolves to sonnet 4.6", "@default", Claude46Sonnet},
		{"@fast falls through to GPT-5.4 Mini", "@fast", GPT54Mini},
		{"invalid tag returns empty", "@invalid", ""},
		{"non-tag returns empty", "claude-4.5-sonnet", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTag(tt.tag, availableModels)
			if got != tt.want {
				t.Errorf("ResolveTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

func TestResolveTag_PrefersGPT55WhenAnthropicDefaultsUnavailable(t *testing.T) {
	availableModels := map[ModelID]Model{
		GPT55: {ID: GPT55, Name: "GPT-5.5"},
		GPT54: {ID: GPT54, Name: "GPT-5.4"},
		GPT52: {ID: GPT52, Name: "GPT-5.2"},
	}

	got := ResolveTag("@default", availableModels)
	if got != GPT55 {
		t.Fatalf("ResolveTag(@default) = %v, want %v", got, GPT55)
	}
}

func TestResolveTagWithFallback(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want ModelID
	}{
		{"@smart fallback to opus", "@smart", Claude46Opus},
		{"@default fallback to sonnet 4.6", "@default", Claude46Sonnet},
		{"@fast fallback to haiku", "@fast", Claude45Haiku},
		{"invalid tag returns empty", "@invalid", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTagWithFallback(tt.tag)
			if got != tt.want {
				t.Errorf("ResolveTagWithFallback(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}