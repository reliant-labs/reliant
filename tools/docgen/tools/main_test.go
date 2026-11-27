// Copyright (c) 2026 Reliant Labs

package main

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// The public tool reference never documents a feature-gated tool: neither
// the shipped gated tools (GTM's) nor websearch, gated for the test.
func TestReferenceSkipsFeatureGatedTools(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)

	toolInfos, tagInfos := extractFromRegistry()
	gated := tools.FeatureGatedTools()
	for _, info := range toolInfos {
		if _, ok := gated[info.Name]; ok {
			t.Fatalf("the reference documents the gated tool %q", info.Name)
		}
	}
	if want := len(tools.GetToolRegistry()) - len(gated); len(toolInfos) != want {
		t.Fatalf("documented %d tools, want every registered tool but the %d gated ones (%d)", len(toolInfos), len(gated), want)
	}
	if markdown := generateMarkdown(toolInfos, tagInfos, true); strings.Contains(markdown, "### "+tools.ToolWebSearch+"\n") {
		t.Fatalf("the rendered reference has a section for the gated tool")
	}
}
