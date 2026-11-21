// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func invokeToolWorkflow(tool string) string {
	return fmt.Sprintf(`
name: it
inputs:
  tool: {type: string, default: get_run}
entry: [call]
nodes:
  - id: call
    type: invoke_tool
    tool: %s
    params: {run_id: "r-1"}
`, tool)
}

// Every exposed tool may be a node, and a templated tool is the runtime's to
// check.
func TestInvokeToolValidation_ExposedToolsAreClean(t *testing.T) {
	for _, tool := range []string{"generate_image", "get_run", "send_to_run", "report_bug", `"{{ inputs.tool }}"`} {
		t.Run(tool, func(t *testing.T) {
			errs := computeFindings(t, invokeToolWorkflow(tool))
			assert.False(t, hasFinding(errs, "cannot run as an invoke_tool node"), "unexpected finding: %v", messages(errs))
		})
	}
}

// A tool that did not opt in is refused at save time, naming what may be
// written instead: the runtime would refuse it on every attempt.
func TestInvokeToolValidation_UnexposedToolIsRefused(t *testing.T) {
	for _, tool := range []string{"shell_output", "start_run", "no_such_tool"} {
		t.Run(tool, func(t *testing.T) {
			errs := computeFindings(t, invokeToolWorkflow(tool))
			assert.True(t, hasFinding(errs, fmt.Sprintf("tool %q cannot run as an invoke_tool node", tool)), "want a refusal: %v", messages(errs))
			assert.True(t, hasFinding(errs, "report_bug"), "the refusal names the exposed tools: %v", messages(errs))
		})
	}
}
