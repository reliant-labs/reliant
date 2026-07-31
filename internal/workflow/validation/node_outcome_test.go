// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The whole value of a declared outcome is that a supervisor can trust it. A
// typo must therefore be a load-time error — a node that silently stamps
// nothing puts the run straight back to reporting a false green.

func TestNodeOutcomeParsesAndValidates(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: outcome-valid
entry: [work]
nodes:
  - id: work
    type: call_llm
  - id: failed
    type: save_message
    outcome: failure
    args:
      role: assistant
      content: "did not pass"
  - id: done
    type: save_message
    outcome: success
    args:
      role: assistant
      content: "shipped"
edges:
  - from: work
    default: failed
`))
	require.NoError(t, err)

	byID := map[string]string{}
	for _, n := range wf.GetNodes() {
		byID[n.GetId()] = n.GetOutcome()
	}
	assert.Equal(t, "failure", byID["failed"], "outcome must survive the YAML parse")
	assert.Equal(t, "success", byID["done"])
	assert.Equal(t, "", byID["work"], "a node that declares nothing must stay undeclared")

	result := &Result{}
	validateStructure(wf, result)
	for _, e := range result.Errors() {
		if strings.Contains(e.Message, "outcome") {
			t.Fatalf("valid outcome rejected: %s", e.Message)
		}
	}
}

func TestNodeOutcomeRejectsUnknownValue(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: outcome-typo
entry: [work]
nodes:
  - id: work
    type: call_llm
    outcome: failed
`))
	require.NoError(t, err)

	result := &Result{}
	validateStructure(wf, result)

	found := false
	for _, e := range result.Errors() {
		if strings.Contains(e.Message, `unknown outcome "failed"`) {
			found = true
		}
	}
	assert.True(t, found, "a misspelled outcome must be rejected at load time, not silently ignored: %+v", result.Errors())
}

// TestNodeOutcomeRejectedInsideInlineWorkflow: an inline block is run by the
// inline executor, which never stamps the run's verdict. A declaration there
// would read correctly and do nothing — silently reinstating the false green.
func TestNodeOutcomeRejectedInsideInlineWorkflow(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: outcome-inline
entry: [attempt]
nodes:
  - id: attempt
    type: loop
    while: iter.iteration < 2
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
        - id: gave_up
          type: call_llm
          outcome: failure
`))
	require.NoError(t, err)

	result := &Result{}
	validateStructure(wf, result)

	found := false
	for _, e := range result.Errors() {
		if strings.Contains(e.Message, "outcome cannot be declared inside an inline workflow") {
			found = true
		}
	}
	assert.True(t, found, "an outcome inside a loop body must be rejected, not silently ignored: %+v", result.Errors())
}
