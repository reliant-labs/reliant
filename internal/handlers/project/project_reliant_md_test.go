// Copyright (c) 2025 Reliant Labs
package project

import (
	"strings"
	"testing"
)

func TestDefaultProjectReliantMD_Shape(t *testing.T) {
	for _, want := range []string{
		"# Project Instructions",
		"## Guidelines",
		"## Key Context",
		"## Code navigation",
		"`code_context(symbol: \"X\")`",
		"Load the `code-search` skill for the details.",
		"## Available Skills",
		"`workflow-builder`: Build agents and automations that run on a schedule or an event",
	} {
		if !strings.Contains(defaultProjectReliantMD, want) {
			t.Errorf("scaffolded reliant.md is missing %q", want)
		}
	}
	if strings.Contains(defaultProjectReliantMD, "Build and test Reliant workflows") {
		t.Error("scaffolded reliant.md still describes workflow-builder as a feature name, not a trigger")
	}
	// The routing guide is rendered live by the harness on every turn; a copy
	// written to disk once goes stale, and the harness drops any copy it finds.
	for _, stale := range []string{"## What Reliant can do", "### forge", "https://github.com/reliant-labs/forge"} {
		if strings.Contains(defaultProjectReliantMD, stale) {
			t.Errorf("scaffolded reliant.md must not carry the routing guide (found %q); it lives in the harness", stale)
		}
	}
}
