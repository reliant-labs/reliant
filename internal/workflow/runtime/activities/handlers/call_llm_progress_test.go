// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider can keep its HTTP connection alive with SSE pings without ever
// delivering another driver event. Temporal heartbeats cannot detect this.
type stalledProgressDriver struct {
	mockLLMDriverForIdempotency
	initial []llm.DriverEvent
	stopped chan struct{}
}

func (d *stalledProgressDriver) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	events := make(chan llm.DriverEvent, len(d.initial))
	for _, event := range d.initial {
		events <- event
	}
	go func() {
		defer close(d.stopped)
		defer close(events)
		<-ctx.Done()
	}()
	return events
}

func TestCallLLM_StalledProgressFailsForRetry(t *testing.T) {
	t.Setenv("RELIANT_LLM_STREAM_PROGRESS_TIMEOUT", "100ms")
	for _, testCase := range []struct {
		name    string
		initial []llm.DriverEvent
	}{
		{name: "before first event"},
		{
			name: "after partial output",
			initial: []llm.DriverEvent{
				{Type: llm.EventContentStart},
				{Type: llm.EventContentDelta, Content: "partial answer"},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			driver := &stalledProgressDriver{initial: testCase.initial, stopped: make(chan struct{})}
			resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
				return driver, nil
			})
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()
			project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
			chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
			h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
			hub := &captureHub{}
			activityInstance := NewCallLLMActivity(h.Repo(), hub, nil, &staticConfigProvider{}, resolver, nil)

			// Bound a regression without letting an outer context deadline pass
			// for the stream's own, independently diagnosed progress timeout.
			run := func(activityCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
				boundedCtx, cancel := context.WithTimeout(activityCtx, 2*time.Second)
				defer cancel()
				return activityInstance.Execute(boundedCtx, input)
			}
			h.env.RegisterActivity(run)
			_, err := h.env.ExecuteActivity(run, callLLMInput(chat.ID, chat.ID, "mock-model"))
			require.Error(t, err, "a stalled stream must fail, not return a completed or interrupted turn")
			assert.Contains(t, err.Error(), "stream progress timeout")
			assert.NotContains(t, err.Error(), "heartbeat", "the activity context was healthy when the stream stalled")
			select {
			case <-driver.stopped:
			case <-time.After(time.Second):
				t.Fatal("progress timeout did not cancel the provider stream")
			}
		})
	}
}
