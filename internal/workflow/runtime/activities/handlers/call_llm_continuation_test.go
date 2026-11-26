// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/store"

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
	"github.com/reliant-labs/reliant/internal/workflow/stopreason"
)

// --- planContinuation: the policy, from history alone ---

func assistantText(text string) message.Message {
	return message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func userText(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func nudge(kind string) message.Message {
	text, _ := continuationNudgeText(kind)
	m := userText(text)
	m.Hidden = true
	return m
}

func TestPlanContinuation(t *testing.T) {
	t.Parallel()

	toolTurn := message.Message{Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "c1", Name: "bash", Input: `{}`},
	}}
	toolResult := message.Message{Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "c1", Name: "bash", Content: "ok"},
	}}

	cases := []struct {
		name      string
		stop      string
		premature llm.PrematureStop
		requested string
		history   []message.Message
		want      string
	}{
		{
			name:    "first truncation continues",
			stop:    stopreason.Truncated,
			history: []message.Message{userText("refactor it")},
			want:    continuationTruncated,
		},
		{
			name:      "third truncation in a row still continues",
			stop:      stopreason.Truncated,
			requested: continuationTruncated,
			history: []message.Message{userText("refactor it"),
				assistantText("a"), nudge(continuationTruncated),
				assistantText("b"), nudge(continuationTruncated)},
			want: continuationTruncated,
		},
		{
			name:      "past the cap the turn ends truncated",
			stop:      stopreason.Truncated,
			requested: continuationTruncated,
			history: []message.Message{userText("refactor it"),
				assistantText("a"), nudge(continuationTruncated),
				assistantText("b"), nudge(continuationTruncated),
				assistantText("c"), nudge(continuationTruncated)},
			want: "",
		},
		{
			name: "a tool round resets the truncation count",
			stop: stopreason.Truncated,
			history: []message.Message{userText("refactor it"),
				assistantText("a"), nudge(continuationTruncated),
				assistantText("b"), nudge(continuationTruncated),
				assistantText("c"), nudge(continuationTruncated),
				toolTurn, toolResult},
			want: continuationTruncated,
		},
		{
			name: "a user message that is not ours resets the count",
			stop: stopreason.Truncated,
			history: []message.Message{
				assistantText("a"), nudge(continuationTruncated),
				assistantText("b"), nudge(continuationTruncated),
				assistantText("c"), nudge(continuationTruncated),
				userText("keep going")},
			want: continuationTruncated,
		},
		{
			name:      "first premature commentary stop is retried",
			stop:      stopreason.Done,
			premature: llm.PrematureStopCommentary,
			history:   []message.Message{userText("fix the auth bug")},
			want:      continuationCommentary,
		},
		{
			name:      "first reasoning-only stop is retried",
			stop:      stopreason.Done,
			premature: llm.PrematureStopReasoningOnly,
			history:   []message.Message{userText("fix the auth bug")},
			want:      continuationReasoningOnly,
		},
		{
			name:      "the retry stopping prematurely again ends the turn",
			stop:      stopreason.Done,
			premature: llm.PrematureStopCommentary,
			requested: continuationCommentary,
			history: []message.Message{userText("fix the auth bug"),
				assistantText("I'll inspect the auth setup."), nudge(continuationCommentary)},
			want: "",
		},
		{
			name:      "the retry is spent across kinds",
			stop:      stopreason.Done,
			premature: llm.PrematureStopReasoningOnly,
			requested: continuationCommentary,
			history: []message.Message{userText("fix the auth bug"),
				assistantText("I'll inspect the auth setup."), nudge(continuationCommentary)},
			want: "",
		},
		{
			name:      "a spent retry stays spent through a truncation continuation",
			stop:      stopreason.Done,
			premature: llm.PrematureStopCommentary,
			requested: continuationTruncated,
			history: []message.Message{userText("fix the auth bug"),
				assistantText("I'll inspect the auth setup."), nudge(continuationCommentary),
				assistantText("long..."), nudge(continuationTruncated)},
			want: "",
		},
		{
			name:      "a tool round earns a fresh retry",
			stop:      stopreason.Done,
			premature: llm.PrematureStopCommentary,
			history: []message.Message{userText("fix the auth bug"),
				assistantText("I'll inspect the auth setup."), nudge(continuationCommentary),
				toolTurn, toolResult},
			want: continuationCommentary,
		},
		{
			name:    "an ordinary finished turn is not continued",
			stop:    stopreason.Done,
			history: []message.Message{userText("hi")},
			want:    "",
		},
		{
			name:      "a premature hint on a tool turn is ignored",
			stop:      stopreason.ToolUse,
			premature: llm.PrematureStopCommentary,
			history:   []message.Message{userText("hi")},
			want:      "",
		},
		{
			name:    "a paused turn is the loop's own business",
			stop:    stopreason.Incomplete,
			history: []message.Message{userText("hi")},
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planContinuation(tc.stop, tc.premature, tc.requested, tc.history)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRequestedContinuation(t *testing.T) {
	t.Parallel()

	args := func(v string) *reliantv1.CallLLMArgs {
		return &reliantv1.CallLLMArgs{Continuation: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: v}}}
	}
	assert.Equal(t, continuationTruncated, requestedContinuation(args("truncated")))
	assert.Equal(t, continuationCommentary, requestedContinuation(args("commentary")))
	assert.Equal(t, "", requestedContinuation(args("some_future_kind")), "an unknown kind is ignored")
	assert.Equal(t, "", requestedContinuation(&reliantv1.CallLLMArgs{}), "unset is the old behavior")
}

