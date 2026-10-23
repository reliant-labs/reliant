// Copyright (c) 2025 Reliant Labs
package project

import (
	"strings"
	"testing"
)

func TestDefaultProjectReliantMD_PinsRoutingFacts(t *testing.T) {
	for _, want := range []string{
		"# Project Instructions",
		"## Guidelines",
		"## Key Context",
		"## What Reliant can do",
		"`workflow-builder` skill",
		"`.reliant/workflows/`",
		"`triggers:` block",
		"`http/request@1`",
		"even when it sounds like \"just a script\"",
		"`mcp__reliant-docs__search_reliant`",
		"reliant forge skill load forge",
		"https://github.com/reliant-labs/forge",
		"cron-kind workloads and operators need",
		"reliant forge obs check <env> --wait --json",
		"Report \"not proven good\"",
		"reliant forge db query <env>",
		"reliant forge skill load observability/query",
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
}
