// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Story 04: a structured-agent iteration in which the model calls a REGULAR
// tool (not the response tool). The loop's outputs expression
//
//	response: "{{ ... ? ... : null}}"
//
// then evaluates to null for that iteration. Historically this crashed
// output conversion with `proto: invalid type: structpb.NullValue` (the
// NullValue enum leaked through CEL → structpb conversion). This story pins
// the fix: iteration 1 produces null outputs, iteration 2 calls the response
// tool, and the workflow completes.
func TestStory04_LoopOutputsWithNullCompletes(t *testing.T) {
	t.Parallel()

	marker := "e2e-null-" + shortID()
	script := NewScriptedLLM(
		// Iteration 1: regular tool call → outputs.response evaluates to null.
		Turn{
			Text: "Let me check something first.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-bash-1", tools.ShellToolName, fmt.Sprintf(`{"command":"echo %s"}`, marker)),
			},
		},
		// Iteration 2: response tool (default schema: choice/value) → completed.
		Turn{
			Text: "Done, submitting my response.",
			ToolCalls: []message.ToolCall{
				ToolCall("call-resp-1", "submit_response", `{"choice":"complete","value":"all good"}`),
			},
		},
	)

	h := newHarness(t, script)

	created := h.CreateChat("builtin://structured-agent", "Do a check, then finish", map[string]any{
		"mode": "auto",
	})
	workflowID := created.WorkflowId

	// Without the NullValue normalization fix, the workflow fails after
	// iteration 1 instead of completing.
	outputs := h.WorkflowResult(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	require.Equal(t, true, outputs["completed"], "outputs: %v", outputs)
	response, ok := outputs["response"].(map[string]interface{})
	require.True(t, ok, "outputs.response must be an object, got %T", outputs["response"])
	assert.Equal(t, "complete", response["choice"])
	assert.Equal(t, "all good", response["value"])

	// Both iterations ran: the bash result reached the second LLM call.
	streamCalls := h.LLM.StreamCalls()
	require.Len(t, streamCalls, 2)
	var sawMarker bool
	for i := range streamCalls[1].Messages {
		for _, tr := range streamCalls[1].Messages[i].ToolResults() {
			if strings.Contains(tr.Content, marker) {
				sawMarker = true
			}
		}
	}
	assert.True(t, sawMarker, "iteration-1 bash output must feed iteration 2")
	assert.False(t, h.LLM.Exhausted())
}
