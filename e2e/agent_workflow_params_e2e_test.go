package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	messageModel "github.com/reliant-labs/reliant/internal/models/message"
)

func TestAgentWorkflowStartWithParams_RecordsToolsAndExecutesToolCall(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponseWithToolCall(
		"I'll run that now.",
		"bash",
		map[string]interface{}{"command": "echo hello"},
	)
	h.MockLLM.AddResponse(MockResponse{Text: "Done: hello"})
	h.MockTools.On("bash", MockToolResponse{Result: "hello\n", Success: true})

	chatID := h.StartAgentWorkflowViaGRPC(
		t,
		"run echo hello",
		WithWorkflow("builtin://agent"),
		WithWorkflowParam("tools", []interface{}{"tag:default"}),
		WithWorkflowParam("spawn_presets", []interface{}{"general", "researcher", "code_reviewer"}),
	)

	messages := h.WaitForMessages(t, chatID, 2)
	AssertMessageRolesInOrder(
		t,
		messages,
		reliantv1.MessageRole_MESSAGE_ROLE_USER,
		reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
	)
	AssertToolCallExists(t, h.DB, messages[1].ID, "bash")

	require.Eventually(t, func() bool {
		return h.MockTools.CallCountFor("bash") == 1
	}, 5*time.Second, 50*time.Millisecond, "expected emitted tool call to execute exactly once")
	toolCalls := h.MockTools.GetCallsFor("bash")
	require.Len(t, toolCalls, 1, "expected exactly one bash execution")
	require.Contains(t, toolCalls[0].Input, "echo hello", "expected emitted tool call to execute echo hello")

	calls := h.MockLLM.GetCalls()
	require.NotEmpty(t, calls, "expected at least one captured LLM call")
	initialCall := requireCallContainingPromptWithTools(t, calls, "run echo hello")
	assertCapturedCallHasToolSnapshot(t, initialCall, "run echo hello")
	require.Contains(t, initialCall.ToolNames, "bash", "expected bash to be present in available tools passed to LLM")
}

func requireCallContainingPromptWithTools(t *testing.T, calls []MockCall, prompt string) MockCall {
	t.Helper()

	matchingCalls := make([]MockCall, 0)
	for _, call := range calls {
		if callHasUserPrompt(call, prompt) {
			matchingCalls = append(matchingCalls, call)
		}
	}
	require.NotEmpty(t, matchingCalls, "expected at least one LLM call containing user prompt %q", prompt)

	for _, call := range matchingCalls {
		if len(call.Tools) > 0 {
			return call
		}
	}

	toolCounts := make([]int, 0, len(matchingCalls))
	for _, call := range matchingCalls {
		toolCounts = append(toolCounts, len(call.Tools))
	}
	require.Failf(t, "expected tools for prompt-matching LLM call", "prompt=%q matching_calls=%d tool_counts=%v", prompt, len(matchingCalls), toolCounts)
	return MockCall{}
}

func callHasUserPrompt(call MockCall, prompt string) bool {
	for _, msg := range call.Messages {
		if msg.Role != messageModel.User {
			continue
		}
		for _, part := range msg.Parts {
			textPart, ok := part.(messageModel.TextContent)
			if ok && strings.Contains(textPart.Text, prompt) {
				return true
			}
		}
	}
	return false
}

func assertCapturedCallHasToolSnapshot(t *testing.T, call MockCall, prompt string) {
	t.Helper()

	require.NotEmpty(t, call.Tools, "expected non-empty tools in mock driver capture for prompt %q", prompt)
	require.NotEmpty(t, call.ToolNames, "expected non-empty tool names in mock driver capture for prompt %q", prompt)

	nonEmptyToolNames := make([]string, 0, len(call.ToolNames))
	for _, toolName := range call.ToolNames {
		if strings.TrimSpace(toolName) != "" {
			nonEmptyToolNames = append(nonEmptyToolNames, toolName)
		}
	}
	require.NotEmpty(t, nonEmptyToolNames, "expected at least one non-empty tool name for prompt %q", prompt)
}
