// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// Story 03: the model emits a response-tool call whose array-typed argument
// is a STRINGIFIED JSON array (`{"slides": "[\"a\",\"b\"]"}` instead of
// `{"slides": ["a","b"]}`) — a real failure mode of several models. The
// schema-repair path (internal/llm/tools/schema_repair.go, wired into the
// response-tool validation in execute_tools.go) must repair it, validate,
// and complete the structured-agent workflow with a REAL array in the
// outputs.
func TestStory03_StringifiedArrayArgIsRepaired(t *testing.T) {
	t.Parallel()

	script := NewScriptedLLM(
		Turn{
			Text: "Submitting the slides now.",
			ToolCalls: []message.ToolCall{
				// The malformed shape: slides is a JSON-encoded string, not an array.
				ToolCall("call-resp-1", "submit_slides", `{"slides":"[\"intro\",\"body\",\"closing\"]"}`),
			},
		},
	)

	h := newHarness(t, script)

	created := h.CreateChat("builtin://structured-agent", "Make me a 3-slide outline", map[string]any{
		"mode":               "auto",
		"response_tool_name": "submit_slides",
		"response_schema": map[string]any{
			"type":     "object",
			"required": []any{"slides"},
			"properties": map[string]any{
				"slides": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			},
		},
	})
	chatID := created.Chat.Id
	workflowID := created.WorkflowId

	// The workflow must complete: repaired input validates, response_data is
	// populated, completed=true, loop exits.
	outputs := h.WorkflowResult(workflowID)
	h.WaitWorkflowStatus(workflowID, db.Completed())

	require.Equal(t, true, outputs["completed"], "response tool must count as completed; outputs: %v", outputs)

	response, ok := outputs["response"].(map[string]interface{})
	require.True(t, ok, "outputs.response must be an object, got %T (%v)", outputs["response"], outputs["response"])
	slides, ok := response["slides"].([]interface{})
	require.True(t, ok, "slides must have been REPAIRED into a real array, got %T (%v)", response["slides"], response["slides"])
	assert.Equal(t, []interface{}{"intro", "body", "closing"}, slides)

	// The persisted tool result must not be an error.
	for _, m := range h.Messages(chatID, workflowID) {
		for _, b := range m.Blocks {
			if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT && b.IsError != nil {
				assert.False(t, *b.IsError, "repaired response tool call must not produce an error result: %v", strPtr(b.Content))
			}
		}
	}

	assert.False(t, h.LLM.Exhausted())
	require.Len(t, h.LLM.StreamCalls(), 1, "one turn should be enough — no retry/reminder loop")
}

func strPtr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
