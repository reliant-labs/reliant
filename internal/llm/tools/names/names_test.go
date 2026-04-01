// Copyright (c) 2025 Reliant Labs
package names

import (
	"regexp"
	"testing"
)

// LLMToolNamePattern is the regex pattern that LLM APIs (Anthropic, OpenAI, etc.)
// require tool names to match. Colons, dots, spaces, and other special characters
// are rejected by these APIs.
var LLMToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func TestAllToolNames_MatchLLMAPIPattern(t *testing.T) {
	for _, name := range AllToolNames {
		if !LLMToolNamePattern.MatchString(name) {
			t.Errorf("tool name %q does not match LLM API pattern %s", name, LLMToolNamePattern.String())
		}
	}
}

func TestAllToolNames_NonEmpty(t *testing.T) {
	if len(AllToolNames) == 0 {
		t.Fatal("AllToolNames is empty")
	}
}

func TestAllToolNames_NoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(AllToolNames))
	for _, name := range AllToolNames {
		if seen[name] {
			t.Errorf("duplicate tool name: %q", name)
		}
		seen[name] = true
	}
}

func TestInvalidToolNames_Rejected(t *testing.T) {
	// These names should NOT match the LLM API pattern.
	// The __response__: prefix bug would have been caught by this test.
	invalidNames := []string{
		"__response__:submit_verdict",
		"tool.name.with.dots",
		"tool name with spaces",
		"tool:with:colons",
		"tool/with/slashes",
		"mcp__/tmp/worktree::server__tool",
		"",
	}
	for _, name := range invalidNames {
		if LLMToolNamePattern.MatchString(name) {
			t.Errorf("expected %q to be rejected by LLM API pattern, but it matched", name)
		}
	}
}
