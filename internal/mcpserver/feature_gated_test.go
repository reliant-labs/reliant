// Copyright (c) 2026 Reliant Labs
package mcpserver

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// No feature-gated tool is served by the connector catalog: it lists the
// same tools to every connected client, so it has no entitlement to apply.
func TestCatalogServesNoFeatureGatedTool(t *testing.T) {
	served := map[string]bool{}
	for _, name := range AllToolNames() {
		served[name] = true
	}
	for _, tool := range ForgeTools() {
		served[tool.Name] = true
	}
	for name := range tools.FeatureGatedTools() {
		if served[name] {
			t.Errorf("the connector catalog serves the feature-gated tool %q", name)
		}
	}
}
