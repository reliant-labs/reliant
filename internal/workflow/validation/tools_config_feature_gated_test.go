// Copyright (c) 2026 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools/toolcatalog"
)

// A feature-gated tool still validates by its exact name, so an entitled
// owner's workflow stays valid, but it is never suggested: to anyone else it
// does not exist. No real tool is gated yet, so generate_image is gated for
// the test (toolcatalog.GateForTest swaps process-wide state: not parallel).
func TestToolsConfigFeatureGatedTool(t *testing.T) {
	toolcatalog.GateForTest(t, "generate_image", "gtm")

	t.Run("the exact name validates", func(t *testing.T) {
		wf := workflowWithToolBindings(t, map[string]map[string]any{
			"generate_image": {"size": "1536x1024"},
		})
		if errs := bindingErrors(t, StaticAnalysis(wf, nil)); len(errs) != 0 {
			t.Fatalf("a gated tool's exact name must validate, got: %+v", errs)
		}
	})

	t.Run("a near miss does not suggest it", func(t *testing.T) {
		wf := workflowWithToolBindings(t, map[string]map[string]any{
			"generate_imagee": {"size": "1536x1024"},
		})
		errs := bindingErrors(t, StaticAnalysis(wf, nil))
		if len(errs) != 1 {
			t.Fatalf("expected exactly 1 tool-binding error, got %d: %+v", len(errs), errs)
		}
		if strings.Contains(errs[0].Suggestion, "generate_image") {
			t.Errorf("the suggestion names a gated tool: %q", errs[0].Suggestion)
		}
		if didYouMeanTool("generate_imag") != "" {
			t.Errorf("didYouMeanTool suggested a gated tool: %q", didYouMeanTool("generate_imag"))
		}
	})
}
