// Copyright (c) 2025 Reliant Labs
package llm

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewToolCallID(t *testing.T) {
	// Every constraint a minted id must meet wherever history may be sent:
	// OpenAI's 40-character cap, Anthropic's ^[a-zA-Z0-9_-]+$, and nine
	// trailing alphanumerics for vLLM's Mistral tokenizer.
	shape := regexp.MustCompile(`^call_[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for range 1000 {
		id := NewToolCallID()
		if !shape.MatchString(id) || len(id) > 40 {
			t.Fatalf("NewToolCallID() = %q", id)
		}
		if seen[id] {
			t.Fatalf("NewToolCallID() repeated %q", id)
		}
		seen[id] = true
	}
}

func TestNewToolCallIDKeepingThoughtSignature(t *testing.T) {
	for _, tc := range []struct {
		providerID    string
		wantSignature string
	}{
		{"call_0", ""},
		{"", ""},
		{"call_abc__thought__U0lHTkFUVVJF", "U0lHTkFUVVJF"},
		{"call_abc__thought__", ""},
	} {
		got := NewToolCallIDKeepingThoughtSignature(tc.providerID)
		base, signature, _ := strings.Cut(got, "__thought__")
		if !regexp.MustCompile(`^call_[0-9a-f]{32}$`).MatchString(base) {
			t.Errorf("%q -> %q: the id before any signature must be minted", tc.providerID, got)
		}
		if signature != tc.wantSignature {
			t.Errorf("%q -> %q: signature %q, want %q", tc.providerID, got, signature, tc.wantSignature)
		}
	}
}
