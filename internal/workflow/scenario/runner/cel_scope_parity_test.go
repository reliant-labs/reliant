// Copyright (c) 2025 Reliant Labs
package runner

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/require"
)

// One rule for every CEL evaluation site: an expression sees exactly the
// namespaces its wfcel context declares, and every declared namespace is
// populated. Validation derives its per-site environment from the same
// Namespaces(), so "validates" == "evaluates". Each test below is a site that
// broke the rule, run on the real DynamicWorkflow.

// savedContent returns the content of every message saved for node.
func savedContent(res *Result, node string) []string {
	var out []string
	for _, m := range res.Execution.SavedMessages {
		if m.Node == node {
			out = append(out, m.Content)
		}
	}
	return out
}

// --- inline `workflow` node outputs -----------------------------------------

// The inline workflow node's declared outputs were evaluated against a raw map
// {nodes, workflow:{inputs}}: `inputs.*` was an undeclared reference and
// `workflow.id` a missing map key, and every failure was swallowed into nil.
const inlineWorkflowOutputsYAML = `
name: inline-outputs
inputs:
  topic:
    type: string
    default: "cats"
entry: [sub]
nodes:
  - id: sub
    type: workflow
    inline:
      entry: [work]
      inputs:
        topic:
          type: string
          default: ""
      outputs:
        topic: "{{inputs.topic}}"
        wf: "{{workflow.id}}"
        text: "{{nodes.work.response_text}}"
      nodes:
        - id: work
          type: call_llm
          args:
            system_prompt: "x"
    args:
      topic: "{{inputs.topic}}"
  - id: after
    type: call_llm
    args:
      system_prompt: "topic={{nodes.sub.topic}} wf={{nodes.sub.wf}} text={{nodes.sub.text}}"
    save_message:
      role: assistant
      content: "topic={{inputs.topic}}"
edges:
  - from: sub
    default: after
`

func TestInlineWorkflowOutputs_SeeInputsAndWorkflow(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "inline_outputs",
		Events: []scenario.SimulatedEvent{
			{Node: "sub.work", Output: map[string]interface{}{"response_text": "hello"}},
			{Node: "after", Output: map[string]interface{}{"response_text": "ok"}},
		},
	}
	res := runYAMLScenario(t, inlineWorkflowOutputsYAML, sc)
	t.Logf("status=%s outcome=%s err=%s", res.Status, res.Execution.Outcome, res.Execution.Error)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)

	out := res.Execution.NodeOutputs["sub"]
	require.Equal(t, "cats", out["topic"], "inputs.* must resolve in inline workflow outputs")
	require.Equal(t, "hello", out["text"])
	wf, _ := out["wf"].(string)
	require.NotEmpty(t, wf, "workflow.id must resolve in inline workflow outputs, got %#v", out["wf"])
}

const inlineWorkflowFailingOutputYAML = `
name: inline-outputs-fail
entry: [sub]
nodes:
  - id: sub
    type: workflow
    inline:
      entry: [work]
      outputs:
        broken: "{{nodes.work.response_text + 1}}"
      nodes:
        - id: work
          type: call_llm
          args:
            system_prompt: "x"
`

// A failing output expression fails the node instead of silently yielding nil.
func TestInlineWorkflowOutputs_FailingExpressionFailsTheNode(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "inline_outputs_fail",
		Events: []scenario.SimulatedEvent{
			{Node: "sub.work", Output: map[string]interface{}{"response_text": "hello"}},
		},
	}
	res := runYAMLScenario(t, inlineWorkflowFailingOutputYAML, sc)
	t.Logf("status=%s outcome=%s err=%s outputs=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Execution.NodeOutputs["sub"])
	require.Equal(t, "error", res.Execution.Outcome,
		"a failing declared output must fail the node, not become nil")
	require.NotNil(t, res.Execution.Error)
	require.Contains(t, res.Execution.Error.Message, `failed to evaluate output "broken"`)
}

// --- save_message iter.item --------------------------------------------------

// The pitch-deck shape: a parallel items loop whose body saves
// "{{iter.item.filename}}". The body node is a sub-workflow (workflow-side save)
// in one test and an ordinary call_llm (save delegated to the activity wrapper)
// in the other; both built iter from the counter alone and dropped item/key.
const saveMessageIterItemWorkflowSideYAML = `
name: save-iter-item-wf
entry: [each]
nodes:
  - id: each
    type: loop
    parallel: true
    items: "{{[{'filename': 'a.md'}, {'filename': 'b.md'}]}}"
    key: "{{iter.item.filename}}"
    inline:
      entry: [write]
      nodes:
        - id: write
          type: workflow
          inline:
            entry: [work]
            outputs:
              response_text: "{{nodes.work.response_text}}"
            nodes:
              - id: work
                type: call_llm
                args:
                  system_prompt: "x"
          save_message:
            role: assistant
            content: "**{{iter.item.filename}}** ({{iter.key}}): {{output.response_text}}"
`

