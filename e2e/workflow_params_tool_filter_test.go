package e2e

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	messageModel "github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

func TestRegression_CreateChatWorkflowParams_ToolFilterResolvesToNonEmptyAvailableTools(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponse("Create path response")

	prompt := "create-path unique prompt"
	chatID := h.StartAgentWorkflowViaGRPC(
		t,
		prompt,
		WithWorkflowParam("model", map[string]interface{}{"tags": []interface{}{"flagship"}}),
		WithWorkflowParam("tools", []interface{}{"tag:default"}),
		WithWorkflowParam("spawn_presets", []interface{}{"general", "researcher"}),
	)

	h.WaitForMessages(t, chatID, 2)
	h.WaitForWorkflowComplete(t, chatID)

	createCall := requireSingleToolFilterCallContainingPrompt(t, h.MockLLM.GetCalls(), prompt)
	require.NotEmpty(t, createCall.Tools, "regression: tool_filter from workflow params resolved to zero available tools on CreateChat path")
	require.True(t, hasSpawnTool(createCall.Tools), "expected spawn tool from spawn_presets to be available")
	require.True(t, hasNonSpawnTool(createCall.Tools), "expected at least one non-spawn tool from tag-based tool filter")
}

func TestRegression_ActiveSendMessageWorkflowParams_ToolFilterResolvesToNonEmptyAvailableTools(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponses("Create path response", "Follow-up send path response")

	startPrompt := "active-send create prompt"
	chatID := h.StartAgentWorkflowViaGRPC(
		t,
		startPrompt,
		WithWorkflowParam("model", map[string]interface{}{"tags": []interface{}{"flagship"}}),
		WithWorkflowParam("tools", []interface{}{"tag:default"}),
		WithWorkflowParam("spawn_presets", []interface{}{"general", "researcher"}),
	)
	h.WaitForWorkflowComplete(t, chatID)

	baselineCallCount := len(h.MockLLM.GetCalls())
	followUpPrompt := "active-send unique follow-up prompt"
	h.SendMessageViaGRPC(
		t,
		chatID,
		followUpPrompt,
		WithSendWorkflowParam("model", map[string]interface{}{"tags": []interface{}{"flagship"}}),
		WithSendWorkflowParam("tools", []interface{}{"tag:default"}),
		WithSendWorkflowParam("spawn_presets", []interface{}{"general", "researcher"}),
	)

	h.WaitForWorkflowComplete(t, chatID)

	calls := h.MockLLM.GetCalls()
	require.Greater(t, len(calls), baselineCallCount, "expected SendMessage follow-up to trigger an additional LLM call")

	followUpCall := requireSingleToolFilterCallContainingPrompt(t, calls[baselineCallCount:], followUpPrompt)
	require.NotEmpty(t, followUpCall.Tools, "regression: tool_filter from SendMessage workflow params resolved to zero available tools on follow-up path")
	require.True(t, hasSpawnTool(followUpCall.Tools), "expected spawn tool from spawn_presets to be available on follow-up SendMessage path")
	require.True(t, hasNonSpawnTool(followUpCall.Tools), "expected at least one non-spawn tool from tag-based tool filter on follow-up SendMessage path")
}

func hasSpawnTool(availableTools []tools.Tool) bool {
	for _, tool := range availableTools {
		if strings.EqualFold(tool.Name(), "spawn") {
			return true
		}
	}
	return false
}

func hasNonSpawnTool(availableTools []tools.Tool) bool {
	for _, tool := range availableTools {
		if !strings.EqualFold(tool.Name(), "spawn") {
			return true
		}
	}
	return false
}

func requireSingleToolFilterCallContainingPrompt(t *testing.T, calls []MockCall, userPrompt string) MockCall {
	t.Helper()

	matchingCalls := make([]MockCall, 0)
	for _, call := range calls {
		if toolFilterCallContainsPrompt(call, userPrompt) {
			matchingCalls = append(matchingCalls, call)
		}
	}

	require.Len(t, matchingCalls, 1, "expected exactly one LLM call containing prompt %q", userPrompt)
	return matchingCalls[0]
}

func toolFilterCallContainsPrompt(call MockCall, userPrompt string) bool {
	for _, msg := range call.Messages {
		if msg.Role != messageModel.User {
			continue
		}
		for _, part := range msg.Parts {
			textPart, ok := part.(messageModel.TextContent)
			if ok && strings.Contains(textPart.Text, userPrompt) {
				return true
			}
		}
	}
	return false
}
