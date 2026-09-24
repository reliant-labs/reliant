// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// End-to-end proof through the REAL DynamicWorkflow, not a direct call to the
// output evaluator.
//
// The unit tests next door show that an absent node field resolves to its typed
// zero. What they cannot show is that a LOOP survives it: an output evaluation
// error is fatal to the iteration (loop_executor.go wraps it in "failed to
// evaluate sub-workflow outputs"), and the while condition then runs against
// whatever the outputs map holds. Those two steps are where an author's missing
// has() guard actually bites, so they are exercised here with the loop running
// underneath a Temporal test environment.
//
// The workflow below is deliberately written the way the brief says an author
// should be able to write it — bare `{{nodes.<id>.<field>}}` with no ternaries —
// and its while condition consumes the substituted values via size(), which is
// the operation that errors on a null.
//
// What this proves: `tool_calls` is written WITHOUT a has() guard and the loop
// survives. That reference is a container, which is the only kind the fallback
// substitutes.
//
// ask_question is edge-guarded to run only when call_llm returns no tool calls,
// so on a tool-calling iteration the key `ask_question` is absent from node
// outputs entirely — nothing normalizes it, because normalization happens
// per-activity as a node COMPLETES and this one never did. Its outputs KEEP
// their has() guards here, deliberately: ask_question exposes only scalars
// (response string, has_feedback bool), and scalars are never substituted
// because a substituted "" or false reads as a real answer. See
// substitutableZero in loop_output_schema.go for why that line is drawn.
const unguardedLoopWorkflow = `
name: unguarded-loop-outputs
description: Loop outputs written without has() guards

inputs:
  max_turns:
    type: number
    default: 3

entry: [agent_loop]

nodes:
  - id: agent_loop
    type: loop
    while: "size(outputs.tool_calls) > 0 && iter.iteration < inputs.max_turns"
    inline:
      name: agent_loop_body
      entry: [call_llm]
      edges:
        - from: call_llm
          cases:
            - to: ask_question
              condition: "size(nodes.call_llm.tool_calls) == 0"
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
        response_text: "{{nodes.call_llm.response_text}}"
        pending_inbox: "{{nodes.call_llm.pending_inbox}}"
        aborted: "{{nodes.call_llm.aborted}}"
        # tool_calls above is the UNGUARDED container reference this test exists
        # for. ask_question's outputs below stay guarded on purpose: it has only
        # scalar fields (response, has_feedback), and scalars are deliberately
        # never substituted — see substitutableZero in loop_output_schema.go.
        has_feedback: "{{has(nodes.ask_question) && has(nodes.ask_question.has_feedback) ? nodes.ask_question.has_feedback : false}}"
        feedback: "{{has(nodes.ask_question) && has(nodes.ask_question.response) ? nodes.ask_question.response : ''}}"
      nodes:
        - id: call_llm
          type: call_llm
          model: claude-sonnet-4-20250514
          messages:
            - role: user
              content: "test"
        - id: ask_question
          type: ask_question
          args:
            metadata: '{"type":"ask_user","questions":[{"question":"anything else?"}]}'
`

// call_llm returns no tool calls, so the guarded edge fires and ask_question
// DOES run here — this is the control case, where every referenced node exists
// and nothing needs substituting.
const unguardedLoopScenario = `
name: unguarded_loop_exits_cleanly
description: A loop whose outputs carry no has() guards must evaluate and exit
events:
  - node: agent_loop.call_llm
    output:
      response_text: "done"
      tool_calls: []
  - node: agent_loop.ask_question
    output:
      has_feedback: false
      response: ""
expect:
  outcome: completed
`

func TestUnguardedLoopOutputs_RunEndToEndThroughDynamicWorkflow(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(unguardedLoopWorkflow))
	require.NoError(t, err, "the unguarded workflow must parse")

	scenarios, err := scenario.ParseScenarioYAML([]byte(unguardedLoopScenario))
	require.NoError(t, err)
	require.Len(t, scenarios, 1)

	res := NewRunner(wf).Run(scenarios[0])

	t.Logf("status=%s outcome=%s duration=%dms",
		res.Status, res.Execution.Outcome, res.Execution.DurationMs)
	t.Logf("reached=%v completed=%v", res.Execution.NodesReached, res.Execution.NodesCompleted)
	if res.Execution.Error != nil {
		t.Logf("error=%s", res.Execution.Error.Message)
	}
	for _, mismatch := range res.Mismatches {
		t.Logf("mismatch: %s", mismatch)
	}

	// The specific failure this change removes. Before it, output evaluation
	// failed on pending_inbox and took the iteration with it.
	if res.Execution.Error != nil {
		assert.NotContains(t, res.Execution.Error.Message, "failed to evaluate sub-workflow outputs",
			"unguarded loop outputs must no longer fail the iteration")
		assert.NotContains(t, res.Execution.Error.Message, "no such key",
			"an absent node field must resolve to its typed zero")
	}

	assert.Equal(t, scenario.StatusPassed, res.Status,
		"the unguarded loop must run to completion (mismatches=%v)", res.Mismatches)

	// The loop must EXIT, not spin. tool_calls is [] so the while condition is
	// false after the first iteration; a runaway would show as many call_llm
	// completions rather than one.
	callLLMRuns := 0
	for _, node := range res.Execution.NodesCompleted {
		if node == "agent_loop.call_llm" {
			callLLMRuns++
		}
	}
	assert.Equal(t, 1, callLLMRuns,
		"the loop must exit after one iteration, not spin (completed=%v)",
		res.Execution.NodesCompleted)
}

// The same workflow with a populated tool_calls must still iterate, proving the
// substitution did not flatten a real value into an exit condition.
//
// On the FIRST iteration tool_calls is non-empty, so the edge guard keeps
// ask_question from running and both of its outputs resolve through the
// fallback — the iteration that must survive for a second one to happen at all.
const unguardedLoopIteratingScenario = `
name: unguarded_loop_iterates_then_exits
description: tool_calls present on the first turn keeps the loop going
events:
  - node: agent_loop.call_llm
    output:
      response_text: "calling a tool"
      tool_calls:
        - id: tc-1
          name: view
          arguments: "{}"
  - node: agent_loop.call_llm
    output:
      response_text: "done"
      tool_calls: []
  - node: agent_loop.ask_question
    output:
      has_feedback: false
      response: ""
expect:
  outcome: completed
`

func TestUnguardedLoopOutputs_PopulatedValueStillDrivesIteration(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(unguardedLoopWorkflow))
	require.NoError(t, err)

	scenarios, err := scenario.ParseScenarioYAML([]byte(unguardedLoopIteratingScenario))
	require.NoError(t, err)
	require.Len(t, scenarios, 1)

	res := NewRunner(wf).Run(scenarios[0])

	t.Logf("status=%s outcome=%s completed=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesCompleted)
	if res.Execution.Error != nil {
		t.Logf("error=%s", res.Execution.Error.Message)
	}

	callLLMRuns := 0
	for _, node := range res.Execution.NodesCompleted {
		if node == "agent_loop.call_llm" {
			callLLMRuns++
		}
	}
	assert.Equal(t, 2, callLLMRuns,
		"a populated tool_calls must keep the loop iterating, then the empty one exits it "+
			"(completed=%v)", res.Execution.NodesCompleted)
}
