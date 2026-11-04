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

// tricklingDriver keeps its stream visibly alive with events that carry no
// output — the shape that held two gpt-6-luna turns open for 15 minutes with
// zero tokens while every inter-event guard kept resetting.
type tricklingDriver struct {
	mockLLMDriverForIdempotency
	every   time.Duration
	stopped chan struct{}
	// keepaliveOnce marks the stream live once, as a transport does for a
	// keepalive read inside an open block, and then never again.
	keepaliveOnce bool
}

func (d *tricklingDriver) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	events := make(chan llm.DriverEvent)
	go func() {
		defer close(d.stopped)
		defer close(events)
		if d.keepaliveOnce {
			llm.StreamLivenessFrom(ctx).Touch()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d.every):
			}
			select {
			case events <- llm.DriverEvent{Type: llm.EventThinkingStart}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return events
}

func TestCallLLM_NoFirstTokenFailsForRetry(t *testing.T) {
	t.Run("no keepalive", func(t *testing.T) {
		testCallLLMNoFirstTokenFailsForRetry(t, false)
	})
	t.Run("a keepalive that went stale", func(t *testing.T) {
		// One liveness mark at the start must not disarm the guard for the
		// rest of the stream.
		testCallLLMNoFirstTokenFailsForRetry(t, true)
	})
}

func testCallLLMNoFirstTokenFailsForRetry(t *testing.T, keepaliveOnce bool) {
	// The progress guard is far away and keeps being reset: only the
	// first-token deadline can end this stream.
	llm.SetStreamTimeoutsForTest(t, llm.StreamTimeouts{Progress: time.Minute, FirstToken: 150 * time.Millisecond})
	driver := &tricklingDriver{every: 10 * time.Millisecond, stopped: make(chan struct{}), keepaliveOnce: keepaliveOnce}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	activityInstance := NewCallLLMActivity(h.Repo(), &captureHub{}, nil, &staticConfigProvider{}, resolver, nil)

	run := func(activityCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		boundedCtx, cancel := context.WithTimeout(activityCtx, 5*time.Second)
		defer cancel()
		return activityInstance.Execute(boundedCtx, input)
	}
	h.env.RegisterActivity(run)
	_, err := h.env.ExecuteActivity(run, callLLMInput(chat.ID, chat.ID, "mock-model"))
	require.Error(t, err, "a stream with no output token must fail so the turn is retried")
	assert.Contains(t, err.Error(), "first-token timeout")
	select {
	case <-driver.stopped:
	case <-time.After(time.Second):
		t.Fatal("first-token timeout did not cancel the provider stream")
	}
}

func TestFirstTokenGuard(t *testing.T) {
	t.Parallel()

	t.Run("an output token disarms it for good", func(t *testing.T) {
		t.Parallel()
		g := newFirstTokenGuard(20 * time.Millisecond)
		defer g.stop()
		g.observe(llm.DriverEvent{Type: llm.EventThinkingStart})
		require.NotNil(t, g.C(), "framing events are not output")
		g.observe(llm.DriverEvent{Type: llm.EventContentDelta, Content: "hi"})
		assert.Nil(t, g.C())
		g.observe(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Delay: time.Second}})
		assert.Nil(t, g.C(), "a disarmed guard stays disarmed")
	})

	t.Run("a recent keepalive defers it, a stale one does not", func(t *testing.T) {
		t.Parallel()
		g := newFirstTokenGuard(200 * time.Millisecond)
		defer g.stop()
		liveness := llm.NewStreamLiveness()
		assert.True(t, g.expired(liveness), "no keepalive was ever read")

		liveness.Touch()
		require.False(t, g.expired(liveness), "a keepalive just read inside an open block defers the deadline")
		require.NotNil(t, g.C(), "deferring keeps the guard armed")
		// The deferred deadline fires one timeout after the keepalive; with
		// no further keepalive, that mark is now stale.
		select {
		case <-g.C():
		case <-time.After(5 * time.Second):
			t.Fatal("deferred guard never fired")
		}
		assert.True(t, g.expired(liveness), "a stale keepalive must not exempt the stream")
	})

	t.Run("fires when nothing arrives", func(t *testing.T) {
		t.Parallel()
		g := newFirstTokenGuard(10 * time.Millisecond)
		defer g.stop()
		select {
		case <-g.C():
		case <-time.After(time.Second):
			t.Fatal("guard never fired")
		}
	})

	t.Run("a retry wait restarts the deadline after the backoff", func(t *testing.T) {
		t.Parallel()
		g := newFirstTokenGuard(30 * time.Millisecond)
		defer g.stop()
		g.observe(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Delay: 200 * time.Millisecond}})
		select {
		case <-g.C():
			t.Fatal("fired during the driver's backoff")
		case <-time.After(100 * time.Millisecond):
		}
	})
}
