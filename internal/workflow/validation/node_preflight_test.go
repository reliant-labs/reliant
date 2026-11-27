// Copyright (c) 2026 Reliant Labs
package validation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func preflightWorkflow(node string) string {
	return fmt.Sprintf(`
name: pf
entry: [n]
nodes:
%s
`, node)
}

// preflight is valid on a node that runs on a machine, and an error on one
// that does not.
func TestPreflightValidation_NodeKinds(t *testing.T) {
	for name, node := range map[string]string{
		"run": `  - id: n
    type: run
    command: make
    preflight: true`,
		"run with overrides": `  - id: n
    type: run
    command: make
    preflight: {tools: [go], min_free_disk: 10GiB, max_load: 4, allow_root: true, max_machines: 2}`,
		"workflow": `  - id: n
    type: workflow
    preflight: true
    inline:
      name: body
      entry: [b]
      nodes:
        - {id: b, type: run, command: make}`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, messages(computeFindings(t, preflightWorkflow(node))))
		})
	}

	for name, node := range map[string]string{
		"save_message": `  - id: n
    type: save_message
    preflight: true
    args: {role: assistant, content: hi}`,
		"compute": `  - id: n
    type: compute
    preflight: true
    outputs: {x: "{{ 1 }}"}`,
		"server action": `  - id: n
    type: action
    uses: reliant/pr.next@1
    preflight: true`,
	} {
		t.Run("refused on "+name, func(t *testing.T) {
			errs := computeFindings(t, preflightWorkflow(node))
			assert.True(t, hasFinding(errs, "preflight checks the machine a node runs on"), messages(errs))
		})
	}
}

// Unset is nothing: a node without preflight carries none.
func TestPreflightValidation_UnsetIsNothing(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(preflightWorkflow(`  - id: n
    type: run
    command: make
    preflight: false`)))
	require.NoError(t, err)
	assert.Nil(t, wf.GetNodes()[0].GetPreflight())
}

// A definition that did not come through the parser (stored JSON, the
// builder) is held to the same values.
func TestPreflightValidation_Values(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(preflightWorkflow(`  - id: n
    type: run
    command: make`)))
	require.NoError(t, err)
	for _, p := range []*reliantv1.NodePreflight{
		{MinFreeDisk: "plenty"}, {MaxMachines: 99}, {MaxLoad: -2}, {Tools: []string{"bin/go"}},
	} {
		wf.GetNodes()[0].Preflight = p
		errs := StaticAnalysis(wf, nil).Errors()
		assert.True(t, hasFinding(errs, "preflight"), "%v: %v", p, messages(errs))
	}
}
