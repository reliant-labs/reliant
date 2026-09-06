// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

// An `approval` node must be dispatched inline via the signal-based wait in
// approval_flow.go. Any node that instead reaches StepExecutor is failed there
// as a bug (step_executor.go:318), Temporal retries it to exhaustion, and the
// run pauses — which is what these tests originally caught.
//
// Both shapes were broken: InlineLoopExecutor had no approval branch, and
// neither did DynamicWorkflow's own top-level dispatch. Only
// InlineWorkflowExecutor handled it. All three now call the same shared flow,
// so an approval node behaves identically at the top level, in a sub-workflow
// body, and in a loop body.
//
// These tests are the reason the Temporal backend exists: the fast simulator
// mocks `approval` as an ordinary activity and reports both shapes green, so
// neither defect was visible from a scenario before.
//
// approvalTopLevelYAML: approval at the graph's top level.
const approvalTopLevelYAML = `
name: approval-toplevel
entry: [start]
nodes:
  - id: start
    type: call_llm
  - id: gate
    type: approval
edges:
  - from: start
    default: gate
`

// approvalInLoopYAML: the identical approval node, one level down in a loop body.
const approvalInLoopYAML = `
name: approval-in-loop
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: iter.iteration < 1
    inline:
      entry: [start]
      nodes:
        - id: start
          type: call_llm
        - id: gate
          type: approval
      edges:
        - from: start
          default: gate
`

// approvalInParallelLoopYAML: an approval node in a PARALLEL loop body. Each
// iteration runs in its own workflow.Go goroutine, so this is where a shared
// approval identity would actually collide — iteration 0's signal satisfying
// iteration 1's gate. Each iteration schedules its own ApprovalCreate and gets
// its own Temporal activity id, hence its own approval row and signal channel.
const approvalInParallelLoopYAML = `
name: approval-in-parallel-loop
entry: [fanout]
nodes:
  - id: fanout
    type: loop
    parallel: true
    items: "{{[1, 2, 3].map(n, {'num': n})}}"
    key: "{{string(iter.item.num)}}"
    inline:
      entry: [start]
      nodes:
        - id: start
          type: call_llm
        - id: gate
          type: approval
      edges:
        - from: start
          default: gate
`

func runYAMLScenario(t *testing.T, yamlStr string, sc *simulator.Scenario) *Result {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(yamlStr))
	require.NoError(t, err)
	return NewRunner(wf).Run(sc)
}

// TestApproval_AtTopLevel_IsReached: a top-level approval node is dispatched
// inline, resolves from the scenario's event, and the run completes.
func TestApproval_AtTopLevel_IsReached(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "approval_toplevel",
		Events: []simulator.SimulatedEvent{
			{Node: "start", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "gate", Output: map[string]interface{}{"status": "approved"}},
		},
	}
	res := runYAMLScenario(t, approvalTopLevelYAML, sc)
	t.Logf("top-level: status=%s outcome=%s reached=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached)

	require.Contains(t, res.Execution.NodesReached, "gate",
		"a top-level approval node is handled inline and must be entered")
	require.Equal(t, "completed", res.Execution.Outcome,
		"the approval resolves from the scenario event; the run must not fail or stall")
}

// TestApproval_InsideLoopBody_IsHandledInline: the SAME node one level down
// runs the same way. This is the regression guard for the original defect — if
// InlineLoopExecutor's approval branch is removed, `agent_loop.gate` stops
// being reached, the step falls through to StepExecutor's
// "should be handled inline" failure, and this test goes red.
func TestApproval_InsideLoopBody_IsHandledInline(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "approval_in_loop",
		Events: []simulator.SimulatedEvent{
			{Node: "agent_loop.start", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "agent_loop.gate", Output: map[string]interface{}{"status": "approved"}},
		},
	}
	res := runYAMLScenario(t, approvalInLoopYAML, sc)
	t.Logf("in-loop: status=%s outcome=%s reached=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached)

	require.Contains(t, res.Execution.NodesReached, "agent_loop.start",
		"the loop body itself must run — otherwise this test proves nothing about approval")
	require.Contains(t, res.Execution.NodesReached, "agent_loop.gate",
		"approval inside a loop body must be dispatched inline, as it is one level up")
	require.Equal(t, "completed", res.Execution.Outcome,
		"the run must complete rather than failing the gate as a StepExecutor defect")
}

// TestApproval_InsideParallelLoopBody_IsHandledInline: parallel loop bodies
// dispatch approval inline too, and every concurrent iteration gets its own
// approval rather than one iteration's resolution satisfying the others.
//
// Three iterations run at once; all three must reach the gate and the run must
// complete. If loop_executor_parallel.go's approval branch is removed, each
// iteration's gate falls through to StepExecutor and fails as a defect.
func TestApproval_InsideParallelLoopBody_IsHandledInline(t *testing.T) {
	sc := &simulator.Scenario{
		Name: "approval_in_parallel_loop",
		Events: []simulator.SimulatedEvent{
			{Node: "fanout.start", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "fanout.start", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "fanout.start", Output: map[string]interface{}{"response_text": "ok"}},
			{Node: "fanout.gate", Output: map[string]interface{}{"status": "approved"}},
			{Node: "fanout.gate", Output: map[string]interface{}{"status": "approved"}},
			{Node: "fanout.gate", Output: map[string]interface{}{"status": "approved"}},
		},
	}
	res := runYAMLScenario(t, approvalInParallelLoopYAML, sc)
	t.Logf("in-parallel-loop: status=%s outcome=%s reached=%v",
		res.Status, res.Execution.Outcome, res.Execution.NodesReached)

	require.Contains(t, res.Execution.NodesReached, "fanout.gate",
		"approval inside a parallel loop body must be dispatched inline")
	require.Equal(t, "completed", res.Execution.Outcome,
		"the run must complete rather than failing the gate as a StepExecutor defect")

	// Each of the three iterations must have consumed its own gate event. An
	// approval shared across iterations would leave events unconsumed.
	require.Empty(t, res.Mismatches,
		"every iteration must resolve its own approval; unconsumed events mean identities collided")
}
