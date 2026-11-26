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

	// A bad binding on a gated tool must not list its parameters: it is
	// reported exactly as the same binding on a tool that does not exist.
	t.Run("a bad parameter reads as an unknown tool", func(t *testing.T) {
		for _, param := range []string{"sizze", "save_to"} {
			gated := bindingErrors(t, StaticAnalysis(workflowWithToolBindings(t, map[string]map[string]any{
				"generate_image": {param: "x"},
			}), nil))
			unknown := bindingErrors(t, StaticAnalysis(workflowWithToolBindings(t, map[string]map[string]any{
				"zz_no_such_tool": {param: "x"},
			}), nil))
			if len(gated) != 1 || len(unknown) != 1 {
				t.Fatalf("%s: expected one error each, got %+v and %+v", param, gated, unknown)
			}
			swap := func(s string) string { return strings.ReplaceAll(s, "zz_no_such_tool", "generate_image") }
			if gated[0].Message != swap(unknown[0].Message) || gated[0].Field != swap(unknown[0].Field) ||
				strings.Join(gated[0].Path, ".") != strings.Join(unknown[0].Path, ".") || gated[0].Suggestion != unknown[0].Suggestion {
				t.Errorf("%s: gated %+v, want the unknown tool's %+v", param, gated[0], unknown[0])
			}
			if strings.Contains(gated[0].Message, "size") {
				t.Errorf("%s: the error lists the gated tool's parameters: %s", param, gated[0].Message)
			}
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
