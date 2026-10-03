// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/stopreason"
)

// countingDriver streams one fixed turn and counts how often the provider was
// actually called — the thing a yielded turn must NOT do and a continued one
// must.
type countingDriver struct {
	calls        atomic.Int32
	text         string
	finishReason message.FinishReason
}

func (d *countingDriver) Name() string { return "mock" }
func (d *countingDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}
func (d *countingDriver) ValidateKey(context.Context) error {
	return nil
}

func (d *countingDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	d.calls.Add(1)
	return &llm.DriverResponse{Content: d.text, FinishReason: d.finishReason}, nil
}

func (d *countingDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	d.calls.Add(1)
	ch := make(chan llm.DriverEvent, 3)
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	if d.text != "" {
		ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: d.text}
	}
	ch <- llm.DriverEvent{
		Type:     llm.EventComplete,
		Response: &llm.DriverResponse{Content: d.text, FinishReason: d.finishReason, Usage: llm.TokenUsage{TokenCount: 30}},
	}
	close(ch)
	return ch
}

func countingResolver(d *countingDriver) drivers.DriverResolver {
	return func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	}
}

func withContinueTurn(input ActivityInput) ActivityInput {
	input.Node.GetCallLlm().ContinueTurn = &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}
	return input
}

// A turn the provider PAUSED leaves history ending with the assistant's own
// message. The loop that saw stop_reason "incomplete" sets continue_turn, and
// call_llm must then call the provider — otherwise the assistant-tail guard
// yields, the turn reads as done, and the agent stops right after announcing
// its next step (chat b43b41fe: "I'll inspect the local dev auth setup…", then
// nothing).
func TestCallLLM_ContinueTurnCallsProviderOnAssistantTail(t *testing.T) {
	driver := &countingDriver{text: "Checked: the dev IdP issues reliant:api tokens.", finishReason: message.FinishReasonEndTurn}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID,
		"I'll inspect the local dev auth setup before attempting the workflow again.")

	activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, countingResolver(driver), nil)

	var output CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withContinueTurn(callLLMInput(chat.ID, chat.ID, "mock-model")), &output))

	assert.EqualValues(t, 1, driver.calls.Load(),
		"continue_turn must call the provider on an assistant-tailed history, not yield")
	assert.Equal(t, "Checked: the dev IdP issues reliant:api tokens.", output.ResponseText)
	assert.Equal(t, stopreason.Done, output.StopReason)
}

// Without continue_turn the guard is unchanged: an assistant-tailed history
// yields WITHOUT calling the provider and reports done, so a thread wedged on
// that shape still recovers on the user's next message. continue_turn is an
// explicit opt-out for one turn, never a weakening of the default.
func TestCallLLM_AssistantTailStillYieldsWithoutContinueTurn(t *testing.T) {
	driver := &countingDriver{text: "should never be produced", finishReason: message.FinishReasonEndTurn}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "All done.")

	activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, countingResolver(driver), nil)

	var output CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		callLLMInput(chat.ID, chat.ID, "mock-model"), &output))

	assert.EqualValues(t, 0, driver.calls.Load(), "a yielded turn must not call the provider")
	assert.Equal(t, stopreason.Done, output.StopReason,
		"a yield is a finished turn — anything else would make the loop retry against an unchanged history")
}

// The provider's pause becomes stop_reason "incomplete" only when the turn
// produced text. A pause that produced nothing would re-send a byte-identical
// request on the next turn; it must read as "error" so the loop yields instead
// of spinning.
func TestCallLLM_PausedTurnStopReason(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"paused with text continues", "I'll run the migration next.", stopreason.Incomplete},
		{"paused with nothing stops", "", stopreason.Error},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &countingDriver{text: tc.text, finishReason: message.FinishReasonPauseTurn}

			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()

			ctx := context.Background()
			project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
			chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
			h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

			activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, countingResolver(driver), nil)

			var output CallLLMOutput
			require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
				callLLMInput(chat.ID, chat.ID, "mock-model"), &output))

			assert.Equal(t, tc.want, output.StopReason)
			assert.Equal(t, string(message.FinishReasonPauseTurn), output.FinishReason,
				"the raw provider reason stays available alongside")
		})
	}
}
