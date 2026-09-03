// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// A heartbeat RPC that merely timed out locally must not be mistaken for an
// instruction to stop. The SDK cancels the activity context on any retryable
// heartbeat error, so without this distinction a single slow round trip to the
// Temporal server kills a healthy activity and the workflow treats the
// resulting context.Canceled as a pause.
func TestSpuriousHeartbeatCancel(t *testing.T) {
	t.Parallel()
	closedWorkerStop := func() <-chan struct{} {
		ch := make(chan struct{})
		close(ch)
		return ch
	}

	tests := []struct {
		name         string
		cause        error
		workerStopCh <-chan struct{}
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
			name:         "worker shutdown is handled by its own rewrite",
			cause:        context.DeadlineExceeded,
			workerStopCh: closedWorkerStop(),
			want:         false,
		},
		{
			name:         "live context is not a cancellation at all",
			notCancelled: true,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if !tt.notCancelled {
				cancel(tt.cause)
			}

			if got := spuriousHeartbeatCancel(ctx, tt.workerStopCh); got != tt.want {
				t.Errorf("spuriousHeartbeatCancel() = %v, want %v", got, tt.want)
			}
		})
	}
}
