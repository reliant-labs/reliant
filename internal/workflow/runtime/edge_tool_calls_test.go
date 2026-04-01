package runtime

import (
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
)

func TestEdgeCondition_ToolCallsNotNull(t *testing.T) {
	// Simulate what happens when call_llm returns tool calls
	// After Temporal serialization/deserialization, tool_calls becomes []interface{}{...}
	nodeOutputs := map[string]interface{}{
		"call_llm": map[string]interface{}{
			"tool_calls": []interface{}{
				map[string]interface{}{
					"id":    "toolu_123",
					"name":  "Bash",
					"type":  "tool_use",
					"input": `{"command":"echo test"}`,
				},
			},
			"response_text": "Let me run that",
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "Let me run that",
			},
			"token_count": float64(150),
			"thinking": map[string]interface{}{
				"content":   "",
				"signature": "",
			},
		},
	}

	workflowInputs := map[string]interface{}{
		"mode": "auto",
	}

	ctx := &wfcel.EdgeEvalContext{
		Nodes:    nodeOutputs,
		Inputs:   workflowInputs,
		Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
	}

	// Test the edge condition that routes to execute_tools
	condition := `nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0 && inputs.mode != 'manual'`

	result, err := wfcel.EvaluateBool(condition, ctx)
	if err != nil {
		t.Fatalf("Edge condition evaluation error: %v", err)
	}

	assert.True(t, result, "Edge condition should match for auto mode with tool calls")

	// Also test with no tool calls
	nodeOutputs2 := map[string]interface{}{
		"call_llm": map[string]interface{}{
			"tool_calls":    nil,
			"response_text": "Hello",
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "Hello",
			},
			"token_count": float64(150),
		},
	}

	ctx2 := &wfcel.EdgeEvalContext{
		Nodes:    nodeOutputs2,
		Inputs:   workflowInputs,
		Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
	}

	result2, err := wfcel.EvaluateBool(condition, ctx2)
	if err != nil {
		t.Fatalf("Edge condition evaluation error for no tool calls: %v", err)
	}

	assert.False(t, result2, "Edge condition should NOT match when tool_calls is nil")
}
