package runtime

import (
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
)

func TestWhileConditionWithToolCalls(t *testing.T) {
	// Simulate what the loop while condition gets: outputs from EvaluateWorkflowOutputs
	// tool_calls is []interface{} containing map[string]interface{} (from JSON deserialization)
	outputs := map[string]interface{}{
		"tool_calls": []interface{}{
			map[string]interface{}{
				"id":    "toolu_123",
				"name":  "Bash",
				"type":  "tool_use",
				"input": map[string]interface{}{"command": "echo test"},
			},
		},
		"message": map[string]interface{}{
			"role": "assistant",
			"text": "I'll run that.",
		},
		"response_text": "I'll run that.",
	}

	ctx := &wfcel.LoopEvalContext{
		Outputs: outputs,
		Iter:    &model.IterContext{Iteration: 1},
		Inputs:  map[string]interface{}{"max_turns": 200},
	}

	whileExpr := "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"

	result, err := wfcel.EvaluateBool(whileExpr, ctx)
	require.NoError(t, err, "while condition should evaluate without error")
	require.True(t, result, "while condition should be true when tool_calls exist and under max_turns")
}
