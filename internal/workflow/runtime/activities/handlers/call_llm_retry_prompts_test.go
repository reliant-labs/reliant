// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// flakyConnectionDriver fails the first stream the way a dropped HTTP/2
// connection does and answers every later one, recording the system prompts
// each attempt sent.
type flakyConnectionDriver struct {
	mu      sync.Mutex
	prompts [][]string
}

func (d *flakyConnectionDriver) Name() string                          { return "mock" }
func (d *flakyConnectionDriver) Model() models.Model                   { return models.Model{ID: "mock-model"} }
func (d *flakyConnectionDriver) ValidateKey(ctx context.Context) error { return nil }

func (d *flakyConnectionDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: "ok", FinishReason: message.FinishReasonEndTurn}, nil
}

func (d *flakyConnectionDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, availableTools []tools.Tool) <-chan llm.DriverEvent {
	d.mu.Lock()
	d.prompts = append(d.prompts, append([]string(nil), prompts...))
	first := len(d.prompts) == 1
	d.mu.Unlock()

	ch := make(chan llm.DriverEvent, 1)
	if first {
		ch <- llm.DriverEvent{Type: llm.EventError, Error: errors.New("http2: client connection lost")}
	} else {
		ch <- llm.DriverEvent{Type: llm.EventComplete, Response: &llm.DriverResponse{
			Content:      "done",
			FinishReason: message.FinishReasonEndTurn,
		}}
	}
	close(ch)
	return ch
}

func (d *flakyConnectionDriver) sentPrompts() [][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]string(nil), d.prompts...)
}

// callLLMWithRetries is the smallest workflow that puts CallLLM on a real
// Temporal retry ladder, so the second run genuinely is attempt 2.
func callLLMWithRetries(ctx workflow.Context, input ActivityInput) (*reliantv1.CallLLMOutput, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Millisecond,
			BackoffCoefficient: 1,
			MaximumAttempts:    3,
		},
	})
	var out reliantv1.CallLLMOutput
	err := workflow.ExecuteActivity(ctx, "CallLLM", input).Get(ctx, &out)
	return &out, err
}

// A retry after a dropped connection must send the model exactly what the
// first attempt sent.
//
// It used to append a reminder to the first system prompt telling the model
// its previous response was "too long or got cut off" and to use fewer tool
// calls. That was never true of the failures that actually reach a retry:
// every injection in the dev logs followed a network or API error, five of
// them in one chat after the machine slept mid-stream. It steered the model
// with false advice, and it changed the first system block on every retry,
// which invalidates the provider's prompt cache exactly when the turn is
// already being re-paid.
func TestCallLLM_RetryAfterDroppedConnection_SendsIdenticalPrompts(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-retry-prompts", "user-retry-prompts")
	chat := h.CreateTestChat(ctx, "chat-retry-prompts", project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)

	driver := &flakyConnectionDriver{}
	callLLM := NewCallLLMActivity(
		h.Repo(),
		nil,
		nil,
		&staticConfigProvider{},
		func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
			return driver, nil
		},
		nil,
	)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(callLLMWithRetries)
	env.RegisterActivityWithOptions(callLLM.Execute, activity.RegisterOptions{Name: "CallLLM"})

	env.ExecuteWorkflow(callLLMWithRetries, buildPlainCallLLMInput(chat.ID))
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "the second attempt answers, so the turn must succeed")

	sent := driver.sentPrompts()
	require.Len(t, sent, 2, "attempt 1 drops the connection, attempt 2 answers")
	assert.Equal(t, sent[0], sent[1],
		"a retry must not rewrite the system prompts: the failure was the network, not the model")
	for i, prompt := range sent[1] {
		assert.NotContains(t, prompt, "retry attempt", "system prompt %d", i)
		assert.NotContains(t, prompt, "fewer tool calls", "system prompt %d", i)
	}
}
