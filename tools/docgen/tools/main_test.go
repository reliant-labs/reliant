// Copyright (c) 2026 Reliant Labs

package main

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// The public tool reference never documents a feature-gated tool. No real
// tool is gated yet, so websearch is gated for the test.
func TestReferenceSkipsFeatureGatedTools(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)

	toolInfos, tagInfos := extractFromRegistry()
	for _, info := range toolInfos {
		if info.Name == tools.ToolWebSearch {
			t.Fatalf("the reference documents the gated tool %q", info.Name)
		}
	}
	if len(toolInfos) != len(tools.GetToolRegistry())-1 {
		t.Fatalf("documented %d tools, want every registered tool but the gated one (%d)", len(toolInfos), len(tools.GetToolRegistry())-1)
	}
	if markdown := generateMarkdown(toolInfos, tagInfos, true); strings.Contains(markdown, "### "+tools.ToolWebSearch+"\n") {
		t.Fatalf("the rendered reference has a section for the gated tool")
	}
}
