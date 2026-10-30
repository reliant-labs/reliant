// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// usageDriver fails its first stream with a network error and answers the
// later ones with a known token split, under a configurable provider name.
type usageDriver struct {
	mu       sync.Mutex
	name     string
	streams  int
	failures int
}

func (d *usageDriver) Name() string                          { return d.name }
func (d *usageDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (d *usageDriver) ValidateKey(ctx context.Context) error { return nil }

func (d *usageDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: "ok", FinishReason: message.FinishReasonEndTurn}, nil
}

func (d *usageDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	d.mu.Lock()
	d.streams++
	fail := d.streams <= d.failures
	d.mu.Unlock()

	ch := make(chan llm.DriverEvent, 1)
	if fail {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: errors.New("http2: client connection lost; secret prompt text sk-live-123")}
	} else {
		ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{
			Content:      "a reply that must never reach the ledger",
			FinishReason: message.FinishReasonEndTurn,
			Usage: llm.TokenUsage{
				TokenCount: 999, InputTokens: 1200, OutputTokens: 340,
				CacheReadInputTokens: 800, CacheCreationInputTokens: 50,
			},
		}}
	}
	close(ch)
	return ch
}

type ledgerRow struct {
	ID, Provider, Model, AuthMode, Outcome, ErrorClass, ChatID, UserID string
	In, Out, CacheRead, CacheWrite, Latency                            int64
	Attempt                                                            int
}

func readLedger(t *testing.T, h *IdempotencyTestHelper) []ledgerRow {
	t.Helper()
	rows, err := h.DB().Query(`SELECT id, provider, model, auth_mode, outcome, error_class, chat_id, user_id,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, latency_ms, attempt
		FROM llm_call_usage ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []ledgerRow
	for rows.Next() {
		var r ledgerRow
		require.NoError(t, rows.Scan(&r.ID, &r.Provider, &r.Model, &r.AuthMode, &r.Outcome, &r.ErrorClass, &r.ChatID, &r.UserID,
			&r.In, &r.Out, &r.CacheRead, &r.CacheWrite, &r.Latency, &r.Attempt))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func runCallLLMWithRetries(t *testing.T, h *IdempotencyTestHelper, driver llm.Driver, chatID string) {
	t.Helper()
	callLLM := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{},
		func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return driver, nil
		}, nil)

	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(callLLMWithRetries)
	env.RegisterActivityWithOptions(callLLM.Execute, activity.RegisterOptions{Name: "CallLLM"})
	env.ExecuteWorkflow(callLLMWithRetries, buildPlainCallLLMInput(chatID))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}

// One model call is one ledger row, even when Temporal runs the activity twice:
// attempt 1 drops the connection, attempt 2 answers, and the row holds attempt
// 2's counts with the attempt number that produced them.
func TestCallLLM_UsageLedgerIsIdempotentUnderActivityRetry(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-ledger", "user-ledger")
	chat := h.CreateTestChat(ctx, "chat-ledger", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	driver := &usageDriver{name: "anthropic", failures: 1}
	runCallLLMWithRetries(t, h, driver, chat.ID)
	require.Equal(t, 2, driver.streams, "the test must really run the activity twice")

	rows := readLedger(t, h)
	require.Len(t, rows, 1, "a retried activity is one call: %+v", rows)
	r := rows[0]
	assert.Equal(t, "user-ledger", r.UserID)
	assert.Equal(t, chat.ID, r.ChatID)
	assert.Equal(t, "anthropic", r.Provider)
	assert.Equal(t, "mock-model", r.Model)
	assert.Equal(t, "byok", r.AuthMode)
	assert.Equal(t, "success", r.Outcome)
	assert.Empty(t, r.ErrorClass)
	assert.Equal(t, [4]int64{1200, 340, 800, 50}, [4]int64{r.In, r.Out, r.CacheRead, r.CacheWrite})
	assert.Equal(t, 2, r.Attempt, "the row says which attempt produced it")

	var calls, errorCalls int64
	require.NoError(t, h.DB().QueryRow(`SELECT COALESCE(sum(calls),0), COALESCE(sum(error_calls),0) FROM llm_call_usage_daily`).Scan(&calls, &errorCalls))
	assert.EqualValues(t, 1, calls)
	assert.EqualValues(t, 0, errorCalls, "the failed first attempt is not a second call in the rollup")
}

// A call that fails for good is recorded as a failed call with a coarse class,
// and nothing from the error or the prompt reaches the ledger.
func TestCallLLM_UsageLedgerRecordsFailureAsClassOnly(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-ledger-fail", "user-ledger-fail")
	chat := h.CreateTestChat(ctx, "chat-ledger-fail", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	driver := &usageDriver{name: "claude", failures: 99}
	callLLM := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{},
		func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return driver, nil
		}, nil)
	var out CallLLMOutput
	err := h.ExecuteActivity(callLLM.Execute, buildPlainCallLLMInput(chat.ID), &out)
	require.Error(t, err)

	rows := readLedger(t, h)
	require.Len(t, rows, 1)
	assert.Equal(t, "subscription", rows[0].AuthMode, "a Claude login is a subscription")
	assert.Equal(t, "error", rows[0].Outcome)
	assert.Equal(t, "network", rows[0].ErrorClass)

	// Every text column in the row, concatenated: no secret or prompt text.
	var dump string
	require.NoError(t, h.DB().QueryRow(`SELECT l::text FROM llm_call_usage l`).Scan(&dump))
	assert.NotContains(t, dump, "sk-live-123")
	assert.NotContains(t, dump, "secret prompt")
	assert.NotContains(t, dump, "http2")
}

func TestLLMUsageAuthMode(t *testing.T) {
	t.Parallel()
	for provider, want := range map[string]string{
		"reliant": "managed", "local": "local",
		"claude": "subscription", "codex": "subscription", "copilot": "subscription", "antigravity": "subscription",
		"anthropic": "byok", "openai": "byok", "gemini": "byok", "openrouter": "byok", "mock": "byok",
	} {
		assert.Equal(t, want, llmUsageAuthMode(provider), provider)
	}
}

// Every class the classifier returns must be one the schema's CHECK allows.
func TestLLMUsageErrorClass(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{"rate_limit": true, "usage_limit": true, "context_length": true, "auth": true,
		"timeout": true, "stalled": true, "network": true, "unknown": true}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"usage limit":      {&ratelimit.UsageLimitError{Provider: "Claude"}, "usage_limit"},
		"wrapped limit":    {fmt.Errorf("turn: %w", &ratelimit.UsageLimitError{Provider: "Codex"}), "usage_limit"},
		"progress timeout": {llm.ErrStreamProgressTimeout, "stalled"},
		"deadline":         {context.DeadlineExceeded, "timeout"},
		"429":              {errors.New("HTTP 429 too many requests"), "rate_limit"},
		"context":          {errors.New("prompt exceeds the maximum context length"), "context_length"},
		"auth":             {errors.New("401 invalid api key sk-secret"), "auth"},
		"network":          {errors.New("unexpected EOF"), "network"},
		"other":            {errors.New("boom"), "unknown"},
	} {
		got := llmUsageErrorClass(tc.err)
		assert.Equal(t, tc.want, got, name)
		assert.True(t, allowed[got], "%s: %q is not in the schema's allowed set", name, got)
	}
	assert.Empty(t, llmUsageErrorClass(nil))
}
