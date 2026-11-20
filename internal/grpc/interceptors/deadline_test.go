// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/observability"
)

// slowRPCCounters snapshots the slow-RPC metric for the procedure label that
// a hand-built unary spec carries (empty). Tests assert deltas, not absolute values, so they stay
// independent of ordering.
func slowRPCCounters() (inFlight, completed float64) {
	inFlight = testutil.ToFloat64(observability.SlowRPCTotal.WithLabelValues("", "in_flight"))
	completed = testutil.ToFloat64(observability.SlowRPCTotal.WithLabelValues("", "completed"))
	return inFlight, completed
}

// watchedBy is a Deadline slot reporting through ReportSlowRPC after
// slowAfter, so tests don't need real 10s sleeps. The repeat interval is an
// hour: the watchdog cannot tick twice within a test.
func watchedBy(slowAfter time.Duration, handler func(context.Context) error) func(context.Context) error {
	return deadlined(serverkit.Deadline{
		Unary:      UnaryTimeout,
		SlowAfter:  slowAfter,
		SlowRepeat: time.Hour,
		OnSlow:     ReportSlowRPC,
	}, handler)
}

// deadlined runs handler as a unary call's handler behind d's interceptor.
func deadlined(d serverkit.Deadline, handler func(context.Context) error) func(context.Context) error {
	wrapped := d.InterceptorV2()(func(ctx context.Context, _ connect.Spec, _ connect.ServerStream) error {
		return handler(ctx)
	})
	return func(ctx context.Context) error {
		return wrapped(ctx, connect.Spec{StreamType: connect.StreamTypeUnary}, nil)
	}
}

func TestReportSlowRPCCountsASlowHandler(t *testing.T) {
	inFlightBefore, completedBefore := slowRPCCounters()

	wrapped := watchedBy(20*time.Millisecond, func(context.Context) error {
		// Stay in flight until the in-flight tick has fired.
		testkit.Eventually(t, "the slow handler to be reported in flight", func() bool {
			inFlight, _ := slowRPCCounters()
			return inFlight > inFlightBefore
		})
		return nil
	})

	err := wrapped(context.Background())
	require.NoError(t, err)

	inFlightAfter, completedAfter := slowRPCCounters()
	require.Equal(t, float64(1), inFlightAfter-inFlightBefore,
		"slow handler should be flagged while still in flight")
	require.Equal(t, float64(1), completedAfter-completedBefore,
		"slow handler should be flagged on completion")
}

func TestReportSlowRPCIgnoresAFastHandler(t *testing.T) {
	inFlightBefore, completedBefore := slowRPCCounters()

	wrapped := watchedBy(50*time.Millisecond, func(context.Context) error {
		return nil
	})

	err := wrapped(context.Background())
	require.NoError(t, err)

	// Give a would-be stray watchdog goroutine a chance to (incorrectly)
	// fire before asserting nothing was counted.
	// forge:lint-disable-next-line forge-test-fixed-sleep: asserting nothing happens — a stray watchdog would fire 50ms in, and there is no event to wait for when it correctly does not
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
	wrapped := deadlined(serverkit.Deadline{Unary: UnaryTimeout, Procedures: ProcedureTimeouts()},
		func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			remaining = time.Until(deadline)
			return nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	err := wrapped(ctx)
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
