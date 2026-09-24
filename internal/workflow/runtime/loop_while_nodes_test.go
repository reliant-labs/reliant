// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// A loop's `while` may read the PARENT scope's node outputs (nodes.*), the
// same scope the simulator's evaluateLoopWhileStrict evaluates it in. The real
// InlineLoopExecutor used to build its while context without Nodes, so any
// such condition failed with "no such key" after iteration 0 — while the
// simulator, which does pass nodes, reported the workflow green. The builtin
// forge-migrate port_loop (`size(nodes.inventory.response.components)`) and
// migrate's workflow_builder_loop are the shapes that broke.
const loopWhileReadsParentNodesYAML = `
name: resume-test
entry: [plan]
nodes:
  - id: plan
    type: call_llm
  - id: agent_loop
    type: loop
    while: iter.iteration < 3 && nodes.plan.response_text == "ok"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
edges:
  - from: plan
    default: agent_loop
`

func TestInlineLoop_WhileReadsParentScopeNodeOutputs(t *testing.T) {
	t.Parallel()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, loopWhileReadsParentNodesYAML)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-while-nodes", nil))

	require.NoError(t, env.GetWorkflowError(),
		"a while condition reading nodes.* must evaluate against the parent scope's node outputs")
	assert.Equal(t, []string{"plan", "work", "work", "work"}, rec.executedNodes,
		"the loop must continue past iteration 0 while the parent-scope condition holds")
}
