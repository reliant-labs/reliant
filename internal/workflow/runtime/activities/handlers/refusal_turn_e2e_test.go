// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusalMockDriver is the provider answering exactly as Anthropic did on chat
// 58cc003f: stop_reason "refusal", and not one content block with it.
type refusalMockDriver struct {
	finishReason message.FinishReason
}

func (m *refusalMockDriver) Name() string                          { return "mock" }
func (m *refusalMockDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (m *refusalMockDriver) ValidateKey(ctx context.Context) error { return nil }

func (m *refusalMockDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{FinishReason: m.finishReason}, nil
}

func (m *refusalMockDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{
		Type: llm.EventComplete,
		Response: &llm.DriverResponse{
			// No Content, no Thinking, no ToolCalls. This is the whole bug.
			FinishReason: m.finishReason,
			Usage:        llm.TokenUsage{TokenCount: 72176},
		},
	}
	close(ch)
	return ch
}

func buildPlainCallLLMInput(chatID string) ActivityInput {
	return ActivityInput{
		Runtime: RuntimeContext{
			ChatID:     chatID,
			Thread:     chatID,
			WorkflowID: "test-wf",
			StepID:     "call_llm",
		},
		Node: &reliantv1.Node{
			Id:   "call_llm",
			Type: "call_llm",
			Args: &reliantv1.Node_CallLlm{CallLlm: &reliantv1.CallLLMArgs{
				SystemPrompt: &reliantv1.CelString{
					Value: &reliantv1.CelString_Literal{Literal: "You are an agent."},
				},
				Model: &reliantv1.CelModelSelector{
					Value: &reliantv1.CelModelSelector_Literal{
						Literal: &reliantv1.ModelSelector{Id: "mock-model"},
					},
				},
			}},
		},
	}
}

// The whole chain, end to end: a provider turn with nothing in it must reach
// the user as an error, must NOT be written into the transcript as something
// the assistant said, and must still tell a parent agent what happened.
//
// Reproduces chat 58cc003f. Before the fix this activity returned success with
// an empty message, SaveMessage refused the blockless row, the step executor
// swallowed that refusal, the loop exited, and the workflow reported
// "Completed successfully" — with nothing on screen either way.
func TestCallLLM_RefusalWithNoContent_ReportsAnError(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-refusal", "user-refusal")
	chat := h.CreateTestChat(ctx, "chat-refusal", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	activity := NewCallLLMActivity(
		h.Repo(),
		nil,
		nil,
		&staticConfigProvider{},
		func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return &refusalMockDriver{finishReason: message.FinishReasonRefusal}, nil
		},
		nil,
	)

	var output reliantv1.CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, buildPlainCallLLMInput(chat.ID), &output),
		"a refusal is a complete turn, not an activity failure — failing here would put the "+
			"retry ladder on a turn that can only reproduce itself")

	// 1. Nothing is fabricated into the transcript. Message.Text is what
	//    save_message persists as assistant speech, and the model said nothing.
	assert.Empty(t, output.GetMessage().GetText(),
		"a refusal must not be persisted as words the assistant said — they would enter provider history")
	assert.Empty(t, output.GetToolCalls())

	// 2. A parent agent reading a spawned child's response_text still learns
	//    what happened; an empty one tells it nothing.
	require.NotEmpty(t, output.GetResponseText())
	assert.Contains(t, strings.ToLower(output.GetResponseText()), "safety")

	// 3. The user sees an error.
	updates, err := h.Repo().GetLatestNonMessageUpdatesPerEntity(ctx, chat.ID)
	require.NoError(t, err)

	var errPayload map[string]interface{}
	for _, u := range updates {
		var p map[string]interface{}
		if json.Unmarshal([]byte(u.Data), &p) != nil {
			continue
		}
		if p["update_type"] == "error" {
			errPayload = p
			break
		}
	}
	require.NotNil(t, errPayload,
		"the turn produced nothing and no error was written — this is the silent stop, unchanged")
	assert.Equal(t, "provider_empty_response", errPayload["activity_type"])
	assert.Equal(t, chat.ID, errPayload["thread"])
	summary, _ := errPayload["error_summary"].(string)
	assert.Contains(t, strings.ToLower(summary), "declined")
}