// --- the activity: the provider is really called, with the nudge ---

// turnScriptDriver serves one scripted turn per call and records the history
// each call was sent.
type turnScriptDriver struct {
	mu    sync.Mutex
	turns []llm.DriverResponse
	sent  [][]message.Message
}

func (d *turnScriptDriver) Name() string { return "mock" }
func (d *turnScriptDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}
func (d *turnScriptDriver) ValidateKey(context.Context) error { return nil }

func (d *turnScriptDriver) next(history []message.Message) llm.DriverResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, append([]message.Message(nil), history...))
	turn := d.turns[0]
	if len(d.turns) > 1 {
		d.turns = d.turns[1:]
	}
	return turn
}

func (d *turnScriptDriver) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sent)
}

func (d *turnScriptDriver) lastSent() []message.Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sent[len(d.sent)-1]
}

func (d *turnScriptDriver) SendMessages(_ context.Context, _ []string, history []message.Message, _ []tools.Tool) (*llm.DriverResponse, error) {
	turn := d.next(history)
	return &turn, nil
}

func (d *turnScriptDriver) StreamResponse(_ context.Context, _ []string, history []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	turn := d.next(history)
	turn.Usage = llm.TokenUsage{TokenCount: 30}
	ch := make(chan llm.DriverEvent, 3)
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	if turn.Content != "" {
		ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: turn.Content}
	}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &turn}
	close(ch)
	return ch
}

func turnScriptResolver(d *turnScriptDriver) drivers.DriverResolver {
	return func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return d, nil
	}
}

func withContinuation(input ActivityInput, kind string) ActivityInput {
	input.Node.GetCallLlm().Continuation = &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: kind}}
	return input
}

// withPosition gives each call its own idempotency key, as each loop
// iteration has in a real run.
func withPosition(input ActivityInput) ActivityInput {
	input.Runtime.MessageIdempotencyKey = "pos-" + uuid.NewString()
	return input
}

func saveHiddenNudge(t *testing.T, h *IdempotencyTestHelper, chatID, kind string) {
	t.Helper()
	text, _ := continuationNudgeText(kind)
	key := "seed-" + uuid.NewString()
	_, err := threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}).SaveMessage(context.Background(), threads.SaveMessageOpts{
		ChatID:       chatID,
		Thread:       chatID,
		Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
		Content:      text,
		ActivityID:   &key,
		DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN),
	})
	require.NoError(t, err)
}

func lastText(msgs []message.Message) (message.MessageRole, string) {
	last := msgs[len(msgs)-1]
	return last.Role, last.Content().Text
}

