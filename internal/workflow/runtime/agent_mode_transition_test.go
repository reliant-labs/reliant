// Copyright (c) 2026 Reliant Labs
package runtime

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Exercise one ongoing builtin agent, not two launches with different defaults.
// The server resolves a composer's null reset to "auto" before signalling; that
// transport contract is tested separately. This test starts at that signal and
// observes the actual evaluated CallLLM dispatch after resuming its inline loop.
func TestBuiltinAgent_PlanToAuto_ResumedCallRestoresPromptAndTools(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../builtin/agent.yaml")
	require.NoError(t, err)
	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err)
	encoded, err := protojson.Marshal(wf)
	require.NoError(t, err)
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context, types.LoadWorkflowInput) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: encoded}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	env.RegisterActivityWithOptions(func(context.Context, types.WorkflowCheckpointInput) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	for _, name := range []string{"WorkflowStatus", "WorkflowError", "EmitToolCallStatus", "Cleanup", "SaveMessage"} {
		env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"success": true, "message_id": "message-mode-transition"}, nil
		}, activity.RegisterOptions{Name: name})
	}
	env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	var mu sync.Mutex
	var calls []*reliantv1.Node
	toolExecutions := 0
	env.RegisterActivityWithOptions(func(context.Context, types.ActivityInput) (map[string]interface{}, error) {
		mu.Lock()
		toolExecutions++
		mu.Unlock()
		return map[string]interface{}{"tool_results": []interface{}{}, "thread_token_count": 0}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})
	env.RegisterActivityWithOptions(func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
		mu.Lock()
		calls = append(calls, proto.Clone(in.Node).(*reliantv1.Node))
		attempt := len(calls)
		mu.Unlock()
		if attempt == 1 {
			env.SignalWorkflow("signal.pause", nil)
			return nil, temporal.NewCanceledError("paused in plan mode")
		}
		if attempt == 2 {
			return map[string]interface{}{
				"response_text": "Implementing now", "stop_reason": "tool_use", "compaction_threshold": 1000,
				"tool_calls": []interface{}{map[string]interface{}{"id": "edit-after-auto", "name": "edit", "input": `{}`}},
				"message":    map[string]interface{}{"role": "assistant", "text": "Implementing now"},
			}, nil
		}
		return map[string]interface{}{
			"response_text": "done", "tool_calls": []interface{}{}, "stop_reason": "done",
			"message": map[string]interface{}{"role": "assistant", "text": "done"},
		}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	// Temporal virtual time advances only once the paused workflow is waiting.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("update_workflow_state", map[string]interface{}{"mode": "auto"})
		env.SignalWorkflow("signal.resume", nil)
	}, time.Second)
	input := nestedPauseInput("chat-plan-to-auto")
	input.WorkflowName = "builtin://agent"
	input.ExecContext.WorkflowName = "builtin://agent"
	input.Inputs = map[string]interface{}{
		"mode": "plan", "system_prompt": "Keep this custom system instruction.",
		"model": map[string]interface{}{"id": userSwitchedTo},
	}
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, calls, 3, "resume the cancelled call, execute its tool, then finish the next iteration")
	assert.Equal(t, 1, toolExecutions, "auto must execute tools without an approval signal")
	plan, auto := calls[0].GetCallLlm(), calls[1].GetCallLlm()
	require.NotNil(t, plan)
	require.NotNil(t, auto)
	assert.Contains(t, model.CelStringValue(plan.GetSystemPrompt()), "PLANNING MODE ACTIVE")
	assert.Contains(t, model.CelStringValue(plan.GetSystemPrompt()), "Keep this custom system instruction.")
	assert.Equal(t, "Keep this custom system instruction.", model.CelStringValue(auto.GetSystemPrompt()))
	assert.ElementsMatch(t, []string{"tag:coding:plan", "tag:shell"}, model.CelStringListValue(plan.GetToolsConfig().GetPreloadedTools()))
	assert.ElementsMatch(t, []string{"tag:coding:plan", "tag:shell"}, model.CelStringListValue(plan.GetToolsConfig().GetLoadableTools()))
	assert.Equal(t, []string{"tag:coding:default"}, model.CelStringListValue(auto.GetToolsConfig().GetPreloadedTools()))
	assert.Equal(t, []string{"*"}, model.CelStringListValue(auto.GetToolsConfig().GetLoadableTools()))
}
