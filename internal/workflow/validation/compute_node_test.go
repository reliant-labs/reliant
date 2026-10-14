// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func computeFindings(t *testing.T, src string) []*Error {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	return StaticAnalysis(wf, nil).Errors()
}

func hasFinding(errs []*Error, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Message, substr) || strings.Contains(strings.Join(e.Path, "."), substr) {
			return true
		}
	}
	return false
}

const computeValidBase = `
name: cv
inputs:
  adds: {type: integer, default: 1}
entry: [c]
nodes:
  - id: c
    type: compute
    outputs:
      total: "{{ inputs.adds + 1 }}"
      label: "n={{ inputs.adds }}"
  - id: use
    type: save_message
    args:
      role: assistant
      content: "%s"
edges:
  - from: c
    default: use
`

func TestComputeValidation_ValidWorkflowIsClean(t *testing.T) {
	src := strings.Replace(computeValidBase, "%s", "{{ nodes.c.total }} {{ nodes.c.label }}", 1)
	assert.Empty(t, computeFindings(t, src))
}

func TestComputeValidation_DownstreamReferenceToAnUndeclaredOutput(t *testing.T) {
	src := strings.Replace(computeValidBase, "%s", "{{ nodes.c.totl }}", 1)
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "totl"), "a typo in an output name is caught at validate time: %v", messages(errs))
}

func TestComputeValidation_ExpressionsAreTypeChecked(t *testing.T) {
	src := `
name: cv2
entry: [c]
nodes:
  - id: c
    type: compute
    outputs:
      a: "{{ inputs.nope }}"
      b: "{{ 1 + }}"
      c: "unclosed {{ 1 "
`
	errs := computeFindings(t, src)
	assert.True(t, hasFinding(errs, "outputs.a"), "undeclared input at its output path: %v", messages(errs))
	assert.True(t, hasFinding(errs, "outputs.b"), "syntax error at its output path: %v", messages(errs))
	assert.True(t, hasFinding(errs, "outputs.c"), "unclosed template at its output path: %v", messages(errs))
}

func TestComputeValidation_NeedsAtLeastOneOutput(t *testing.T) {
	errs := computeFindings(t, `
name: cv3
entry: [c]
nodes:
  - id: c
    type: compute
`)
	assert.True(t, hasFinding(errs, "declares no outputs"), "%v", messages(errs))
}

func TestComputeValidation_OutputNamesFollowTheOutputRules(t *testing.T) {
	errs := computeFindings(t, `
name: cv4
entry: [c]
nodes:
  - id: c
    type: compute
    outputs:
      _hidden: 1
      "bad-name": 2
`)
	assert.True(t, hasFinding(errs, "_hidden"), "%v", messages(errs))
	assert.True(t, hasFinding(errs, "bad-name"), "%v", messages(errs))
}

func TestComputeValidation_ReadingANodeThatRunsLaterIsCaught(t *testing.T) {
	errs := computeFindings(t, `
name: cv5
entry: [c]
nodes:
  - id: c
    type: compute
    outputs:
      x: "{{ nodes.later.stdout }}"
  - id: later
    type: run
    args:
      command: "echo hi"
edges:
  - from: c
    default: later
`)
	assert.NotEmpty(t, errs, "a compute node reads only what ran before it")
}