func TestSaveMessage_IterItem_WorkflowSideSave(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "save_iter_item_wf",
		Events: []scenario.SimulatedEvent{
			{Node: "each.write.work", Output: map[string]interface{}{"response_text": "done"}},
			{Node: "each.write.work", Output: map[string]interface{}{"response_text": "done"}},
		},
	}
	res := runYAMLScenario(t, saveMessageIterItemWorkflowSideYAML, sc)
	t.Logf("status=%s outcome=%s err=%s saved=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Execution.SavedMessages)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)

	got := strings.Join(savedContent(res, "each.write"), "\n")
	require.Contains(t, got, "**a.md** (a.md): done")
	require.Contains(t, got, "**b.md** (b.md): done")
}

const saveMessageIterItemDelegatedYAML = `
name: save-iter-item-delegated
entry: [each]
nodes:
  - id: each
    type: loop
    while: "true"
    items: "{{[{'filename': 'a.md'}, {'filename': 'b.md'}]}}"
    key: "{{iter.item.filename}}"
    inline:
      entry: [write]
      nodes:
        - id: write
          type: call_llm
          args:
            system_prompt: "x"
          save_message:
            role: assistant
            content: "**{{iter.item.filename}}** #{{iter.iteration}}: {{output.response_text}}"
`

func TestSaveMessage_IterItem_DelegatedSave(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "save_iter_item_delegated",
		Events: []scenario.SimulatedEvent{
			{Node: "each.write", Output: map[string]interface{}{"response_text": "one"}},
			{Node: "each.write", Output: map[string]interface{}{"response_text": "two"}},
		},
	}
	res := runYAMLScenario(t, saveMessageIterItemDelegatedYAML, sc)
	t.Logf("status=%s outcome=%s err=%s saved=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Execution.SavedMessages)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)

	got := strings.Join(savedContent(res, "each.write"), "\n")
	require.Contains(t, got, "**a.md** #0: one")
	require.Contains(t, got, "**b.md** #1: two")
}

// --- edge case conditions inside a loop body ---------------------------------

// Edge case conditions were evaluated without iter/outputs: iter.iteration was
// always 0 and outputs.x failed "no such key", although EdgeEvalContext
// declares both and the body's node conditions already received them.
const edgeConditionLoopScopeYAML = `
name: edge-loop-scope
entry: [attempt]
nodes:
  - id: attempt
    type: loop
    while: "iter.iteration < 2"
    inline:
      entry: [work]
      outputs:
        marker: "{{has(nodes.second) ? 'second' : 'first'}}"
      nodes:
        - id: work
          type: call_llm
          args:
            system_prompt: "x"
        - id: first
          type: call_llm
          args:
            system_prompt: "x"
        - id: second
          type: call_llm
          args:
            system_prompt: "x"
      edges:
        - from: work
          cases:
            - condition: "iter.iteration == 1 && outputs.marker == 'first'"
              to: [second]
          default: [first]
`

func TestEdgeCondition_SeesLoopIterAndPreviousOutputs(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "edge_loop_scope",
		Events: []scenario.SimulatedEvent{
			{Node: "attempt.work", Output: map[string]interface{}{"response_text": "w0"}},
			{Node: "attempt.first", Output: map[string]interface{}{"response_text": "f0"}},
			{Node: "attempt.work", Output: map[string]interface{}{"response_text": "w1"}},
			{Node: "attempt.second", Output: map[string]interface{}{"response_text": "s1"}},
		},
	}
	res := runYAMLScenario(t, edgeConditionLoopScopeYAML, sc)
	t.Logf("status=%s outcome=%s err=%s reached=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Execution.NodesReached)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	require.Contains(t, res.Execution.NodesReached, "attempt.second",
		"on iteration 1 the edge case (iter.iteration == 1 && outputs.marker == 'first') must route to second")
}

// --- node condition in a ref'd / sub-workflow loop body at iteration 0 --------

