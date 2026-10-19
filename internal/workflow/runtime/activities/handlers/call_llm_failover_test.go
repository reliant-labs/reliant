// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// accountDriver is the driver of one account: it either streams a reply or is
// refused with a usage limit, and records the history it was sent.
type accountDriver struct {
	account string
	refuse  *ratelimit.UsageLimitError
	sent    *[][]message.Message
}

func (d *accountDriver) Name() string        { return "claude" }
func (d *accountDriver) Model() models.Model { return models.Model{ID: "claude-5.5-opus"} }
func (d *accountDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return nil, nil
}
func (d *accountDriver) ValidateKey(context.Context) error { return nil }
func (d *accountDriver) StreamResponse(_ context.Context, _ []string, msgs []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	*d.sent = append(*d.sent, msgs)
	ch := make(chan llm.DriverEvent, 4)
	if d.refuse != nil {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: d.refuse}
		close(ch)
		return ch
	}
	ch <- llm.DriverEvent{Type: llm.EventContentStart}
	ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "served by " + d.account}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{
		Content: "served by " + d.account, FinishReason: "end_turn", Usage: llm.TokenUsage{TokenCount: 30},
	}}
	close(ch)
	return ch
}

// End to end through the real activity on a real database: account A is
// refused, the activity benches it and reports that account B can serve; the
// retried step lands on B, says so once, and records B on the message; the
// turn after stays on B and says nothing.
func TestCallLLM_FailsOverToAnotherAccountOfTheProvider(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("acct-a", "alice@acme.com", 0), claudeAccount("acct-b", "bob@acme.com", 1)))
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	env := newRoutingEnv(t)
	var sent [][]message.Message
	reset := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	refused := map[string]bool{}
	env.routing.DriverFor = func(_ context.Context, _ string, cfg models.DriverConfig, _ models.Model, _ ...llm.DriverOption) (llm.Driver, error) {
		env.built = append(env.built, cfg.AccountID)
		d := &accountDriver{account: cfg.AccountID, sent: &sent}
		if cfg.AccountID == "acct-a" && !refused["done"] {
			d.refuse = &ratelimit.UsageLimitError{Provider: "Claude subscription", StatusCode: 429, ResetAt: reset, Window: "five_hour"}
		}
		return d, nil
	}
	activity := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil).WithAccountRouting(env.routing)

	// The thread already sits on account A (its first assignment).
	env.store.assignments[chat.ID+"|claude"] = "acct-a"

	input := callLLMInput(chat.ID, chat.ID, routeModel+"@claude")
	input.Runtime.ProviderPreference = nil

	// 1. A is refused. The activity fails with facts for the workflow.
	var out CallLLMOutput
	err := h.ExecuteActivity(activity.Execute, input, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage limit", "it is still the provider's usage-limit sentence")
	assert.Equal(t, []string{"acct-a"}, env.recorder.benched, "the refused account is benched")
	assert.Contains(t, env.recorder.observed, "acct-a", "its rate-limit observer was installed under A")

	// 2. The workflow retries the step: it resolves to B, which serves it.
	refused["done"] = true
	var retried CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, input, &retried))
	assert.Equal(t, "acct-b", retried.ProviderAccountId, "the output names the account that served it")
	assert.Equal(t, "acct-b", env.store.assignments[chat.ID+"|claude"], "and the thread moved")

	notices := systemNotices(t, h, chat.ID)
	require.Len(t, notices, 1, "exactly one notice for the move")
	assert.Contains(t, notices[0], "Claude account **alice@acme.com** reached its 5-hour limit")
	assert.Contains(t, notices[0], "Continuing on **bob@acme.com**.")

	// 3. The next turn stays on B and posts nothing.
	var next CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, input, &next))
	assert.Equal(t, "acct-b", next.ProviderAccountId)
	assert.Len(t, systemNotices(t, h, chat.ID), 1, "no second notice")
}

