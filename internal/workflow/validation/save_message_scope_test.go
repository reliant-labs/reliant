// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func celErrors(t *testing.T, workflowYAML string) []string {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)
	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)
	var msgs []string
	for _, e := range result.Errors() {
		msgs = append(msgs, strings.Join(e.Path, ".")+": "+e.Message)
	}
	return msgs
}

func hasErrorContaining(msgs []string, parts ...string) bool {
	for _, m := range msgs {
		all := true
		for _, p := range parts {
			if !strings.Contains(m, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// A node's save_message is written by whoever executes the node — for an
// activity, the worker, which cannot see other nodes' outputs. So `nodes` is
// not in the save_message environment, and validation must say so by name.
func TestSaveMessage_RejectsNodesReferences(t *testing.T) {
	t.Parallel()
	msgs := celErrors(t, `
name: save-nodes
entry: [first]
nodes:
  - id: first
    type: call_llm
  - id: second
    type: call_llm
    save_message:
      role: assistant
      content: "{{nodes.first.response_text}}"
edges:
  - from: first
    default: second
`)
	require.True(t, hasErrorContaining(msgs, "save_message", "content", "`nodes`"),
		"save_message reading nodes.* must be rejected with a clear error, got: %v", msgs)
}

func TestSaveMessage_RejectsNodesInCondition(t *testing.T) {
	t.Parallel()
	msgs := celErrors(t, `
name: save-nodes-cond
entry: [first]
nodes:
  - id: first
    type: call_llm
  - id: second
    type: call_llm
    save_message:
      condition: "nodes.first.token_count > 0"
      role: assistant
      content: "{{output.response_text}}"
edges:
  - from: first
    default: second
`)
	require.True(t, hasErrorContaining(msgs, "condition", "`nodes`"), "got: %v", msgs)
}

func TestSaveMessage_OutputInputsWorkflowIterStillValid(t *testing.T) {
	t.Parallel()
	msgs := celErrors(t, `
name: save-ok
entry: [step]
inputs:
  label:
    type: string
    default: x
nodes:
  - id: step
    type: call_llm
    save_message:
      role: "{{output.message.role}}"
      content: "{{inputs.label}} {{workflow.name}} {{iter.iteration}} {{output.response_text}}"
      tool_calls: "{{output.tool_calls}}"
`)
	require.Empty(t, msgs)
}

// Rule 2: a message-only output field never reaches the workflow, so it is not
// part of the node's CEL-visible output.
func TestMessageOnlyField_NotReadableByCEL(t *testing.T) {
	t.Parallel()
	msgs := celErrors(t, `
name: thinking-ref
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
  - id: after
    type: call_llm
edges:
  - from: call_llm
    cases:
      - to: after
        condition: "nodes.call_llm.thinking.content != ''"
`)
	require.True(t, hasErrorContaining(msgs, "thinking"),
		"nodes.call_llm.thinking must fail validation, got: %v", msgs)
}
