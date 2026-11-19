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
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
)

// A turn a pause cut off mid-stream is continued by its re-dispatch, not
// yielded as the agent's answer (reliant #2759, chat e1041795: a sub-agent
// streaming when its root's provider failed for good — which pauses the run —
// ended with half a reply once the user resumed). The workflow half, that the
// re-dispatch names the cut-off dispatch's id, is TestPauseCutOffTurn in
// internal/workflow/runtime.

const cutOffPartial = "I'll start by reading the"

// turnScript is one provider turn of scriptedDriver.
type turnScript int

const (
	// streamThenHang streams cutOffPartial, then waits for the turn to be
	// cancelled, as a call does when a pause lands mid-stream.
	streamThenHang turnScript = iota
	// streamThenFail streams cutOffPartial, then fails terminally, as the
	// incident's Anthropic 400 claude_code_version_too_old did.
	streamThenFail
	// answer streams a whole reply.
	answer
)

// scriptedDriver plays one turnScript per call and records the history each
// call was sent.
type scriptedDriver struct {
	mu      sync.Mutex
	scripts []turnScript
	replies []string
	sent    [][]message.Message
	// streaming is closed once a streamThenHang turn has streamed its text.
	streaming chan struct{}
}

func (d *scriptedDriver) Name() string { return "mock" }
func (d *scriptedDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}
func (d *scriptedDriver) ValidateKey(context.Context) error { return nil }
func (d *scriptedDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, errors.New("scriptedDriver only streams")
}

func (d *scriptedDriver) StreamResponse(ctx context.Context, _ []string, msgs []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	d.mu.Lock()
	call := len(d.sent)
	d.sent = append(d.sent, append([]message.Message(nil), msgs...))
	script, reply := answer, ""
	if call < len(d.scripts) {
		script = d.scripts[call]
	}
	if call < len(d.replies) {
		reply = d.replies[call]
	}
	d.mu.Unlock()

	ch := make(chan llm.DriverEvent, 4)
	switch script {
	case streamThenHang:
		ch <- llm.DriverEvent{Type: llm.EventContentStart}
		ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: cutOffPartial}
		close(d.streaming)
		go func() {
			<-ctx.Done()
			close(ch)
		}()
	case streamThenFail:
		ch <- llm.DriverEvent{Type: llm.EventContentStart}
		ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: cutOffPartial}
		ch <- llm.DriverEvent{Type: llm.EventError, Error: errors.New(
			`POST "https://api.anthropic.com/v1/messages": 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","details":{"error_code":"claude_code_version_too_old"}}}`)}
		close(ch)
	default:
		ch <- llm.DriverEvent{Type: llm.EventContentStart}
		ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: reply}
		ch <- llm.DriverEvent{
			Type:     llm.EventComplete,
			Response: &llm.DriverResponse{Content: reply, FinishReason: message.FinishReasonEndTurn, Usage: llm.TokenUsage{TokenCount: 30}},
		}
		close(ch)
	}
	return ch
}

func (d *scriptedDriver) calls() [][]message.Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]message.Message(nil), d.sent...)
}

// cutOffFixture is a chat whose thread holds one user message, and a CallLLM
// activity on a scriptedDriver.
type cutOffFixture struct {
	h        *IdempotencyTestHelper
	chatID   string
	driver   *scriptedDriver
	activity *CallLLMActivity
}

func newCutOffFixture(t *testing.T, driver *scriptedDriver) *cutOffFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "implement the feature")
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	return &cutOffFixture{
		h:        h,
		chatID:   chat.ID,
		driver:   driver,
		activity: NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, resolver, nil),
	}
}

// dispatch is one dispatch of the call_llm step at a fixed graph position,
// streaming under assistantID.
func (f *cutOffFixture) dispatch(assistantID string) ActivityInput {
	input := callLLMInput(f.chatID, f.chatID, "mock-model")
	input.Runtime.WorkflowID = f.chatID
	input.Runtime.StepID = "call_llm"
	input.Runtime.MessageIdempotencyKey = "call_llm|" + f.chatID + "|call_llm|agent_loop|iter:3"
	input.Runtime.AssistantMessageID = assistantID
	return input
}

// runCutOff runs input and cancels it once the driver has streamed its
// partial: a pause landing mid-stream.
func (f *cutOffFixture) runCutOff(t *testing.T, input ActivityInput) {
	t.Helper()
	cutOff := func(actCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		cancelCtx, cancel := context.WithCancel(actCtx)
		defer cancel()
		go func() {
			<-f.driver.streaming
			cancel()
		}()
		return f.activity.Execute(cancelCtx, input)
	}
	f.h.env.RegisterActivity(cutOff)
	_, err := f.h.env.ExecuteActivity(cutOff, input)
	require.NoError(t, err, "a turn a pause cut off returns, having persisted its partial")
}

// assistantTexts returns the text of every assistant row on the thread, by id.
func (f *cutOffFixture) assistantTexts(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	msgs, err := f.h.Repo().ListMessages(ctx, f.chatID, db.MessageListOptions{})
	require.NoError(t, err)
	out := map[string]string{}
	for _, m := range msgs {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			continue
		}
		blocks, err := f.h.Repo().ListContentBlocks(ctx, m.ID)
		require.NoError(t, err)
		var text strings.Builder
		for _, b := range blocks {
			if b.Content != nil {
				text.WriteString(*b.Content)
			}
		}
		out[m.ID] = text.String()
	}
	return out
}

