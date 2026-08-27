// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// heartbeatKilledStream decides whether a cancelled stream was killed by
// infrastructure or by a real instruction to stop, and getting it wrong is
// invisible in opposite directions:
//
//   - say "real" when it was spurious, and the turn settles as a completed
//     turn with zero tool calls — the agent loop reads that as "the model is
//     done" and abandons the work (chat 7da3935c, thread 5e3fe370: killed
//     1.75s into a stream with two edits applied, reported to its parent as a
//     clean finish).
//   - say "spurious" when it was real, and a user's pause or interrupt is
//     retried against their intent.
//
// The cases below mirror TestSpuriousHeartbeatCancel in the runtime package
// one-for-one. The two classifiers MUST agree: this one decides to fail the
// activity, and the wrapper's decides whether that failure becomes a retry. If
// they disagree, a turn fails without ever being retried.
func TestHeartbeatKilledStream(t *testing.T) {
	tests := []struct {
		name         string
		cause        error
		notCancelled bool
		want         bool
	}{
		{
			name:  "heartbeat RPC deadline is spurious",
			cause: context.DeadlineExceeded,
			want:  true,
		},
		{
			name:  "heartbeat transport failure is spurious",
			cause: errors.New("connection reset by peer"),
			want:  true,
		},
		{
			name:  "server-side cancellation is real",
			cause: temporal.NewCanceledError(),
			want:  false,
		},
		{
			name:  "activity pause is real",
			cause: activity.ErrActivityPaused,
			want:  false,
		},
		{
			name:  "activity reset is real",
			cause: activity.ErrActivityReset,
			want:  false,
		},
		{
			name:  "plain cancel with no distinguishing cause is treated as real",
			cause: nil,
			want:  false,
		},
		{
			name:         "a live context is not a cancellation at all",
			notCancelled: true,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if !tt.notCancelled {
				var cancel context.CancelCauseFunc
				ctx, cancel = context.WithCancelCause(context.Background())
				cancel(tt.cause)
			}

			if got := heartbeatKilledStream(ctx); got != tt.want {
				t.Errorf("heartbeatKilledStream() = %v, want %v (cause: %v)", got, tt.want, tt.cause)
			}
		})
	}
}

// End to end: a stream killed by a heartbeat deadline must FAIL the activity.
//
// Failing is what routes the turn through ActivityWrapper's
// spuriousHeartbeatCancel, which converts it to a retryable HeartbeatCancel so
// the work is re-dispatched. Returning success instead — which is what
// happened before, because a cancelled stream was indistinguishable from a
// user interrupt — skipped that guard entirely and handed the loop an empty
// turn it read as "the model is done".
func TestCallLLM_HeartbeatCancelledStreamFailsForRetry(t *testing.T) {
	driver := &cancellingMockLLMDriver{ready: make(chan struct{})}
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

	// Cancel the way a blown heartbeat RPC does: with DeadlineExceeded as the
	// cause, which is what context.WithTimeout records and what the SDK's
	// retryable-error path passes to cancelHandler.
	runHeartbeatKilled := func(actCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		cancelCtx, cancel := context.WithCancelCause(actCtx)
		defer cancel(nil)
		go func() {
			<-driver.ready
			cancel(context.DeadlineExceeded)
		}()
		return activityInstance.Execute(cancelCtx, input)
	}

	h.env.RegisterActivity(runHeartbeatKilled)
	_, err := h.env.ExecuteActivity(runHeartbeatKilled, callLLMInput(chat.ID, chat.ID, "mock-model"))

	require.Error(t, err,
		"a heartbeat-killed stream must fail so ActivityWrapper can retry it; returning "+
			"success reports a truncated turn as a completed one")
	assert.Contains(t, err.Error(), "heartbeat",
		"the failure must name the cause so it is greppable when it recurs")
}

// A REAL interrupt keeps its existing behavior: persist the partial, return
// success, and let the loop take one more mailbox-draining turn. This is the
// boundary that stops the fix above from retrying against the user's intent.
func TestCallLLM_RealInterruptStillSucceeds(t *testing.T) {
	driver := &cancellingMockLLMDriver{ready: make(chan struct{})}
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

	// A plain cancel with no distinguishing cause is how a user interrupt
	// reaches the activity.
	runInterrupted := func(actCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		cancelCtx, cancel := context.WithCancel(actCtx)
		defer cancel()
		go func() {
			<-driver.ready
			cancel()
		}()
		return activityInstance.Execute(cancelCtx, input)
	}

	h.env.RegisterActivity(runInterrupted)
	val, err := h.env.ExecuteActivity(runInterrupted, callLLMInput(chat.ID, chat.ID, "mock-model"))
	require.NoError(t, err, "a user interrupt is not an activity failure")

	var output CallLLMOutput
	require.NoError(t, val.Get(&output))
	assert.True(t, output.Aborted,
		"a real interrupt still reports aborted so the loop takes its draining turn")
}
