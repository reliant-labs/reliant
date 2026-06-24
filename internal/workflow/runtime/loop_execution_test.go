package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestLoopExecution_LoopOutput tests proto-backed loop output structure.
func TestLoopExecution_LoopOutput(t *testing.T) {
	t.Run("loop output contains sub-workflow outputs", func(t *testing.T) {
		protoOutputs, err := structpb.NewStruct(map[string]interface{}{
			"exit_code": 0,
			"success":   true,
		})
		require.NoError(t, err)

		output := reliantv1.LoopOutput{Outputs: protoOutputs}
		assert.Equal(t, float64(0), output.GetOutputs().AsMap()["exit_code"])
		assert.Equal(t, true, output.GetOutputs().AsMap()["success"])
	})

	t.Run("loop output map flattening keeps _iterations", func(t *testing.T) {
		protoOutputs, err := structpb.NewStruct(map[string]interface{}{
			"exit_code": 1,
			"success":   false,
		})
		require.NoError(t, err)

		output := reliantv1.LoopOutput{
			Iterations: 3,
			Outputs:    protoOutputs,
		}
		outputMap := model.LoopOutputToMap(int(output.GetIterations()), output.GetOutputs().AsMap())
		assert.Equal(t, float64(1), outputMap["exit_code"])
		assert.Equal(t, false, outputMap["success"])
		assert.Equal(t, 3, outputMap["_iterations"])
	})
}

// TestLoopExecution_StateMachineRouting tests state machine routing after loop completion
func TestLoopExecution_StateMachineRouting(t *testing.T) {
	t.Run("state machine routes based on loop output", func(t *testing.T) {
		wfJSON := `{
			"name": "test-routing",
			"entry": ["check_loop"],
			"nodes": [
				{"id": "check_loop", "type": "loop", "while": "outputs.status == 'ready' || iter.iteration >= 3", "ref": "status-check"},
				{"id": "proceed", "type": "noop"},
				{"id": "wait", "type": "noop"}
			],
			"edges": [
				{"from": "check_loop", "cases": [
					{"to": "proceed", "condition": "nodes.check_loop.status == 'ready'"},
					{"to": "wait", "condition": "nodes.check_loop.status != 'ready'"}
				]}
			]
		}`
		workflow, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		sm := NewSimplifiedStateMachine("test-workflow-id", workflow)

		nodeOutputs := map[string]interface{}{
			"check_loop": map[string]interface{}{
				"status": "ready",
			},
		}

		event := &core.WorkflowEvent{
			ID:         "loop-complete",
			WorkflowID: "test-workflow-id",
			StepID:     "check_loop",
			Data:       nodeOutputs["check_loop"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		assert.Equal(t, 1, len(triggered))
		assert.Equal(t, "proceed", triggered[0].Node.GetId())
	})
}

func TestBackfillNodeOutputsFromEvents(t *testing.T) {
	nodeOutputs := map[string]interface{}{}
	events := []*core.WorkflowEvent{
		nil,
		{ID: "workflow-start", StepID: "", Data: map[string]interface{}{"ignored": true}},
		{ID: "missing-data", StepID: "call_llm", Data: nil},
		{ID: "call-llm-complete", StepID: "call_llm", Data: map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"name": "spawn"}}}},
	}

	backfillNodeOutputsFromEvents(events, nodeOutputs)

	callLLMOutput, ok := nodeOutputs["call_llm"].(map[string]interface{})
	require.True(t, ok, "call_llm output should be backfilled from completion event data")
	require.Contains(t, callLLMOutput, "tool_calls")
}