// The get-it-right regression: a loop body node's condition reads outputs.*,
// and at iteration 0 there is no previous iteration. `outputs` must still be
// declared (as the empty map) — "undeclared reference to 'outputs'" killed the
// run. The loop body here is itself a sub-workflow, as when a workflow refs
// builtin://get-it-right.
const nodeConditionIterationZeroYAML = `
name: cond-iter0
entry: [outer]
nodes:
  - id: outer
    type: workflow
    inline:
      entry: [attempt]
      nodes:
        - id: attempt
          type: loop
          while: "iter.iteration < 1"
          inline:
            entry: [implement]
            outputs:
              eval_strategy: "'pass'"
            nodes:
              - id: implement
                type: call_llm
                condition: "!(has(outputs.eval_strategy) && outputs.eval_strategy == 'stuck')"
                args:
                  system_prompt: "x"
`

func TestNodeCondition_OutputsDeclaredAtIterationZero(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "cond_iter0",
		Events: []scenario.SimulatedEvent{
			{Node: "outer.attempt.implement", Output: map[string]interface{}{"response_text": "ok"}},
		},
	}
	res := runYAMLScenario(t, nodeConditionIterationZeroYAML, sc)
	t.Logf("status=%s outcome=%s err=%s reached=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Execution.NodesReached)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	require.Contains(t, res.Execution.NodesReached, "outer.attempt.implement")
}

// --- sub-workflow body inside a loop: conditions see the loop's iter ----------

const subWorkflowConditionIterYAML = `
name: subwf-cond-iter
entry: [each]
nodes:
  - id: each
    type: loop
    while: "true"
    items: "{{['a', 'b']}}"
    inline:
      entry: [sub]
      nodes:
        - id: sub
          type: workflow
          inline:
            entry: [only_b]
            nodes:
              - id: only_b
                type: call_llm
                condition: "iter.item == 'b'"
                args:
                  system_prompt: "x"
`

// A sub-workflow body inside a loop resolved node CONFIG against the loop's
// iter but evaluated node CONDITIONS against no scope at all (iteration 0, no
// item) — so a condition and the template beside it disagreed.
func TestSubWorkflowBodyInLoop_ConditionSeesIterItem(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "subwf_cond_iter",
		Events: []scenario.SimulatedEvent{
			{Node: "each.sub.only_b", Output: map[string]interface{}{"response_text": "b ran"}},
		},
	}
	res := runYAMLScenario(t, subWorkflowConditionIterYAML, sc)
	t.Logf("status=%s outcome=%s err=%s completed=%v mismatches=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Execution.NodesCompleted, res.Mismatches)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	executed := 0
	for _, id := range res.Execution.NodesCompleted {
		if id == "each.sub.only_b" {
			executed++
		}
	}
	require.Equal(t, 1, executed, "only_b must run exactly once — on the iteration whose item is 'b'")
}

// --- loop items / key see workflow and the enclosing iter --------------------

const loopItemsKeyScopeYAML = `
name: items-key-scope
entry: [outer]
nodes:
  - id: outer
    type: loop
    while: "true"
    items: "{{[workflow.id]}}"
    inline:
      entry: [inner]
      nodes:
        - id: inner
          type: loop
          parallel: true
          items: "{{[iter.item + '-x']}}"
          key: "{{workflow.id + ':' + iter.item}}"
          inline:
            entry: [work]
            nodes:
              - id: work
                type: call_llm
                args:
                  system_prompt: "{{iter.key}}"
`

func TestLoopItemsAndKey_SeeWorkflowAndEnclosingIter(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "items_key_scope",
		Events: []scenario.SimulatedEvent{
			{Node: "outer.inner.work", Output: map[string]interface{}{"response_text": "ok"}},
		},
	}
	res := runYAMLScenario(t, loopItemsKeyScopeYAML, sc)
	t.Logf("status=%s outcome=%s err=%s outputs=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Execution.NodeOutputs["outer.inner"])
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	require.Contains(t, res.Execution.NodesReached, "outer.inner.work")
}

// --- loop while sees workflow ------------------------------------------------

// `workflow` is available at every site; the while condition's context lacked
// it, so `workflow.id` in a while was an undeclared reference.
const whileWorkflowYAML = `
name: while-workflow
entry: [attempt]
nodes:
  - id: attempt
    type: loop
    while: "workflow.id != '' && iter.iteration < 2"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args:
            system_prompt: "x"
`

func TestLoopWhile_SeesWorkflow(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "while_workflow",
		Events: []scenario.SimulatedEvent{
			{Node: "attempt.work", Output: map[string]interface{}{"response_text": "a"}},
			{Node: "attempt.work", Output: map[string]interface{}{"response_text": "b"}},
		},
	}
	res := runYAMLScenario(t, whileWorkflowYAML, sc)
	t.Logf("status=%s outcome=%s err=%+v mismatches=%v", res.Status, res.Execution.Outcome, res.Execution.Error, res.Mismatches)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	require.Equal(t, scenario.StatusPassed, res.Status, "%v", res.Mismatches)
}