// A truncated turn used to end as "done": the loop re-entered, but call_llm
// found history ending with the assistant and yielded without calling the
// provider. With continuation "truncated" the next call sends the nudge and
// calls the provider; a turn truncated again asks for another continuation.
func TestCallLLM_TruncatedTurnIsContinuedWithANudge(t *testing.T) {
	t.Parallel()

	driver := &turnScriptDriver{turns: []llm.DriverResponse{
		{Content: "...and the second half of the plan is", FinishReason: message.FinishReasonMaxTokens},
	}}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "Here is the first half of the plan:")

	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: tools.WithChats(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: nil, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: turnScriptResolver(driver), MCPBinder: nil})

	var output CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withPosition(withContinuation(callLLMInput(chat.ID, chat.ID, "mock-model"), continuationTruncated)), &output))

	require.Equal(t, 1, driver.calls(), "a truncation continuation must call the provider, not yield")
	role, text := lastText(driver.lastSent())
	assert.Equal(t, message.User, role)
	assert.Equal(t, truncationNudgeText, text, "the request must end with the hidden nudge")
	assert.Equal(t, stopreason.Truncated, output.StopReason)
	assert.Equal(t, continuationTruncated, output.Continuation, "one nudge so far: under the cap, keep going")

	// The nudge is in the thread, hidden, so the next turn is well-formed
	// and counts it.
	history, err := activityInstance.loadConversationHistory(ctx, chat.ID, chat.ID, nil)
	require.NoError(t, err)
	kind, ok := isContinuationNudge(history[len(history)-1])
	require.True(t, ok, "the saved nudge must read back as a hidden continuation nudge")
	assert.Equal(t, continuationTruncated, kind)
}

// The 3rd nudged continuation that truncates again ends the run as truncated:
// no 4th continuation.
func TestCallLLM_TruncationContinuationsStopAtTheCap(t *testing.T) {
	t.Parallel()

	driver := &turnScriptDriver{turns: []llm.DriverResponse{
		{Content: "...still going", FinishReason: message.FinishReasonMaxTokens},
	}}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "part 1")
	saveHiddenNudge(t, h, chat.ID, continuationTruncated)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "part 2")
	saveHiddenNudge(t, h, chat.ID, continuationTruncated)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "part 3")

	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: tools.WithChats(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: nil, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: turnScriptResolver(driver), MCPBinder: nil})

	var output CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withPosition(withContinuation(callLLMInput(chat.ID, chat.ID, "mock-model"), continuationTruncated)), &output))

	require.Equal(t, 1, driver.calls(), "the third continuation itself still runs")
	assert.Equal(t, stopreason.Truncated, output.StopReason, "past the cap the turn ends honestly as truncated, not done")
	assert.Empty(t, output.Continuation, "three nudged continuations in a row is the cap")
}

// Mailbox delivery resets the old nudge run, but the requested continuation
// must still be nudged and counted so repeated truncation reaches the cap.
func TestCallLLM_MailboxDeliveryPreservesContinuationTracking(t *testing.T) {
	t.Parallel()

	driver := &turnScriptDriver{turns: []llm.DriverResponse{
		{Content: "...still going", FinishReason: message.FinishReasonMaxTokens},
	}}
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	for range maxTruncationContinuations {
		saveHiddenNudge(t, h, chat.ID, continuationTruncated)
		saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "partial answer")
	}
	const mailboxText = "Also check the migration."
	queueHumanMessage(t, h.Repo(), chat.ID, chat.ID, mailboxText)
	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: tools.WithChats(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: nil, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: turnScriptResolver(driver), MCPBinder: nil})

	for i := 1; i <= maxTruncationContinuations; i++ {
		var output CallLLMOutput
		require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
			withPosition(withContinuation(callLLMInput(chat.ID, chat.ID, "mock-model"), continuationTruncated)), &output))
		require.Equal(t, i, driver.calls())
		sent := driver.lastSent()
		role, text := lastText(sent)
		assert.Equal(t, message.User, role)
		assert.Equal(t, truncationNudgeText, text)
		assert.Equal(t, i, trailingNudges(sent)[continuationTruncated], "mailbox resets the old run, not the requested nudge")
		if i == 1 {
			require.GreaterOrEqual(t, len(sent), 2)
			assert.Contains(t, sent[len(sent)-2].Content().Text, mailboxText, "mailbox input precedes the nudge")
		}
		assert.Equal(t, stopreason.Truncated, output.StopReason)
		if i < maxTruncationContinuations {
			assert.Equal(t, continuationTruncated, output.Continuation)
		} else {
			assert.Empty(t, output.Continuation, "retruncation after mailbox delivery must still reach the cap")
		}
		saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, output.ResponseText)
	}
}

