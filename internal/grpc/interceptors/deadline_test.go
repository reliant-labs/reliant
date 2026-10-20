// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/observability"
)

// slowRPCCounters snapshots the slow-RPC metric for the procedure label that
// connect.NewRequest produces (empty — Spec is only populated on real
// transports). Tests assert deltas, not absolute values, so they stay
// independent of ordering.
func slowRPCCounters() (inFlight, completed float64) {
	inFlight = testutil.ToFloat64(observability.SlowRPCTotal.WithLabelValues("", "in_flight"))
	completed = testutil.ToFloat64(observability.SlowRPCTotal.WithLabelValues("", "completed"))
	return inFlight, completed
}

// watchedBy is a Deadline slot reporting through ReportSlowRPC after
// slowAfter, so tests don't need real 10s sleeps. The repeat interval is an
// hour: the watchdog cannot tick twice within a test.
func watchedBy(slowAfter time.Duration, handler connect.UnaryFunc) connect.UnaryFunc {
	return serverkit.Deadline{
		Unary:      UnaryTimeout,
		SlowAfter:  slowAfter,
		SlowRepeat: time.Hour,
		OnSlow:     ReportSlowRPC,
	}.WrapUnary(handler)
}

func TestReportSlowRPCCountsASlowHandler(t *testing.T) {
	inFlightBefore, completedBefore := slowRPCCounters()

	wrapped := watchedBy(20*time.Millisecond, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		// Sleep well past SlowAfter so the in-flight tick fires while the
		// handler is still running.
		time.Sleep(120 * time.Millisecond)
		return nil, nil
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&struct{}{}))
	require.NoError(t, err)

	inFlightAfter, completedAfter := slowRPCCounters()
	require.Equal(t, float64(1), inFlightAfter-inFlightBefore,
		"slow handler should be flagged while still in flight")
	require.Equal(t, float64(1), completedAfter-completedBefore,
		"slow handler should be flagged on completion")
}

func TestReportSlowRPCIgnoresAFastHandler(t *testing.T) {
	inFlightBefore, completedBefore := slowRPCCounters()

	wrapped := watchedBy(50*time.Millisecond, func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
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

// reliant's chains leave SlowAfter/SlowRepeat at forge's defaults, which are
// the thresholds reliant's own watchdog used (10s first report, every 30s
// after): pin them so a forge change to either is noticed here.
func TestSlowRPCThresholdsAreForgeDefaults(t *testing.T) {
	require.Equal(t, 10*time.Second, serverkit.DefaultSlowAfter)
	require.Equal(t, 30*time.Second, serverkit.DefaultSlowRepeat)
}

// A budget is a ceiling: a caller's longer deadline no longer lifts it (the
// old interceptor only applied its timeout when the caller sent none).
func TestUnaryTimeoutIsACeilingOverALongerCallerDeadline(t *testing.T) {
	var remaining time.Duration
	wrapped := serverkit.Deadline{Unary: UnaryTimeout, Procedures: ProcedureTimeouts()}.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			remaining = time.Until(deadline)
			return nil, nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	_, err := wrapped(ctx, connect.NewRequest(&struct{}{}))
	require.NoError(t, err)
	require.LessOrEqual(t, remaining, UnaryTimeout)
}

// ProcedureTimeouts merges a package's own budgets over the table.
func TestProcedureTimeoutsTakesOwnedBudgets(t *testing.T) {
	const owned = "/reliant.v1.ForgeService/GetTopology"
	timeouts := ProcedureTimeouts(map[string]time.Duration{owned: 42 * time.Second})
	require.Equal(t, 42*time.Second, timeouts[owned])
	require.Equal(t, worktreeOperationTimeout, timeouts["/reliant.v1.WorktreeService/CreateWorktree"])
	require.Zero(t, timeouts["/reliant.v1.SystemService/StartOAuthSignIn"], "the OAuth sign-in waits on a person")
}
