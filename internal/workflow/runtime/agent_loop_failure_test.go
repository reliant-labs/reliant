// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// The Autopilot agent loop, as shipped (builtin/autopilot-agent.yaml), driven
// by the real DynamicWorkflow. Only the two activities that talk to a model and
// to tools are stubbed; the loop, its while-condition, the response-tool
// detection and the step executor are the real ones.
//
// A production run (chat 84ffd041) called its response tool twenty-eight times,
// was told "unknown tool ... no placement" every time, was reminded to call it
// again each turn, and then ended COMPLETED with nothing posted.

func loadShippedAgentLoop(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../builtin/autopilot-agent.yaml")
	require.NoError(t, err)
	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err)
	out, err := protojson.Marshal(wf)
	require.NoError(t, err)
	return out
}

type agentLoopStubs struct {
	callLLM      atomic.Int32
	executeTools atomic.Int32
	saveMessage  atomic.Int32

	mu sync.Mutex
	// expectedResponseTools is what each ExecuteTools was told is a response tool.
	expectedResponseTools [][]string
	// statuses is every WorkflowStatus notification, in order.
	statuses []map[string]interface{}
}

func (s *agentLoopStubs) lastStatus(t *testing.T) map[string]interface{} {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.statuses)
	return s.statuses[len(s.statuses)-1]
}

// registerAgentLoopStubs scripts every model turn to call `submit_review` and
// answers every ExecuteTools with toolResult.
func registerAgentLoopStubs(t *testing.T, env *testsuite.TestWorkflowEnvironment, wfJSON []byte, toolResult map[string]interface{}) *agentLoopStubs {
	t.Helper()
	s := &agentLoopStubs{}
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.LoadWorkflowInput) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (interface{}, error) {
		s.mu.Lock()
		s.statuses = append(s.statuses, in)
		s.mu.Unlock()
		return nil, nil
	}, activity.RegisterOptions{Name: "WorkflowStatus"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "Cleanup"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return nil, nil
	}, activity.RegisterOptions{Name: "WorkflowError"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ types.ActivityInput) (map[string]interface{}, error) {
		s.callLLM.Add(1)
		return map[string]interface{}{
			"response_text": "",
			"stop_reason":   "tool_use",
			"model":         "stub",
			"tool_calls": []interface{}{map[string]interface{}{
				"id": "tc", "name": "submit_review", "input": `{"verdict":"COMMENT"}`,
			}},
		}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	env.RegisterActivityWithOptions(func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
		s.executeTools.Add(1)
		s.mu.Lock()
		s.expectedResponseTools = append(s.expectedResponseTools, in.Node.GetExecuteTools().GetExpectedResponseTools())
		s.mu.Unlock()
		return toolResult, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})
	// The budget warning (finish_warning_turns before max_turns) saves a system message.
	env.RegisterActivityWithOptions(func(ctx context.Context, in map[string]interface{}) (interface{}, error) {
		s.saveMessage.Add(1)
		return stubSaveMessage(ctx, in)
	}, activity.RegisterOptions{Name: "SaveMessage"})
	return s
}

func agentLoopInput(chatID string, unattended bool) WorkflowInput {
	in := resumeWorkflowInput(chatID, nil)
	in.WorkflowName = "autopilot-agent"
	in.ExecContext.WorkflowName = "autopilot-agent"
	in.Inputs = map[string]interface{}{
		"response_tool_name": "submit_review",
		"max_turns":          int64(40),
		InputKeyLaunchRun:    true,
	}
	if unattended {
		in.Inputs[InputKeyUnattended] = true
	}
	return in
}

// The response tool's name comes from `{{ inputs.response_tool_name }}`, in the
// spaced spelling the shipped YAML uses. execute_tools must be told it is a
// response tool: otherwise a call to it is dispatched like any registry tool
// and refused ("unknown tool: no placement") however well the model used it.
func TestAutopilotAgentLoop_ExecuteToolsKnowsTheResponseTool(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	wfJSON := loadShippedAgentLoop(t)
	stubs := registerAgentLoopStubs(t, env, wfJSON, map[string]interface{}{
		"tool_results": []interface{}{map[string]interface{}{
			"tool_call_id": "tc", "name": "submit_review", "content": "ok", "is_error": false,
			"metadata": `{"verdict":"COMMENT"}`,
		}},
		"response_data": map[string]interface{}{"submit_review": map[string]interface{}{"verdict": "COMMENT"}},
	})

	env.ExecuteWorkflow(DynamicWorkflow, agentLoopInput("chat-agent-known", true))

	require.NoError(t, env.GetWorkflowError())
	require.NotEmpty(t, stubs.expectedResponseTools, "the agent loop ran execute_tools")
	for _, got := range stubs.expectedResponseTools {
		assert.Equal(t, []string{"submit_review"}, got,
			"execute_tools must be told which tool is the response tool, resolved from inputs")
	}
}

// An unattended agent whose tool calls fail identically, turn after turn, must
// have its step ended with an error rather than looping to max_turns, and the
// run must END FAILED, not completed.
func TestAutopilotAgentLoop_RepeatedIdenticalToolErrorsFailTheRun(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	wfJSON := loadShippedAgentLoop(t)
	stubs := registerAgentLoopStubs(t, env, wfJSON, map[string]interface{}{
		"tool_results": []interface{}{map[string]interface{}{
			"tool_call_id": "tc", "name": "submit_review", "is_error": true,
			"content": `Tool execution failed: unknown tool "submit_review": no placement`,
		}},
		"response_data": map[string]interface{}{"submit_review": nil},
	})

	env.ExecuteWorkflow(DynamicWorkflow, agentLoopInput("chat-agent-stuck", true))

	require.True(t, env.IsWorkflowCompleted())
	assert.Equal(t, int32(RepeatedToolErrorLimit), stubs.executeTools.Load(),
		"the agent is stopped after exactly the limit, not after max_turns (40)")
	assert.Equal(t, int32(RepeatedToolErrorLimit), stubs.callLLM.Load())
	assert.Equal(t, "failed", stubs.lastStatus(t)["status"],
		"an agent that never produced its response is a FAILED run, not a completed one")
}

// A person's chat is never cut off by the breaker: someone is there to
// redirect it.
func TestAutopilotAgentLoop_AttendedRunsAreNotCutOff(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	wfJSON := loadShippedAgentLoop(t)
	stubs := registerAgentLoopStubs(t, env, wfJSON, map[string]interface{}{
		"tool_results": []interface{}{map[string]interface{}{
			"tool_call_id": "tc", "name": "submit_review", "is_error": true, "content": "same error",
		}},
		"response_data": map[string]interface{}{"submit_review": nil},
	})
	in := agentLoopInput("chat-agent-attended", false)
	in.Inputs["max_turns"] = int64(6)

	env.ExecuteWorkflow(DynamicWorkflow, in)

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, int32(6), stubs.executeTools.Load(), "attended: runs until max_turns as before")
	assert.Equal(t, int32(1), stubs.saveMessage.Load(), "the turn-budget warning is saved once, finish_warning_turns before the end")
}
