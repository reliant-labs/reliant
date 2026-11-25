// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/rowreads"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

func TestStreamStalledError_OneSentenceThenMarkerThenDiagnosis(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		stall    *llm.StreamStallError
		sentence string
	}{
		"no block open": {
			stall:    &llm.StreamStallError{Phase: llm.StallAwaitingContent, Timeout: 5 * time.Minute},
			sentence: "AI provider stopped responding: claude-5.5-sonnet on anthropic accepted the request but sent no output for 5m0s (usually an exhausted subscription or an overloaded provider)",
		},
		"block open": {
			stall:    &llm.StreamStallError{Phase: llm.StallMidBlock, Timeout: 30 * time.Minute},
			sentence: "AI provider stopped responding: claude-5.5-sonnet on anthropic went silent partway through its reply for 30m0s",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := newStreamStalledError(context.Background(), tc.stall, "anthropic", "claude-5.5-sonnet")

			assert.Equal(t, tc.sentence+" [RELIANT_PROVIDER_STREAM_STALLED:anthropic]: "+tc.stall.Error(), err.Error())
			assert.Equal(t, tc.sentence, chatmarkers.ProviderStreamStalledSummary(err.Error()),
				"the UI summary is exactly the sentence")
			assert.NotContains(t, err.Error(), "retrying",
				"the same text heads the paused row; what happens next is the surface's to say")
			assert.ErrorIs(t, err, llm.ErrStreamContentStalled)
			var stall *llm.StreamStallError
			require.ErrorAs(t, err, &stall)
			assert.Equal(t, tc.stall.Phase, stall.Phase)
		})
	}
}

// The retry schedule of a stalled turn. Every attempt has already cost a whole
// stall deadline, so the step's blanket policy — five attempts, 1-8s apart —
// spent 25 minutes on chat 622675c2's turn before pausing. A turn that sent
// nothing at all is spaced out and given three attempts; one that wedged with
// a block open, whose deadline is up to 30 minutes, gets one more.
func TestStreamStalledError_RetrySchedule(t *testing.T) {
	t.Parallel()
	awaiting := &llm.StreamStallError{Phase: llm.StallAwaitingContent, Timeout: 5 * time.Minute}
	midBlock := &llm.StreamStallError{Phase: llm.StallMidBlock, Timeout: 30 * time.Minute}
	for _, tc := range []struct {
		stall   *llm.StreamStallError
		attempt int32
		delay   time.Duration
		retry   bool
	}{
		{awaiting, 1, 15 * time.Second, true},
		{awaiting, 2, 30 * time.Second, true},
		{awaiting, 3, 0, false},
		{awaiting, 5, 0, false},
		{midBlock, 1, 5 * time.Second, true},
		{midBlock, 2, 0, false},
	} {
		err := &streamStalledError{stall: tc.stall, provider: "anthropic", model: "m", attempt: tc.attempt}
		delay, retry := err.RetryAfter()
		assert.Equal(t, tc.retry, retry, "%s attempt %d", tc.stall.Phase, tc.attempt)
		assert.Equal(t, tc.delay, delay, "%s attempt %d", tc.stall.Phase, tc.attempt)
	}
	assert.Equal(t, "ProviderStreamStalled", (&streamStalledError{stall: awaiting}).TemporalErrorType())
}

// stallingDriver ends its stream the way the transport does when a stall
// deadline fires.
type stallingDriver struct {
	mockLLMDriverForIdempotency
	stall *llm.StreamStallError
}

func (d *stallingDriver) StreamResponse(context.Context, []string, []message.Message, []tools.Tool) <-chan llm.DriverEvent {
	events := make(chan llm.DriverEvent, 1)
	events <- llm.DriverEvent{Type: llm.EventError, Error: d.stall}
	close(events)
	return events
}

// What CallLLM returns for a stall: the one sentence and marker, the
// transport's diagnosis once, and none of the old "LLM streaming error"
// wrapping around it.
func TestCallLLM_StalledStreamFailsWithOneCleanSentence(t *testing.T) {
	t.Parallel()

	driver := &stallingDriver{stall: &llm.StreamStallError{Phase: llm.StallMidBlock, Timeout: 30 * time.Minute}}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: rowreads.WithRepo(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: &captureHub{}, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: resolver, MCPBinder: nil})

	// Capture the error as CallLLM returns it, before the test environment's
	// failure converter (production flattens it first, in wrapActivity).
	var returned error
	run := func(activityCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		output, err := activityInstance.Execute(activityCtx, input)
		returned = err
		return output, err
	}
	h.env.RegisterActivity(run)
	_, err := h.env.ExecuteActivity(run, callLLMInput(chat.ID, chat.ID, "mock-model"))
	require.Error(t, err)
	require.Error(t, returned)

	msg := returned.Error()
	assert.Contains(t, msg, "AI provider stopped responding: mock-model on ")
	assert.Contains(t, msg, "went silent partway through its reply for 30m0s")
	assert.Contains(t, msg, "[RELIANT_PROVIDER_STREAM_STALLED:")
	assert.NotContains(t, msg, "LLM streaming error")
	assert.Equal(t, 1, strings.Count(msg, "a content block stayed open for 30m0s"), msg)

	var stalled *streamStalledError
	require.ErrorAs(t, returned, &stalled, "the activity boundary schedules the retry from this")
	assert.ErrorIs(t, returned, llm.ErrStreamContentStalled)
}
