package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A CallLLM result as the workflow decodes it: protojson without unpopulated
// fields, so everything unset — scalars, sub-messages, and fields inside
// present messages — is absent. Normalization restores every declared field.
func TestNormalizeActivityOutput_FillsEveryDeclaredField(t *testing.T) {
	t.Parallel()
	result := &reliantv1.CallLLMOutput{
		Message:   &reliantv1.MessageOutput{Role: "assistant"},
		ToolCalls: []*reliantv1.ToolCallMsg{{Id: "tc1", Name: "view"}},
	}
	decoded, err := activityResultMap(result)
	require.NoError(t, err)
	require.NotContains(t, decoded, "compaction_threshold", "precondition: protojson omits unset fields")

	normalized := normalizeActivityOutput(decoded, "CallLLM")

	assert.EqualValues(t, 0, normalized["compaction_threshold"])
	assert.Equal(t, "", normalized["response_text"])
	assert.Equal(t, false, normalized["pending_inbox"])
	assert.Equal(t, map[string]interface{}{}, normalized["response_data"], "unset Struct is an empty map, not null")

	message, ok := normalized["message"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, map[string]interface{}{"id": "", "role": "assistant", "text": ""}, message,
		"fields inside a present message are filled; present ones are kept")

	toolCalls, ok := normalized["tool_calls"].([]interface{})
	require.True(t, ok)
	require.Len(t, toolCalls, 1)
	assert.Equal(t, map[string]interface{}{"id": "tc1", "name": "view", "input": ""}, toolCalls[0],
		"each repeated-message element is filled")

	assert.NotContains(t, normalized, "thinking", "message_only fields are never reintroduced")
	assert.NotContains(t, decoded["message"], "id", "the caller's map is not mutated")
}

func TestNormalizeActivityOutput_UnsetMessageIsZeroShape(t *testing.T) {
	t.Parallel()
	normalized := normalizeActivityOutput(nil, "ExecuteTools")
	assert.Equal(t, map[string]interface{}{"id": "", "role": "", "text": ""}, normalized["message"])
	assert.Equal(t, map[string]interface{}{}, normalized["response_data"])
	assert.Equal(t, []interface{}{}, normalized["tool_results"])
	assert.EqualValues(t, 0, normalized["thread_token_count"])

	// A present-but-null sub-message is filled the same as an absent one.
	normalized = normalizeActivityOutput(map[string]interface{}{"message": nil}, "ExecuteTools")
	assert.Equal(t, map[string]interface{}{"id": "", "role": "", "text": ""}, normalized["message"])
}

// The reason the fill exists: templates over unset fields resolve instead of
// failing with "no such key".
func TestNormalizeActivityOutput_UnsetFieldsResolveInCEL(t *testing.T) {
	t.Parallel()
	normalized := normalizeActivityOutput(map[string]interface{}{"response_text": "hi"}, "CallLLM")
	ctx := &wfcel.EdgeEvalContext{Nodes: map[string]interface{}{"call_llm": normalized}}
	for _, expr := range []string{
		"nodes.call_llm.compaction_threshold == 0",
		"nodes.call_llm.message.text == ''",
		"nodes.call_llm.message.id == ''",
		"size(nodes.call_llm.response_data) == 0",
	} {
		got, err := wfcel.EvaluateBool(expr, ctx)
		require.NoError(t, err, expr)
		assert.True(t, got, expr)
	}
	_, err := wfcel.EvaluateBool("has(nodes.call_llm.thinking)", ctx)
	require.NoError(t, err)
}