// nudgeSaveFailingRepo fails every save of a hidden continuation nudge (its
// activity id carries "|continuation-") and passes every other write through.
type nudgeSaveFailingRepo struct {
	*db.Repo
	failed int
	mu     sync.Mutex
}

func (r *nudgeSaveFailingRepo) ResolveThreadAmbientMemoryExclusion(ctx context.Context, chatID, threadID string, requested bool) (bool, error) {
	return tools.WithChats(r.Repo).ResolveThreadAmbientMemoryExclusion(ctx, chatID, threadID, requested)
}

func (r *nudgeSaveFailingRepo) GetChat(ctx context.Context, id string) (*db.Chat, error) {
	return chatviews.Of(r.Repo).GetChat(ctx, id)
}

func (r *nudgeSaveFailingRepo) SaveMessageAtomic(ctx context.Context, w db.AtomicMessageWrite) (*db.AtomicMessageResult, error) {
	if w.ActivityID != nil && strings.Contains(*w.ActivityID, "|continuation-") {
		r.mu.Lock()
		r.failed++
		r.mu.Unlock()
		return nil, errors.New("storage unavailable")
	}
	return r.Repo.SaveMessageAtomic(ctx, w)
}

// The saved nudges are the only record planContinuation counts toward the
// cap, so a nudge that cannot be saved must fail the attempt before the
// provider is called. Sending it unsaved would leave the count at zero, and
// a storage failure that persisted would continue a truncating turn forever.
func TestCallLLM_UnsavedNudgeFailsTheAttemptInsteadOfContinuing(t *testing.T) {
	t.Parallel()

	driver := &turnScriptDriver{turns: []llm.DriverResponse{
		{Content: "...still going", FinishReason: message.FinishReasonMaxTokens},
	}}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "part 1")

	repo := &nudgeSaveFailingRepo{Repo: h.Repo()}
	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: repo, Mailbox: store.NewAgentMessageExtStore(repo.ORM()), Threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}), Spawns: store.NewToolCallExtStore(repo.ORM()), Hub: nil, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: turnScriptResolver(driver), MCPBinder: nil})

	for range maxTruncationContinuations + 1 {
		var output CallLLMOutput
		err := h.ExecuteActivity(activityInstance.Execute,
			withPosition(withContinuation(callLLMInput(chat.ID, chat.ID, "mock-model"), continuationTruncated)), &output)
		require.Error(t, err, "an unsaved nudge must fail the attempt")
	}

	assert.Equal(t, 0, driver.calls(), "no provider call may carry a nudge the cap cannot count")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, maxTruncationContinuations+1, repo.failed, "every attempt tried to save its nudge")
}

// A Codex turn that ends on commentary with no tool call gets exactly one
// nudged retry; a retry that stops the same way ends the turn as done.
func TestCallLLM_PrematureStopIsRetriedOnce(t *testing.T) {
	t.Parallel()

	commentary := llm.DriverResponse{
		Content:       "I'll inspect the local dev auth setup next.",
		FinishReason:  message.FinishReasonEndTurn,
		Phase:         "commentary",
		PrematureStop: llm.PrematureStopCommentary,
	}
	driver := &turnScriptDriver{turns: []llm.DriverResponse{commentary}}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: tools.WithChats(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: nil, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: turnScriptResolver(driver), MCPBinder: nil})

	// Turn 1: the premature stop. stop_reason stays the provider's "done";
	// continuation carries the retry.
	var first CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withPosition(callLLMInput(chat.ID, chat.ID, "mock-model")), &first))
	assert.Equal(t, stopreason.Done, first.StopReason)
	assert.Equal(t, continuationCommentary, first.Continuation)

	// The loop's save_message persists the commentary turn.
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, commentary.Content)

	// Turn 2: the retry. The provider is called with the nudge; it stops the
	// same way, and there is no second retry.
	var second CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withPosition(withContinuation(callLLMInput(chat.ID, chat.ID, "mock-model"), first.Continuation)), &second))
	require.Equal(t, 2, driver.calls())
	role, text := lastText(driver.lastSent())
	assert.Equal(t, message.User, role)
	assert.Equal(t, commentaryNudgeText, text)
	assert.Equal(t, stopreason.Done, second.StopReason)
	assert.Empty(t, second.Continuation, "one retry per turn: the commentary stands")
}

