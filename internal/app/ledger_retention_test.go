// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// retentionHarness runs a LedgerRetention whose clock and pass the test
// controls: after hands back the timer the test fires, and each pass is
// reported on passes instead of touching a database.
type retentionHarness struct {
	l       *LedgerRetention
	asked   chan time.Duration
	fire    chan time.Time
	passes  chan struct{}
	cancel  context.CancelFunc
	stopped chan error
}

func startRetention(t *testing.T, delay time.Duration) *retentionHarness {
	t.Helper()
	l, err := NewLedgerRetention(LedgerRetentionDeps{
		ChatUpdateRetention: 7 * 24 * time.Hour, StepExecutionRetention: 30 * 24 * time.Hour, StartDelay: delay,
	})
	require.NoError(t, err)
	h := &retentionHarness{
		l: l, asked: make(chan time.Duration, 1), fire: make(chan time.Time),
		passes: make(chan struct{}, 8), stopped: make(chan error, 1),
	}
	l.after = func(d time.Duration) <-chan time.Time {
		h.asked <- d
		return h.fire
	}
	l.runPass = func(context.Context, time.Time) { h.passes <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.stopped <- l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.stopped:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after its context ended")
		}
	})
	return h
}

func (h *retentionHarness) waitPass(t *testing.T) {
	t.Helper()
	select {
	case <-h.passes:
	case <-time.After(5 * time.Second):
		t.Fatal("no retention pass ran")
	}
}

// The first pass waits out the start delay, so a worker that starts in a
// rollout deletes nothing while api-servers of the previous release, which do
// not read the compaction watermark, may still be serving reconnects.
func TestLedgerRetention_FirstPassWaitsOutTheStartDelay(t *testing.T) {
	h := startRetention(t, time.Hour)

	select {
	case d := <-h.asked:
		require.Equal(t, time.Hour, d, "the first pass must wait the configured delay")
	case <-time.After(5 * time.Second):
		t.Fatal("Run never started waiting for the start delay")
	}
	// Run is blocked on the delay timer, which comes before any pass.
	require.Empty(t, h.passes, "a pass ran before the start delay was over")

	h.fire <- time.Now()
	h.waitPass(t)
}

// A delay of 0 runs the first pass at once, with no timer.
func TestLedgerRetention_ZeroStartDelayRunsAtOnce(t *testing.T) {
	h := startRetention(t, 0)
	h.waitPass(t)
	require.Empty(t, h.asked, "a zero delay must not wait on a timer")
}

// Shutting down during the delay ends Run without a pass.
func TestLedgerRetention_StopDuringStartDelayRunsNoPass(t *testing.T) {
	h := startRetention(t, time.Hour)
	<-h.asked
	h.cancel()
	select {
	case err := <-h.stopped:
		require.NoError(t, err)
		h.stopped <- err // for the cleanup
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context ended during the delay")
	}
	require.Empty(t, h.passes)
}
