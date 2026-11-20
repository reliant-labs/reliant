// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// A refusal as the Anthropic driver surfaces it. Its counts (210k > 200k) are
// close enough that they do not deepen the shrink, so the steps are the plain
// contextOverflowShrink ladder.
const overflowRefusalText = `POST "https://api.anthropic.com/v1/messages": 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`

// windowLimitedDriver refuses, as a context-window overflow, every request
// whose tool output is longer than fits, and answers the rest. It records the
// tool-output size of every request it was sent.
type windowLimitedDriver struct {
	mockLLMDriverForIdempotency
	fits    int
	refusal error

	mu   sync.Mutex
	sent []int
}

// toolOutputChars is the size of a request's content. A request with no tools
// carries its tool output flattened into text (prepareHistoryForLLM), so this
// counts every part, which the tool output dominates.
func toolOutputChars(messages []message.Message) int {
	return message.MessagesChars(messages)
}

func (d *windowLimitedDriver) StreamResponse(_ context.Context, _ []string, messages []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	size := toolOutputChars(messages)
	d.mu.Lock()
	d.sent = append(d.sent, size)
	d.mu.Unlock()

	ch := make(chan llm.DriverEvent, 3)
	if size > d.fits {
		refusal := d.refusal
		if refusal == nil {
			refusal = errors.New(overflowRefusalText)
		}
		ch <- llm.DriverEvent{Type: llm.EventError, Error: refusal}
	} else {
		ch <- llm.DriverEvent{Type: llm.EventContentStart}
		ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "answered"}
		ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{Content: "answered", FinishReason: message.FinishReasonEndTurn}}
	}
	close(ch)
	return ch
}

func (d *windowLimitedDriver) requests() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.sent...)
}

// historyWithToolOutput is a turn whose last tool result is toolChars long:
// ~150k estimated tokens at 600k characters, under the 200k-window backstop,
// so the first request goes out untrimmed.
func historyWithToolOutput(toolChars int) []message.Message {
	return []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "read the log"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "call-1", Name: "shell", Input: `{"command":"cat log"}`}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call-1", Name: "shell", Content: strings.Repeat("log line\n", toolChars/9)}}},
	}
}

// drain collects a stream's events.
func drain(ch <-chan llm.DriverEvent) (events []llm.DriverEvent, err error) {
	for ev := range ch {
		events = append(events, ev)
		if ev.Type == llm.EventError {
			err = ev.Error
		}
	}
	return events, err
}

// startRecovery prepares history the way call_llm does and streams it.
func startRecovery(driver llm.Driver, history []message.Message) <-chan llm.DriverEvent {
	recovery := newContextOverflowRecovery("chat", "anthropic", "claude-test", history, nil, nil, 0)
	prepared := prepareHistoryForLLM("chat", history, nil, nil, 0)
	return recovery.stream(context.Background(), driver, prepared)
}

func TestContextOverflowRecovery_ResendsSmallerUntilItFits(t *testing.T) {
	t.Parallel()
	history := historyWithToolOutput(600_000)
	original := toolOutputChars(history)
	// Fits only once the request is trimmed past the first step.
	driver := &windowLimitedDriver{fits: 450_000}

	events, err := drain(startRecovery(driver, history))
	require.NoError(t, err, "the refusals must not reach the consumer: a smaller request fit")

	sent := driver.requests()
	require.Len(t, sent, 3, "refused untrimmed, refused at the first step, answered at the second: %v", sent)
	assert.InDelta(t, original, sent[0], 200, "the first request is the turn as prepared (tool blocks flattened to text, nothing trimmed)")
	assert.Greater(t, sent[0], sent[1])
	assert.Greater(t, sent[1], sent[2])
	assert.LessOrEqual(t, sent[2], driver.fits)

	var text strings.Builder
	for _, ev := range events {
		text.WriteString(ev.Content)
	}
	assert.Equal(t, "answered", text.String(), "the answer is relayed once, untouched")
	assert.Equal(t, original, toolOutputChars(history),
		"the trim is the request's alone: the turn's history is unchanged")
}

// Each step trims the ORIGINAL turn, not the previous request: the second
// resend's size is a function of its own step, not of how many came before.
func TestContextOverflowRecovery_StepsTrimTheOriginal(t *testing.T) {
	t.Parallel()
	stopAtFirst := &windowLimitedDriver{fits: 520_000}
	_, err := drain(startRecovery(stopAtFirst, historyWithToolOutput(600_000)))
	require.NoError(t, err)
	stopAtSecond := &windowLimitedDriver{fits: 450_000}
	_, err = drain(startRecovery(stopAtSecond, historyWithToolOutput(600_000)))
	require.NoError(t, err)

	require.Len(t, stopAtFirst.requests(), 2)
	require.Len(t, stopAtSecond.requests(), 3)
	assert.Equal(t, stopAtFirst.requests()[1], stopAtSecond.requests()[1], "the first step is the same request either way")
}

func TestContextOverflowRecovery_GivesUpAfterTheCap(t *testing.T) {
	t.Parallel()
	driver := &windowLimitedDriver{fits: 1_000}

	_, err := drain(startRecovery(driver, historyWithToolOutput(600_000)))
	require.Error(t, err)

	assert.Len(t, driver.requests(), 1+len(contextOverflowShrink), "one request plus one per step, then stop")
	assert.ErrorIs(t, err, drivererrors.ErrContextOverflow, "the runtime classifies it as a terminal overflow by identity")
	assert.Contains(t, err.Error(), "the conversation is too long for claude-test")
	assert.Contains(t, err.Error(), "after resending it 3 time(s) with tool output trimmed")
	assert.Contains(t, err.Error(), "Start a new chat")
	assert.Contains(t, err.Error(), "prompt is too long", "the provider's own words are kept")
}

