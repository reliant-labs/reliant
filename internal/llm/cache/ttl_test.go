// Copyright (c) 2025 Reliant Labs
package cache

import "testing"

func TestSupportsOpenAIExtendedRetention(t *testing.T) {
	tests := []struct {
		apiModel string
		want     bool
	}{
		// On OpenAI's documented extended-retention list.
		{"gpt-5.5", true},
		{"gpt-5.5-pro", true},
		{"gpt-5.4", true},
		{"gpt-5.2", true},
		{"gpt-4.1", true},
		{" GPT-5.4 ", true},

		// Catalog models that are NOT on the list. Sending the field to these
		// has undocumented behavior, so they must get the provider default.
		{"gpt-5.4-pro", false},
		{"gpt-5.4-mini", false},
		{"gpt-5.2-pro", false},
		{"gpt-5.2-codex", false},
		{"gpt-5.3-codex", false},

		// GPT-5.6+ and GPT-6 replaced the field with prompt_cache_options.
		{"gpt-5.6-sol", false},
		{"gpt-6-astra", false},
		{"gpt-6-sol", false},

		// Non-OpenAI models routed through an OpenAI-compatible client.
		{"claude-opus-5-5", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := SupportsOpenAIExtendedRetention(tt.apiModel); got != tt.want {
			t.Errorf("SupportsOpenAIExtendedRetention(%q) = %v, want %v", tt.apiModel, got, tt.want)
		}
	}
}
