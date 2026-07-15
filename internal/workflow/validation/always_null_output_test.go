// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// TestAlwaysNullOutputWarning verifies that output expressions whose static CEL
// type is exactly null_type (always null) are flagged with a warning, while
// conditionally-null expressions (ternaries with a null branch) are NOT flagged.
//
// Background: a loop output that evaluated to CEL null used to crash the loop at
// runtime ("proto: invalid type: structpb.NullValue"). The runtime now normalizes
// CEL null to Go nil (representable as structpb NullValue), so null outputs are
// legal. Validation flags only the statically-provable always-null case, which
// is almost certainly an authoring mistake.
func TestAlwaysNullOutputWarning(t *testing.T) {
	findAlwaysNullWarnings := func(result *Result) []*Error {
		var found []*Error
		for _, w := range result.Warnings() {
			if strings.Contains(w.Message, "always null") {
				found = append(found, w)
			}
		}
		return found
	}

	t.Run("top-level output with null literal is flagged", func(t *testing.T) {
		workflowYAML := `
name: test-null-output
entry: [step]
nodes:
  - id: step
    type: call_llm
    model:
      tags: [flagship]
outputs:
  broken: "{{null}}"
  fine: "{{nodes.step.response_text}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		warnings := findAlwaysNullWarnings(result)
		require.Len(t, warnings, 1, "exactly the 'broken' output should be flagged, got: %v", result.All())
		assert.Contains(t, pathToString(warnings[0].Path), "broken")
	})

	t.Run("loop inline output with null literal is flagged", func(t *testing.T) {
		workflowYAML := `
name: test-null-loop-output
entry: [loop]
nodes:
  - id: loop
    type: loop
    while: "iter.iteration < 3"
    inline:
      name: loop-iteration
      entry: [step]
      nodes:
        - id: step
          type: call_llm
          model:
            tags: [flagship]
      outputs:
        broken: "{{null}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		warnings := findAlwaysNullWarnings(result)
		require.Len(t, warnings, 1, "the inline loop 'broken' output should be flagged, got: %v", result.All())
		assert.Contains(t, pathToString(warnings[0].Path), "broken")
	})

	t.Run("conditionally-null ternary is NOT flagged", func(t *testing.T) {
		// Mirrors the structured-agent pattern: an output that is null in some
		// iterations is legal and representable at runtime (structpb NullValue).
		workflowYAML := `
name: test-conditional-null
entry: [loop]
nodes:
  - id: loop
    type: loop
    while: "outputs.completed != true"
    inline:
      name: loop-iteration
      entry: [step]
      nodes:
        - id: step
          type: call_llm
          model:
            tags: [flagship]
      outputs:
        response: "{{has(nodes.step) && nodes.step.response_text != '' ? nodes.step.response_text : null}}"
        completed: "{{has(nodes.step) && nodes.step.response_text != ''}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		warnings := findAlwaysNullWarnings(result)
		assert.Empty(t, warnings, "conditionally-null outputs must not be flagged: %v", warnings)
	})

	t.Run("normal outputs are not flagged", func(t *testing.T) {
		workflowYAML := `
name: test-normal-outputs
entry: [step]
nodes:
  - id: step
    type: call_llm
    model:
      tags: [flagship]
outputs:
  text: "{{nodes.step.response_text}}"
  count: "{{nodes.step.token_count}}"
`
		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		warnings := findAlwaysNullWarnings(result)
		assert.Empty(t, warnings, "normal outputs must not be flagged: %v", warnings)
	})
}