// With no tool output to trim, a resend would be the same request: fail at
// once instead of sending it again.
func TestContextOverflowRecovery_NothingToTrimFailsAtOnce(t *testing.T) {
	t.Parallel()
	driver := &windowLimitedDriver{fits: -1} // refuses everything
	history := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: strings.Repeat("x", 100_000)}}}}

	_, err := drain(startRecovery(driver, history))
	require.Error(t, err)
	assert.Len(t, driver.requests(), 1)
	assert.ErrorIs(t, err, drivererrors.ErrContextOverflow)
	assert.Contains(t, err.Error(), "no tool output left in the request to trim")
}

// Only an overflow is resent. A throttle that reads like one (a 429 whose body
// mentions a long prompt) and any other failure pass through untouched, after
// one request.
func TestContextOverflowRecovery_OtherErrorsPassThrough(t *testing.T) {
	t.Parallel()
	for name, refusal := range map[string]error{
		"429 with overflow wording": errors.New(`POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests {"type":"error","error":{"type":"rate_limit_error","message":"prompt is too long for your rate limit"}}`),
		"unrelated 400":             errors.New(`POST "https://api.openai.com/v1/responses": 400 Bad Request {"error":{"message":"Invalid value for 'model'"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			driver := &windowLimitedDriver{fits: 1_000, refusal: refusal}
			_, err := drain(startRecovery(driver, historyWithToolOutput(600_000)))
			assert.Equal(t, refusal, err, "forwarded as the driver sent it")
			assert.Len(t, driver.requests(), 1)
		})
	}
}

// outputThenOverflowDriver streams text and then fails with an overflow.
type outputThenOverflowDriver struct {
	mockLLMDriverForIdempotency
	calls int
}

func (d *outputThenOverflowDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	d.calls++
	ch := make(chan llm.DriverEvent, 2)
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "partial"}
	ch <- llm.DriverEvent{Type: llm.EventError, Error: errors.New(overflowRefusalText)}
	close(ch)
	return ch
}

// A refusal after output was relayed is not resent: the user already watched
// that output stream, and a resend would repeat it.
func TestContextOverflowRecovery_NeverResendsAfterOutput(t *testing.T) {
	t.Parallel()
	driver := &outputThenOverflowDriver{}
	events, err := drain(startRecovery(driver, historyWithToolOutput(600_000)))
	require.Error(t, err)
	assert.Equal(t, 1, driver.calls)
	assert.Equal(t, overflowRefusalText, err.Error(), "forwarded as the driver sent it")
	assert.Equal(t, "partial", events[0].Content)
}

// End to end through CallLLM: the turn's tool output overflows the provider,
// the activity resends it trimmed, and the turn completes with the answer.
func TestCallLLM_ContextOverflowIsRecoveredInTheTurn(t *testing.T) {
	t.Parallel()
	driver := &windowLimitedDriver{fits: 450_000}
	out, h, err := runCallLLMWithToolOutput(t, driver, 600_000)
	require.NoError(t, err)
	assert.Equal(t, "answered", out.GetResponseText())
	assert.Len(t, driver.requests(), 3)

	// Every request the provider answered is in the usage ledger: the two
	// refusals as failed requests, then the answer.
	callRows, _, rollupErrors, _, _, _, _ := ledgerTotals(t, h)
	assert.Equal(t, int64(3), callRows)
	assert.Equal(t, int64(2), rollupErrors)
}

// And when no resend fits, the turn fails with the explanation, still
// recognisable as an overflow.
func TestCallLLM_ContextOverflowGivesUpWithAnExplanation(t *testing.T) {
	t.Parallel()
	driver := &windowLimitedDriver{fits: 1_000}
	_, _, err := runCallLLMWithToolOutput(t, driver, 600_000)
	require.Error(t, err)
	assert.Len(t, driver.requests(), 1+len(contextOverflowShrink))
	assert.ErrorIs(t, err, drivererrors.ErrContextOverflow)
	assert.Contains(t, err.Error(), "the conversation is too long for")
}

// runCallLLMWithToolOutput runs CallLLM on a turn whose last tool result is
// toolChars long, returning the activity's own output and error.
func runCallLLMWithToolOutput(t *testing.T, driver llm.Driver, toolChars int) (*CallLLMOutput, *IdempotencyTestHelper, error) {
	t.Helper()
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	activityInstance := NewCallLLMActivity(h.Repo(), &captureHub{}, nil, &staticConfigProvider{}, resolver, nil)

	input := callLLMInput(chat.ID, chat.ID, "mock-model")
	input.Node.GetCallLlm().Messages = []*reliantv1.CallLLMMessageInput{
		{Role: "assistant", ToolCalls: []*reliantv1.ToolCallMsg{{Id: "call-1", Name: "shell", Input: `{"command":"cat log"}`}}},
		{Role: "tool", ToolResult: &reliantv1.ToolResultMsg{ToolCallId: "call-1", Name: "shell", Content: strings.Repeat("log line\n", toolChars/9)}},
	}

	var out *CallLLMOutput
	var returned error
	run := func(activityCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		out, returned = activityInstance.Execute(activityCtx, input)
		return out, returned
	}
	h.env.RegisterActivity(run)
	_, _ = h.env.ExecuteActivity(run, input)
	return out, h, returned
}
