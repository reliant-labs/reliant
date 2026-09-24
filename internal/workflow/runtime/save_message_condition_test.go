// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// save_message.condition is raw CEL that must return bool, like every other
// condition field. It used to be a template string: a value without {{ }}
// parsed as a LITERAL, and any non-empty literal was "truthy" — so conditions
// written the way every other condition is written never gated anything.
//
// These tests drive the builtin workflows' real save_message configs, parsed
// from the embedded YAML, so they pin what ships.

// builtinSaveMessage returns the save_message of the node with id nodeID
// (searched through inline sub-workflows) in the named builtin workflow.
func builtinSaveMessage(t *testing.T, file, nodeID string) *reliantv1.SaveMessageConfig {
	t.Helper()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(file)
	require.NoError(t, err)
	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err)
	var find func(*reliantv1.Workflow) *reliantv1.SaveMessageConfig
	find = func(w *reliantv1.Workflow) *reliantv1.SaveMessageConfig {
		for _, n := range w.GetNodes() {
			if n.GetId() == nodeID {
				return n.GetSaveMessage()
			}
			if inline := model.NodeInlineWorkflow(n); inline != nil {
				if sm := find(inline); sm != nil {
					return sm
				}
			}
		}
		return nil
	}
	sm := find(wf)
	require.NotNil(t, sm, "%s: node %q has no save_message", file, nodeID)
	return sm
}

func conditionScope(inputs map[string]interface{}) saveMessageScope {
	if inputs == nil {
		inputs = map[string]interface{}{}
	}
	return saveMessageScope{
		Inputs:     inputs,
		Workflow:   &model.WorkflowContext{ID: "wf-1", Name: "test"},
		Iter:       &model.IterContext{Iteration: 0},
		ChatID:     "chat-1",
		Thread:     "thread-1",
		WorkflowID: "wf-1",
		StepID:     "node-save",
	}
}

// auditOutput is execute_audit's output for an audit tool call. guidance is
// omitted when empty, exactly as an approving auditor omits it.
func auditOutput(guidance string) map[string]interface{} {
	audit := map[string]interface{}{"approved": true}
	if guidance != "" {
		audit["approved"] = false
		audit["guidance"] = guidance
	}
	return map[string]interface{}{
		"tool_results":  []interface{}{},
		"response_data": map[string]interface{}{"audit": audit},
	}
}

// An approved audit omits guidance. The condition must decline the save —
// before, it was a literal (always "true"), content then read the missing
// guidance, and the save failed the step on every clean approval.
func TestSaveMessageCondition_AuditingAgentApprovedWithoutGuidance(t *testing.T) {
	t.Parallel()
	sm := builtinSaveMessage(t, "auditing-agent.yaml", "execute_audit")

	saveInput, skip, err := resolveSaveMessage(sm, auditOutput(""), conditionScope(nil))
	require.NoError(t, err, "a clean approval must not error")
	assert.Nil(t, saveInput, "no feedback message on a clean approval")
	assert.Equal(t, saveSkipCondition, skip)

	saveInput, _, err = resolveSaveMessage(sm, auditOutput("add a test for the nil case"), conditionScope(nil))
	require.NoError(t, err)
	require.NotNil(t, saveInput, "non-empty guidance must be saved")
	assert.Equal(t, "user", saveInput.Role)
	assert.Contains(t, saveInput.Content, "add a test for the nil case")
}

