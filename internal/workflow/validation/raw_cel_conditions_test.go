// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every condition-like field is raw CEL, never a template: node condition,
// edge case condition, switch case condition, loop while, and save_message
// condition. A value wrapped in {{ }} is rejected uniformly — nothing strips
// the delimiters — with the unwrapped expression as the fix.

func TestRawCEL_TemplateDelimitersRejected(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		yaml string
		path string
	}{
		"node condition": {path: "nodes.[1](second).condition", yaml: `
name: t
entry: [first]
inputs:
  enabled: {type: boolean, default: true}
nodes:
  - id: first
    type: call_llm
  - id: second
    type: call_llm
    condition: "{{inputs.enabled}}"
edges:
  - from: first
    default: second
`},
		"edge case condition": {path: "edges.[0].cases.[0].condition", yaml: `
name: t
entry: [first]
nodes:
  - id: first
    type: call_llm
  - id: second
    type: call_llm
edges:
  - from: first
    cases:
      - condition: "{{nodes.first.token_count > 0}}"
        to: second
`},
		"loop while": {path: "nodes.[0](loop).while", yaml: `
name: t
entry: [loop]
nodes:
  - id: loop
    type: loop
    while: "{{iter.iteration < 3}}"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
`},
		"switch case condition": {path: "ui.switches.sw.cases.[0].condition", yaml: `
name: t
entry: [first]
nodes:
  - id: first
    type: call_llm
ui:
  switches:
    sw:
      source_node: first
      cases:
        - id: c1
          condition: "{{nodes.first.token_count > 0}}"
`},
		"save_message condition": {path: "nodes.[0](run).save_message.condition", yaml: `
name: t
entry: [run]
nodes:
  - id: run
    type: run
    command: "true"
    save_message:
      condition: "{{output.exit_code != 0}}"
      role: assistant
      content: "failed"
`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msgs := celErrors(t, tc.yaml)
			require.True(t, hasErrorContaining(msgs, tc.path, "conditions are raw CEL; remove the {{ }}"),
				"expected a raw-CEL delimiter error at %s, got: %v", tc.path, msgs)
		})
	}
}

// The error names the unwrapped expression, so the fix is copy-paste.
func TestRawCEL_TemplateDelimiterErrorSuggestsUnwrapped(t *testing.T) {
	t.Parallel()
	msgs := celErrors(t, `
name: t
entry: [run]
nodes:
  - id: run
    type: run
    command: "true"
    save_message:
      condition: "{{output.exit_code != 0}}"
      role: assistant
      content: "failed"
`)
	require.True(t, hasErrorContaining(msgs, "remove the {{ }}: output.exit_code != 0"), "got: %v", msgs)
}

func TestSaveMessageCondition_MustReturnBool(t *testing.T) {
	t.Parallel()
	msgs := celErrors(t, `
name: t
entry: [step]
nodes:
  - id: step
    type: call_llm
    save_message:
      condition: "output.response_text"
      role: assistant
      content: "{{output.response_text}}"
`)
	require.True(t, hasErrorContaining(msgs, "save_message.condition", "condition must return bool"), "got: %v", msgs)
}

// Compiled in the save_message environment: output/inputs/workflow/iter are
// visible, a typo is caught.
func TestSaveMessageCondition_CompiledAsRawCEL(t *testing.T) {
	t.Parallel()
	valid := celErrors(t, `
name: t
entry: [run]
inputs:
  bridge: {type: string, default: summary}
nodes:
  - id: run
    type: run
    command: "true"
    save_message:
      condition: "output.exit_code != 0 && inputs.bridge != 'none' && iter.iteration >= 0 && workflow.name != ''"
      role: assistant
      content: "failed"
`)
	require.Empty(t, valid)

	typo := celErrors(t, `
name: t
entry: [run]
nodes:
  - id: run
    type: run
    command: "true"
    save_message:
      condition: "outptu.exit_code != 0"
      role: assistant
      content: "failed"
`)
	require.True(t, hasErrorContaining(typo, "save_message.condition", "CEL compilation error"), "got: %v", typo)
}
