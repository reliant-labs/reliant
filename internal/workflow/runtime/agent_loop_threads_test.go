// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

func loopBody(nodeTypes ...string) *reliantv1.Workflow {
	wf := &reliantv1.Workflow{}
	for i, typ := range nodeTypes {
		wf.Nodes = append(wf.Nodes, &reliantv1.Node{Id: string(rune('a' + i)), Type: typ})
	}
	return wf
}

// Only a loop that calls the LLM, driven by the run's graph, is a thread a
// message to the run may be routed to: a spawn belongs to its parent agent,
// and a loop with no call_llm has no agent to read anything.
func TestRegisterAgentLoop(t *testing.T) {
	t.Parallel()

	tracker := &ChildWorkflowTracker{rootThread: "root"}
	loop := func(thread string, depth int, body *reliantv1.Workflow, prefix string) *InlineLoopExecutor {
		return &InlineLoopExecutor{
			childTracker:   tracker,
			subWorkflow:    body,
			loopID:         "agent_loop",
			nodePathPrefix: prefix,
			execContext:    &ExecutionContext{Thread: thread, SpawnDepth: depth},
		}
	}

	releaseRoot := loop("root", 0, loopBody("call_llm", "execute_tools"), "").registerAgentLoop()
	require.NotNil(t, releaseRoot)
	releaseNode := loop("implement-thread", 0, loopBody("call_llm"), "implement").registerAgentLoop()
	require.NotNil(t, releaseNode)
	assert.Nil(t, loop("spawn-thread", 1, loopBody("call_llm"), "").registerAgentLoop(), "a spawned sub-agent is not a run-level recipient")
	assert.Nil(t, loop("retry-thread", 0, loopBody("run", "compute"), "").registerAgentLoop(), "a loop with no call_llm has no agent")

	assert.Equal(t, []AgentLoopThread{
		{Thread: "implement-thread", Node: "implement.agent_loop"},
		{Thread: "root", Node: "agent_loop"},
	}, tracker.agentLoopThreads())
	assert.True(t, tracker.rootRanAgentLoop)

	releaseNode()
	assert.Equal(t, []AgentLoopThread{{Thread: "root", Node: "agent_loop"}}, tracker.agentLoopThreads(),
		"a loop that exited no longer reads anything")
	releaseRoot()
	assert.Empty(t, tracker.agentLoopThreads())
	assert.True(t, tracker.rootRanAgentLoop, "the root ran an agent loop in this execution, even once it exited")
}

// The real run answers the query: while the root agent and its two
// background spawns all wait on a usage limit, the root's loop is the one
// thread listed. The spawns are sub-agents, reached by thread id.
func TestAgentLoopThreadsQuery_ListsTheRootLoopAndNotItsSpawns(t *testing.T) {
	t.Parallel()
	var s temporaltest.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)

	input := spawnE2EWorkflowInput("chat-agent-loop-threads")
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	reset := env.Now().Add(4 * time.Hour)
	var c capacity
	e.parentTurn = func(turn int) ([]map[string]interface{}, error) {
		if turn == 1 {
			return []map[string]interface{}{
				spawnToolCall("tc-a", "implement the feature"),
				spawnToolCall("tc-b", "write the tests"),
			}, nil
		}
		if !c.ok() {
			return nil, usageLimitError(reset)
		}
		return nil, nil
	}
	e.childTurn = func(_ string, _ int) (map[string]interface{}, error) {
		if !c.ok() {
			return nil, usageLimitError(reset)
		}
		return map[string]interface{}{"response_text": "done", "tool_calls": nil}, nil
	}

	var listed []AgentLoopThread
	var queryErr error
	env.RegisterDelayedCallback(func() {
		val, err := env.QueryWorkflow(AgentLoopThreadsQuery)
		if err != nil {
			queryErr = err
			return
		}
		queryErr = val.Get(&listed)
	}, 5*time.Minute)
	env.RegisterDelayedCallback(func() {
		c.restore()
		env.SignalWorkflow(CapacityWakeSignalName, nil)
	}, 10*time.Minute)

	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.NoError(t, queryErr)
	require.Len(t, listed, 1, "only the root's agent loop is a run-level recipient: %+v", listed)
	assert.Equal(t, input.ExecContext.Thread, listed[0].Thread)
}