// The same audit through the ActivityWrapper, which is who performs the save
// for an execute_tools node: an approval without guidance completes the
// activity and writes nothing; guidance is written as a user message.
func TestSaveMessageCondition_AuditingAgentThroughWrapper(t *testing.T) {
	t.Parallel()
	sm := builtinSaveMessage(t, "auditing-agent.yaml", "execute_audit")

	for name, tc := range map[string]struct {
		guidance string
		wantMsgs int
	}{
		"approved, no guidance":  {guidance: "", wantMsgs: 0},
		"rejected with guidance": {guidance: "cover the error path", wantMsgs: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			responseData, err := structpb.NewStruct(auditOutput(tc.guidance)["response_data"].(map[string]interface{}))
			require.NoError(t, err)
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			env.SetDataConverter(rtemporal.NewFlexibleDataConverter())
			writer := &recordingMessageWriter{}
			registry := NewActivityRegistry(&wrapperTestRepo{})
			registry.SetMessageWriter(writer)
			registerWrapped(env, registry, "ExecuteTools",
				func(context.Context, types.ActivityInput) (*reliantv1.ExecuteToolsOutput, error) {
					return &reliantv1.ExecuteToolsOutput{ResponseData: responseData}, nil
				})
			input := callLLMInput(saveRequest(sm, nil))
			input.Runtime.StepID = "execute_audit"
			input.Runtime.AssistantMessageID = ""
			input.Node = &reliantv1.Node{Id: "execute_audit", Type: model.NodeTypeExecuteTools}

			_, err = env.ExecuteActivity("ExecuteTools", input)
			require.NoError(t, err, "the audit step must complete")
			msgs := writer.messages()
			require.Len(t, msgs, tc.wantMsgs)
			if tc.wantMsgs == 1 {
				assert.Equal(t, "user", msgs[0].Args.GetResolvedRole())
				assert.Contains(t, msgs[0].Args.GetResolvedContent(), tc.guidance)
			}
		})
	}
}

// get-it-right's implement and review saves are gated on context_bridge:
// with 'none' the next attempt is meant to start with no carried context,
// so neither summary may be written to the parent thread.
func TestSaveMessageCondition_GetItRightContextBridge(t *testing.T) {
	t.Parallel()
	implementOutput := map[string]interface{}{"response_text": "changed foo.go"}
	reviewOutput := map[string]interface{}{
		"response": map[string]interface{}{"strategy": "continue", "feedback": "almost"},
	}
	for _, node := range []struct {
		id     string
		output map[string]interface{}
	}{{"implement", implementOutput}, {"review", reviewOutput}} {
		sm := builtinSaveMessage(t, "get-it-right.yaml", node.id)

		saveInput, skip, err := resolveSaveMessage(sm, node.output, conditionScope(map[string]interface{}{"context_bridge": "none"}))
		require.NoError(t, err, node.id)
		assert.Nil(t, saveInput, "%s: context_bridge=none must not save", node.id)
		assert.Equal(t, saveSkipCondition, skip, node.id)

		for _, bridge := range []string{"summary", "full"} {
			saveInput, _, err = resolveSaveMessage(sm, node.output, conditionScope(map[string]interface{}{"context_bridge": bridge}))
			require.NoError(t, err, node.id)
			assert.NotNil(t, saveInput, "%s: context_bridge=%s must save", node.id, bridge)
		}
	}
}

// get-it-right's lint/test/build run nodes save only a FAILING lane.
func TestSaveMessageCondition_GetItRightRunLanes(t *testing.T) {
	t.Parallel()
	for _, lane := range []string{"lint", "test", "build"} {
		sm := builtinSaveMessage(t, "get-it-right.yaml", lane)
		output := func(exit int) map[string]interface{} {
			return map[string]interface{}{"exit_code": exit, "log_file": "/tmp/x.log", "working_dir": "/w", "stdout": "", "stderr": ""}
		}

		saveInput, skip, err := resolveSaveMessage(sm, output(0), conditionScope(nil))
		require.NoError(t, err, lane)
		assert.Nil(t, saveInput, "%s: a passing lane must not save", lane)
		assert.Equal(t, saveSkipCondition, skip, lane)

		saveInput, _, err = resolveSaveMessage(sm, output(2), conditionScope(nil))
		require.NoError(t, err, lane)
		require.NotNil(t, saveInput, "%s: a failing lane must save", lane)
		assert.Contains(t, saveInput.Content, "exit 2")
	}
}

// A condition that does not return bool is an error, never "truthy".
func TestSaveMessageCondition_NonBoolIsError(t *testing.T) {
	t.Parallel()
	data := []byte(`
name: t
entry: [n]
nodes:
  - id: n
    type: call_llm
    save_message:
      condition: "output.response_text"
      role: assistant
      content: "{{output.response_text}}"
`)
	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err)
	sm := wf.GetNodes()[0].GetSaveMessage()
	_, _, err = resolveSaveMessage(sm, map[string]interface{}{"response_text": "hi"}, conditionScope(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "condition")
}