// When every account is limited the activity makes no provider call at all: it
// refuses the way the provider would, with the facts the wait decides on.
func TestCallLLM_AllAccountsLimitedMakesNoProviderCall(t *testing.T) {
	withAvailableDrivers(t, driversWith(claudeAccount("acct-a", "alice@acme.com", 0), claudeAccount("acct-b", "bob@acme.com", 1)))
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	env := newRoutingEnv(t)
	soon := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	env.store.bench("acct-a", time.Now().Add(3*time.Hour), "seven_day")
	env.store.bench("acct-b", soon, "five_hour")
	activity := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil).WithAccountRouting(env.routing)

	var out CallLLMOutput
	err := h.ExecuteActivity(activity.Execute, callLLMInput(chat.ID, chat.ID, routeModel+"@claude"), &out)
	require.Error(t, err)
	assert.Empty(t, env.built, "no driver was built, so nothing reached a provider")
	assert.Contains(t, err.Error(), soon.Format(time.RFC3339), "the marker carries the earliest reset for the wait")
}

func systemNotices(t *testing.T, h *IdempotencyTestHelper, chatID string) []string {
	t.Helper()
	msgs, err := h.Repo().ListMessages(context.Background(), chatID, db.MessageListOptions{})
	require.NoError(t, err)
	var out []string
	for _, m := range msgs {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		blocks, err := h.Repo().ListContentBlocks(context.Background(), m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil {
				out = append(out, *b.Content)
			}
		}
	}
	return out
}

var _ = types.FailoverPolicy{}

// Reasoning another account of an org-bound provider produced is unverifiable
// by the account now serving the thread: the request that reaches the driver
// carries none of it, and carries everything else.
func TestCallLLM_DropsReasoningAnotherAccountProduced(t *testing.T) {
	codexAcct := func(id string) models.AccountConfig {
		return models.AccountConfig{
			DriverConfig: models.DriverConfig{DriverID: "codex", AccountID: id, AccountEmail: id + "@x.com", AccountLabel: id + "@x.com", Enabled: true, APIKey: "k"},
			Status:       models.AccountAvailable,
		}
	}
	withAvailableDrivers(t, driversWith(codexAcct("cx-a"), codexAcct("cx-b")))
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	// An assistant turn account cx-a served, with an encrypted reasoning item.
	aid := "cx-a"
	svc := newThreadsFor(h)
	_, err := svc.SaveMessage(ctx, threadsSaveOpts(chat.ID, "Here is the plan.", "sealed-by-cx-a", aid))
	require.NoError(t, err)
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "continue")

	env := newRoutingEnv(t)
	var sent [][]message.Message
	env.routing.DriverFor = func(_ context.Context, _ string, cfg models.DriverConfig, _ models.Model, _ ...llm.DriverOption) (llm.Driver, error) {
		return &accountDriver{account: cfg.AccountID, sent: &sent}, nil
	}
	// The thread has moved to cx-b.
	env.store.assignments[chat.ID+"|codex"] = "cx-b"
	// A request that carries tools keeps reasoning parts; a tool-less one is
	// flattened to text first, which would hide whether the strip ran.
	activity := NewCallLLMActivity(h.Repo(), nil, tools.NewToolsFactory(&tools.ToolsOptions{Repo: h.Repo()}), &staticConfigProvider{}, nil, nil).WithAccountRouting(env.routing)

	input := callLLMInput(chat.ID, chat.ID, "gpt-5.6-sol@codex")
	input.Node.GetCallLlm().ToolsConfig = &reliantv1.ToolsConfig{PreloadedTools: celStringListLiteral([]string{"view"})}
	var out CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, input, &out))
	require.Equal(t, "cx-b", out.ProviderAccountId)

	require.Len(t, sent, 1)
	var reasoning, text int
	for _, m := range sent[0] {
		for _, p := range m.Parts {
			switch p.(type) {
			case message.ReasoningContent, message.RedactedReasoningContent:
				reasoning++
			case message.TextContent:
				text++
			}
		}
	}
	assert.Zero(t, reasoning, "cx-a's sealed reasoning is not sent to cx-b")
	assert.GreaterOrEqual(t, text, 2, "the plan and the user's messages are")
}
