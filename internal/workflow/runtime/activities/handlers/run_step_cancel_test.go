// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// A run step Temporal cancelled tells the machine to stop its command, by the
// request id the command was sent under; every other end of the activity's
// context leaves the command running, because a retry reattaches to it.
func TestRunStep_StopsTheCommandOnlyWhenTemporalCancelsTheStep(t *testing.T) {
	cases := []struct {
		name  string
		cause error // nil: the context is not done
		stops bool
	}{
		{name: "server cancel (run cancelled or paused)", cause: temporal.NewCanceledError(), stops: true},
		{name: "attempt the server timed out", cause: serviceerror.NewNotFound("activity not found")},
		{name: "activity paused", cause: activity.ErrActivityPaused},
		{name: "worker stopping", cause: context.Canceled},
		{name: "command finished, context live"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if tc.cause != nil {
				cancel(tc.cause)
			}
			router := &toolexectest.Recorder{}
			a := NewExecuteRunStepActivity(nil, nil, nil).WithDaemonRouter(router)

			a.stopCommandIfCancelled(ctx, RunExecutorContext{UserID: "user-1"}, ExecuteRunStepInput{ChatID: "chat-1", StepID: "lease"}, "runstep-abc")

			if !tc.stops {
				assert.Empty(t, router.CancelCalls)
				return
			}
			require.Len(t, router.CancelCalls, 1)
			assert.Equal(t, toolexectest.CancelCall{UserID: "user-1", RequestID: "runstep-abc", Reason: runStepCancelReason}, router.CancelCalls[0])
		})
	}
}

// The cause survives the wrappers the activity runs under: a child context of
// a cancelled one carries its parent's cause.
func TestRunStep_CancelCauseSurvivesDerivedContexts(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	child, stop := context.WithCancel(parent)
	defer stop()
	cancel(temporal.NewCanceledError())

	assert.True(t, cancelledByServer(child))
	assert.False(t, cancelledByServer(context.Background()))
	assert.False(t, errors.Is(context.Cause(child), context.DeadlineExceeded))
}
