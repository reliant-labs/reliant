package e2e

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

func TestActiveWorkflowRunning_SendMessageAppliesPresetAndParamUpdates(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Keep the first turn active long enough to hit the running SendMessage path.
	h.MockLLM.SetResponseWithToolCall(
		"Let me run a command first.",
		"Bash",
		map[string]interface{}{"command": "echo running-path"},
	)
	h.MockLLM.AddResponse(MockResponse{Text: "First turn complete."})
	h.MockTools.On("Bash", MockToolResponse{Result: "running-path", Success: true, Delay: 1200 * time.Millisecond})

	chatID := h.StartAgentWorkflowViaGRPC(t, "start")
	h.WaitForMessages(t, chatID, 2) // user + assistant(tool call)

	workflowID, runID := mustGetWorkflowExecution(t, h, chatID)
	require.Eventually(t, func() bool {
		wf, err := h.DB.GetWorkflow(context.Background(), workflowID)
		return err == nil && wf.Status == db.WorkflowStatusRunning
	}, 2*time.Second, 50*time.Millisecond)

	baselineCalls := len(h.MockLLM.GetCalls())

	h.SendMessageViaGRPC(
		t,
		chatID,
		"continue",
		WithSendSelectedPreset("default", "general"),
		WithSendWorkflowParam("temperature", 0.42),
		WithSendWorkflowParam("tools", []interface{}{}), // regression: should not wipe preset defaults
	)

	// Validate effective workflow inputs from the active execution.
	var observedInputs map[string]interface{}
	require.Eventually(t, func() bool {
		inputs, ok := queryWorkflowInputs(h, workflowID, runID)
		if !ok {
			return false
		}
		observedInputs = inputs

		temperatureValue, hasTemperature := inputs["temperature"].(float64)
		toolsValue, hasTools := inputs["tools"].([]interface{})

		if !hasTemperature || !hasTools || len(toolsValue) == 0 {
			return false
		}
		return temperatureValue > 0.41 && temperatureValue < 0.43
	}, 3*time.Second, 50*time.Millisecond)

	require.InDelta(t, 0.42, observedInputs["temperature"].(float64), 0.0001)
	require.NotEmpty(t, observedInputs["tools"].([]interface{}))

	h.WaitForWorkflowComplete(t, chatID)

	calls := h.MockLLM.GetCalls()
	require.Greater(t, len(calls), baselineCalls, "send message on active running path should produce additional LLM calls")
	require.NotEmpty(t, calls[len(calls)-1].Tools, "tools should remain available after active running-path updates")

	chat := mustGetChat(t, h, chatID)
	require.Equal(t, "general", chat.SelectedPresets["default"])
}

func TestActiveWorkflowPaused_SendMessageAppliesPresetAndParamUpdates(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Keep tool execution in-flight so we can pause and then exercise the paused SendMessage path.
	h.MockLLM.SetResponseWithToolCall(
		"Let me run a command first.",
		"Bash",
		map[string]interface{}{"command": "echo paused-path"},
	)
	h.MockLLM.AddResponse(MockResponse{Text: "Paused turn complete."})
	h.MockTools.On("Bash", MockToolResponse{Result: "paused-path", Success: true, Delay: 1200 * time.Millisecond})

	chatID := h.StartAgentWorkflowViaGRPC(t, "start")
	h.WaitForMessages(t, chatID, 2)

	workflowID, runID := mustGetWorkflowExecution(t, h, chatID)

	authCtx := context.WithValue(context.Background(), auth.UserIDContextKey, h.UserID())
	_, err := h.chatService.PauseChat(authCtx, connect.NewRequest(&reliantv1.PauseChatRequest{ChatId: chatID}))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		wf, workflowErr := h.DB.GetWorkflow(context.Background(), workflowID)
		return workflowErr == nil && wf.Status == db.WorkflowStatusPaused
	}, 3*time.Second, 50*time.Millisecond)

	baselineCalls := len(h.MockLLM.GetCalls())

	h.SendMessageViaGRPC(
		t,
		chatID,
		"resume and continue",
		WithSendSelectedPreset("default", "general"),
		WithSendWorkflowParam("temperature", 0.33),
		WithSendWorkflowParam("tools", []interface{}{}), // regression: empty tools should not remove preset defaults
	)

	var observedInputs map[string]interface{}
	require.Eventually(t, func() bool {
		inputs, ok := queryWorkflowInputs(h, workflowID, runID)
		if !ok {
			return false
		}
		observedInputs = inputs

		temperatureValue, hasTemperature := inputs["temperature"].(float64)
		toolsValue, hasTools := inputs["tools"].([]interface{})

		if !hasTemperature || !hasTools || len(toolsValue) == 0 {
			return false
		}
		return temperatureValue > 0.32 && temperatureValue < 0.34
	}, 4*time.Second, 50*time.Millisecond)

	require.InDelta(t, 0.33, observedInputs["temperature"].(float64), 0.0001)
	require.NotEmpty(t, observedInputs["tools"].([]interface{}))

	h.WaitForWorkflowComplete(t, chatID)

	calls := h.MockLLM.GetCalls()
	require.Greater(t, len(calls), baselineCalls, "send message on active paused-path should produce additional LLM calls")
	require.NotEmpty(t, calls[len(calls)-1].Tools, "tools should remain available after active paused-path updates")

	chat := mustGetChat(t, h, chatID)
	require.Equal(t, "general", chat.SelectedPresets["default"])
}

func mustGetWorkflowExecution(t *testing.T, h *TestHarness, chatID string) (string, string) {
	t.Helper()

	chat := mustGetChat(t, h, chatID)
	require.NotNil(t, chat.WorkflowID)
	require.NotNil(t, chat.RunID)

	return *chat.WorkflowID, *chat.RunID
}

func mustGetChat(t *testing.T, h *TestHarness, chatID string) *db.Chat {
	t.Helper()

	chat, err := h.DB.GetChat(context.Background(), chatID)
	require.NoError(t, err)
	require.NotNil(t, chat)
	return chat
}

func queryWorkflowInputs(h *TestHarness, workflowID, runID string) (map[string]interface{}, bool) {
	_ = runID // active path may reset run ids; query latest execution by workflow ID
	queryResp, err := h.TemporalClient.QueryWorkflow(context.Background(), workflowID, "", "get_workflow_inputs")
	if err != nil {
		return nil, false
	}

	var inputs map[string]interface{}
	if err := queryResp.Get(&inputs); err != nil {
		return nil, false
	}

	return inputs, true
}