// saveTurn persists a completed turn's reply the way its save_message does.
func (f *cutOffFixture) saveTurn(t *testing.T, messageID, text string) {
	t.Helper()
	_, err := threads.NewService(f.h.Repo()).SaveMessage(context.Background(), threads.SaveMessageOpts{
		ChatID:    f.chatID,
		Thread:    f.chatID,
		Role:      int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		Content:   text,
		MessageID: messageID,
	})
	require.NoError(t, err)
}

// countText counts the messages in history whose text contains s.
func countText(history []message.Message, s string) int {
	n := 0
	for _, m := range history {
		for _, tc := range m.TextContents() {
			if strings.Contains(tc.Text, s) {
				n++
				break
			}
		}
	}
	return n
}

func TestCallLLM_PauseCutOffTurnContinues(t *testing.T) {
	t.Parallel()

	const rest = "spec, then implement the feature."
	driver := &scriptedDriver{
		scripts:   []turnScript{streamThenHang, answer, answer},
		replies:   []string{"", rest, "Done: the feature is in."},
		streaming: make(chan struct{}),
	}
	f := newCutOffFixture(t, driver)

	// The pause cuts the turn off mid-stream: its partial is persisted under
	// the id it streamed under.
	cutID := uuid.NewString()
	f.runCutOff(t, f.dispatch(cutID))
	require.Equal(t, map[string]string{cutID: cutOffPartial}, f.assistantTexts(t),
		"the cut-off turn persisted its partial, under the id it streamed under")

	// The resume re-dispatches the step, naming the partial it cut off. It
	// must call the provider, not yield the partial as the agent's answer.
	redispatchID := uuid.NewString()
	redispatch := f.dispatch(redispatchID)
	redispatch.Runtime.CutOffMessageID = cutID
	var out CallLLMOutput
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, redispatch, &out))

	calls := driver.calls()
	require.Len(t, calls, 2, "the re-dispatch called the provider instead of yielding the cut-off partial")
	sent := calls[1]
	last := sent[len(sent)-1]
	assert.Equal(t, message.User, last.Role, "the request ends with the hidden continue message")
	assert.Equal(t, resumeContinueText, last.Content().String())
	assert.Equal(t, 1, countText(sent, cutOffPartial), "the partial is sent once, not duplicated")
	assert.Equal(t, rest, out.ResponseText)
	assert.Len(t, hiddenContinueRows(t, f.h, f.chatID), 1)
	f.saveTurn(t, redispatchID, out.ResponseText)

	// The conversation goes on: the user's next message gets a normal turn,
	// whose history carries the partial and its continuation exactly once.
	f.h.CreateTestUserMessageWithText(context.Background(), f.chatID, f.chatID, "thanks, now add tests")
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, f.dispatch(uuid.NewString()), &out))
	calls = driver.calls()
	require.Len(t, calls, 3, "the next user turn called the provider")
	sent = calls[2]
	assert.Equal(t, message.User, sent[len(sent)-1].Role)
	assert.Equal(t, 1, countText(sent, cutOffPartial), "the cut-off partial appears once in later history")
	assert.Equal(t, 1, countText(sent, rest), "its continuation appears once")
	assert.Equal(t, "Done: the feature is in.", out.ResponseText)
	assert.Len(t, f.assistantTexts(t), 2, "one row for the partial and one for its continuation; nothing duplicated")
}

// The id match is what keeps the continue exact: a pause that cut a dispatch
// off before it streamed anything persisted nothing, so the tail is some other
// assistant message — a finished answer — and the re-dispatch still yields.
func TestCallLLM_PauseCutOffTurnLeavesAnotherTailAlone(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{scripts: []turnScript{answer}, replies: []string{"never"}}
	f := newCutOffFixture(t, driver)
	saveTextOnlyAssistantMessage(t, f.h, f.chatID, f.chatID, "All done.")

	redispatch := f.dispatch(uuid.NewString())
	redispatch.Runtime.CutOffMessageID = uuid.NewString()
	var out CallLLMOutput
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, redispatch, &out))
	assert.Empty(t, driver.calls(), "a finished answer at the tail still yields")
	assert.Empty(t, hiddenContinueRows(t, f.h, f.chatID))
}

// The path the issue first suspected does not leave a tail: a provider that
// fails terminally after streaming text fails the turn and persists nothing,
// so the turn that follows calls the provider on the user's message.
func TestCallLLM_ProviderFailureMidStreamLeavesNoAssistantTail(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{
		scripts: []turnScript{streamThenFail, answer},
		replies: []string{"", "Here is the feature."},
	}
	f := newCutOffFixture(t, driver)

	var out CallLLMOutput
	err := f.h.ExecuteActivity(f.activity.Execute, f.dispatch(uuid.NewString()), &out)
	require.Error(t, err, "a terminal provider failure fails the turn")
	assert.Empty(t, f.assistantTexts(t), "a failed turn persists no partial for the next turn to find")

	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, f.dispatch(uuid.NewString()), &out))
	calls := driver.calls()
	require.Len(t, calls, 2, "the next turn called the provider rather than yielding")
	sent := calls[1]
	assert.Equal(t, message.User, sent[len(sent)-1].Role, "the next turn's history ends with the user's message")
	assert.Zero(t, countText(sent, cutOffPartial), "the failed turn's streamed text is not in history")
	assert.Equal(t, "Here is the feature.", out.ResponseText)
}