func TestCallLLM_ReasoningOnlyStopIsRetried(t *testing.T) {
	t.Parallel()

	driver := &turnScriptDriver{turns: []llm.DriverResponse{
		{Thinking: "planning the edit", FinishReason: message.FinishReasonEndTurn, PrematureStop: llm.PrematureStopReasoningOnly},
		{Content: "Done: the edit is applied.", FinishReason: message.FinishReasonEndTurn},
	}}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: tools.WithChats(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: nil, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: turnScriptResolver(driver), MCPBinder: nil})

	var first CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withPosition(callLLMInput(chat.ID, chat.ID, "mock-model")), &first))
	assert.Equal(t, continuationReasoningOnly, first.Continuation)

	// Nothing visible was said, so nothing may have been saved: the nudge
	// still goes on, after the user's message, so the retry differs from the
	// request that produced nothing.
	var second CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withPosition(withContinuation(callLLMInput(chat.ID, chat.ID, "mock-model"), first.Continuation)), &second))
	require.Equal(t, 2, driver.calls())
	_, text := lastText(driver.lastSent())
	assert.Equal(t, reasoningOnlyNudgeText, text)
	assert.Equal(t, "Done: the edit is applied.", second.ResponseText)
	assert.Equal(t, stopreason.Done, second.StopReason)
	assert.Empty(t, second.Continuation)
}

// Providers that report no premature-stop hint (Anthropic, Gemini, Chat
// Completions) are untouched: a text-only finished turn is done, with no
// continuation, exactly as before.
func TestCallLLM_NoPrematureHintNoContinuation(t *testing.T) {
	t.Parallel()

	driver := &turnScriptDriver{turns: []llm.DriverResponse{
		{Content: "I'll inspect the auth setup next.", FinishReason: message.FinishReasonEndTurn},
	}}

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	activityInstance := NewCallLLMActivity(CallLLMDeps{Store: tools.WithChats(h.Repo()), Mailbox: store.NewAgentMessageExtStore(h.Repo().ORM()), Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: store.NewMessageExtStore(h.Repo().ORM()), Senders: store.NewThreadExtStore(h.Repo().ORM())}), Spawns: store.NewToolCallExtStore(h.Repo().ORM()), Hub: nil, ToolsFactory: nil, ConfigProvider: &staticConfigProvider{}, DriverResolver: turnScriptResolver(driver), MCPBinder: nil})

	var output CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activityInstance.Execute,
		withPosition(callLLMInput(chat.ID, chat.ID, "mock-model")), &output))
	assert.Equal(t, stopreason.Done, output.StopReason)
	assert.Empty(t, output.Continuation)
}

// A retried attempt reloads the nudge the first attempt saved; it must not be
// sent twice.
func TestAppendContinuationNudge_DedupesARetriedAttempt(t *testing.T) {
	t.Parallel()

	saved := nudge(continuationTruncated)
	saved.ID = "nudge-1"
	history := []message.Message{userText("go"), assistantText("part 1"), saved}

	got := appendContinuationNudge(history, saved)
	assert.Len(t, got, 3, "the nudge is already in history")

	fresh := nudge(continuationTruncated)
	fresh.ID = "nudge-2"
	assert.Len(t, appendContinuationNudge(history, fresh), 4, "a new iteration's nudge is appended")

	unsaved := nudge(continuationTruncated)
	assert.Len(t, appendContinuationNudge(history, unsaved), 4, "a nudge whose save failed still goes on the request")
}
