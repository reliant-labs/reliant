// Copyright (c) 2025 Reliant Labs
package runner

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A child run through project:// renders the templates behind its YAML aliases
// and merge keys, exactly as if their text were written in place. The child is
// parsed once and run as loaded, with no marshal round trip to hide an alias:
// the path on which control-plane pr-review's `command: *disarm` ran
// `n='{{ inputs.pr_number }}'` verbatim under pr-review-priority.
func TestRefChildRendersTemplatesBehindAliases(t *testing.T) {
	const child = `
name: child
inputs:
  pr_number: {type: string}
entry: [probe]
nodes:
  # A step the scenario mocks, which is what opens the ref (runs its body).
  - id: probe
    type: run
    args: {command: check}
  - id: first
    type: save_message
    args: &say
      role: assistant
      content: &text "pr {{ inputs.pr_number }}"
  - id: aliased
    type: save_message
    args: {role: assistant, content: *text}
  - id: merged
    type: save_message
    args:
      <<: *say
      content: "merged {{ inputs.pr_number }}"
edges:
  - from: probe
    default: first
  - from: first
    default: aliased
  - from: aliased
    default: merged
`
	const parent = `
name: parent
entry: [review]
nodes:
  - id: review
    type: workflow
    ref: project://child
    args:
      pr_number: "{{ '42' }}"
`
	p, err := wfyaml.ParseWorkflow([]byte(parent))
	require.NoError(t, err)
	c, err := wfyaml.ParseWorkflow([]byte(child))
	require.NoError(t, err)
	r := New(p, Options{Loader: func(ref string) (*reliantv1.Workflow, error) {
		if ref == "project://child" || ref == "child" {
			return c, nil
		}
		return nil, fmt.Errorf("no workflow %q", ref)
	}})
	res := r.Run(&scenario.Scenario{Name: "run", Events: []scenario.SimulatedEvent{
		{Node: "review.probe", Output: map[string]interface{}{"exit_code": 0, "stdout": "", "stderr": ""}},
	}})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	require.Equal(t, []string{"pr 42", "pr 42", "merged 42"}, savedTexts(res))
}
