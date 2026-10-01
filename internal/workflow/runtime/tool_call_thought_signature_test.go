// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Gemini 3.x rejects a request whose functionCall parts do not echo back the
// thought_signature the model emitted ("Function call is missing a
// thought_signature in functionCall parts"), so the signature has to survive
// the whole CallLLM → save_message → database path. The hop that lost it was
// the proto one: CallLLMOutput.ToolCalls had no signature field, so by the time
// convertToToolCalls read m["thought_signature"] there was nothing there.

const testThoughtSignature = "CrYBAdHtim9sig"

func signedCallLLMOutput() *reliantv1.CallLLMOutput {
	return &reliantv1.CallLLMOutput{
		Message:      &reliantv1.MessageOutput{Role: "assistant", Text: "Reading it."},
		ResponseText: "Reading it.",
		ToolCalls: []*reliantv1.ToolCallMsg{{
			Id:               "tc1",
			Name:             "view",
			Input:            `{"file_path":"a.go"}`,
			ThoughtSignature: testThoughtSignature,
		}},
		Model: "gemini-3-pro",
	}
}

// The real delegated path: render the activity result exactly as the workflow
// will see it, then resolve agent.yaml's call_llm save_message against it.
func TestDelegatedSave_CarriesToolCallThoughtSignature(t *testing.T) {
	t.Parallel()

	output, err := activityResultMap(resultForSave(signedCallLLMOutput()))
	require.NoError(t, err)

	saveInput, err := ResolveDelegatedSaveMessage(
		saveRequest(agentAssistantSave(), nil),
		"CallLLM",
		output,
		DelegatedSaveIdentity{ChatID: "chat-1", Thread: "thread-1", WorkflowID: "wf-1", StepID: "call_llm"},
	)
	require.NoError(t, err)
	require.NotNil(t, saveInput)
	require.Len(t, saveInput.ToolCalls, 1)
	assert.Equal(t, "tc1", saveInput.ToolCalls[0].ID)
	assert.Equal(t, testThoughtSignature, saveInput.ToolCalls[0].ThoughtSignature,
		"the signature must reach the message the save writes")
}

// buildSaveMessageNode is what the wrapper hands the writer, so a signature
// dropped here never reaches the database.
func TestBuildSaveMessageNode_CarriesToolCallThoughtSignature(t *testing.T) {
	t.Parallel()

	node := buildSaveMessageNode(&types.SaveMessageInput{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:               "tc1",
			Name:             "view",
			Input:            `{"file_path":"a.go"}`,
			ThoughtSignature: testThoughtSignature,
		}},
	})

	calls := node.GetSaveMessageNode().GetResolvedToolCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, testThoughtSignature, calls[0].GetThoughtSignature())
}

// Rule 2 at any depth: the signature is persisted with the message and then
// stripped from what goes back to the workflow — signatures are kilobytes and
// every tool call carries one, so they must not enter Temporal history.
func TestClearMessageOnlyFields_StripsNestedToolCallSignature(t *testing.T) {
	t.Parallel()

	out := signedCallLLMOutput()
	out.ToolCalls[0].ThoughtSignature = strings.Repeat("s", 4096)
	clearMessageOnlyFields(&out)

	require.Len(t, out.GetToolCalls(), 1)
	assert.Empty(t, out.GetToolCalls()[0].GetThoughtSignature(),
		"a nested message_only field must be cleared too")
	assert.Equal(t, "tc1", out.GetToolCalls()[0].GetId(), "siblings are untouched")
	assert.Equal(t, "view", out.GetToolCalls()[0].GetName())
	assert.Equal(t, `{"file_path":"a.go"}`, out.GetToolCalls()[0].GetInput())
}
