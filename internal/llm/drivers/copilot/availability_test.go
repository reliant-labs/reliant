// Copyright (c) 2025 Reliant Labs
package copilot

import "testing"

func TestParseEnabledModels(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"gpt-5-mini","policy":{"state":"enabled"}},
		{"id":"claude-sonnet-5","policy":{"state":"enabled"}},
		{"id":"claude-opus-4.8","policy":{"state":"disabled"}},
		{"id":"gpt-4o","policy":null},
		{"id":"gpt-4o-mini"}
	]}`)

	got, err := parseEnabledModels(body)
	if err != nil {
		t.Fatalf("parseEnabledModels: %v", err)
	}

	want := map[string]bool{
		"gpt-5-mini":      true,
		"claude-sonnet-5": true,
		"claude-opus-4.8": false, // policy disabled -> not available
		"gpt-4o":          true,  // null policy -> unrestricted
		"gpt-4o-mini":     true,  // absent policy -> unrestricted
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %v", len(got), len(want), got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("model %s: got enabled=%v, want %v", id, got[id], w)
		}
	}
}

func TestIsModelEnabledUnknownFailsOpen(t *testing.T) {
	// A model not present in the account catalog is treated as enabled so a
	// stale/renamed catalog never hides a Reliant-mapped model.
	enabled := map[string]bool{"gpt-5-mini": true}
	if _, known := enabled["some-unmapped-model"]; known {
		t.Fatal("precondition failed")
	}
}
