// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/observability"
)

// shrinkWatchdogThresholds lowers the package-level thresholds so tests don't
// need real 10s sleeps, restoring the production defaults on cleanup.
func shrinkWatchdogThresholds(t *testing.T, threshold, repeat time.Duration) {
	t.Helper()
	origThreshold, origRepeat := slowRPCThreshold, slowRPCRepeat
	slowRPCThreshold, slowRPCRepeat = threshold, repeat
	t.Cleanup(func() {
		slowRPCThreshold, slowRPCRepeat = origThreshold, origRepeat
	})
}

// slowRPCCounters snapshots the watchdog metric for the procedure label that
// connect.NewRequest produces (empty — Spec is only populated on real
// transports). Tests assert deltas, not absolute values, so they stay
// independent of ordering.
func slowRPCCounters() (inFlight, completed float64) {
	inFlight = testutil.ToFloat64(observability.SlowRPCTotal.WithLabelValues("", "in_flight"))
	completed = testutil.ToFloat64(observability.SlowRPCTotal.WithLabelValues("", "completed"))
	return inFlight, completed
}

func TestSlowRPCWatchdogFlagsSlowHandler(t *testing.T) {
	shrinkWatchdogThresholds(t, 20*time.Millisecond, 1*time.Hour)

	inFlightBefore, completedBefore := slowRPCCounters()

	wrapped := NewSlowRPCWatchdogInterceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		// Sleep well past the shrunken threshold so the in-flight tick fires
		// while the handler is still running.
		time.Sleep(120 * time.Millisecond)
		return nil, nil
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&struct{}{}))
	require.NoError(t, err)

	inFlightAfter, completedAfter := slowRPCCounters()
	// Exactly one in-flight tick: the repeat interval is 1h so the timer
	// cannot fire twice within the test.
	require.Equal(t, float64(1), inFlightAfter-inFlightBefore,
		"slow handler should be flagged while still in flight")
	require.Equal(t, float64(1), completedAfter-completedBefore,
		"slow handler should be flagged on completion")
}

func TestSlowRPCWatchdogIgnoresFastHandler(t *testing.T) {
	shrinkWatchdogThresholds(t, 50*time.Millisecond, 1*time.Hour)

	inFlightBefore, completedBefore := slowRPCCounters()

	wrapped := NewSlowRPCWatchdogInterceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&struct{}{}))
	require.NoError(t, err)

	// Give a would-be stray watchdog goroutine a chance to (incorrectly)
	// fire before asserting nothing was counted.
	time.Sleep(80 * time.Millisecond)

	inFlightAfter, completedAfter := slowRPCCounters()
	require.Equal(t, inFlightBefore, inFlightAfter,
		"fast handler must not be flagged in flight")
	require.Equal(t, completedBefore, completedAfter,
		"fast handler must not be flagged on completion")
}

func TestSlowRPCWatchdogProductionDefaults(t *testing.T) {
	// The thresholds are vars purely for testability — pin the production
	// values so an accidental edit fails loudly.
	require.Equal(t, 10*time.Second, slowRPCThreshold)
	require.Equal(t, 30*time.Second, slowRPCRepeat)
}
