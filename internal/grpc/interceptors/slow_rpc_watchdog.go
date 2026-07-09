// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// Slow-RPC watchdog thresholds. Every logging/metrics interceptor in the
// chain observes an RPC only when it COMPLETES — a hung handler is invisible
// for as long as it hangs, which during the 2026-07-09 worktree incident was
// "forever, silently, while the whole renderer starved behind it". The
// watchdog logs while the handler is still running, so a wedge shows up in
// the logs within seconds instead of never.
// Vars rather than consts so tests can shrink them; production defaults are
// 10s/30s and nothing outside tests mutates them.
var (
	// slowRPCThreshold is when a unary handler is first flagged. Interactive
	// RPCs should complete in well under a second; 10s is unambiguously wrong
	// without being noisy for legitimately chunky calls.
	slowRPCThreshold = 10 * time.Second
	// slowRPCRepeat re-logs a still-running handler so a multi-minute hang
	// stays visible in the log tail rather than scrolling away.
	slowRPCRepeat = 30 * time.Second
)

// SlowRPCWatchdogInterceptor flags unary handlers that are still running
// after slowRPCThreshold, and logs the final duration of any handler that
// exceeded it. Streaming handlers are deliberately NOT watched: long-lived
// streams (user updates, chat, terminals) are healthy by design and would
// only add noise.
type SlowRPCWatchdogInterceptor struct{}

// NewSlowRPCWatchdogInterceptor creates a new slow-RPC watchdog interceptor.
func NewSlowRPCWatchdogInterceptor() *SlowRPCWatchdogInterceptor {
	return &SlowRPCWatchdogInterceptor{}
}

// WrapUnary implements connect.Interceptor.
func (i *SlowRPCWatchdogInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure
		// Snapshot the thresholds once, in the request goroutine: the watchdog
		// goroutine must not read the package vars (tests mutate them, and a
		// mid-request change would make one RPC see two thresholds anyway).
		threshold, repeat := slowRPCThreshold, slowRPCRepeat
		start := time.Now()
		done := make(chan struct{})
		go func() {
			timer := time.NewTimer(threshold)
			defer timer.Stop()
			for {
				select {
				case <-done:
					return
				case <-timer.C:
					logging.Warn("[SlowRPC] unary handler still in flight",
						"procedure", procedure,
						"elapsed", time.Since(start).Round(time.Second).String())
					observability.SlowRPCTotal.WithLabelValues(procedure, "in_flight").Inc()
					timer.Reset(repeat)
				}
			}
		}()

		resp, err := next(ctx, req)
		close(done)

		if elapsed := time.Since(start); elapsed >= threshold {
			logging.Warn("[SlowRPC] unary handler completed slowly",
				"procedure", procedure,
				"elapsed", elapsed.Round(time.Millisecond).String(),
				"failed", err != nil)
			observability.SlowRPCTotal.WithLabelValues(procedure, "completed").Inc()
		}
		return resp, err
	}
}

// WrapStreamingClient implements connect.Interceptor (pass-through).
func (i *SlowRPCWatchdogInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements connect.Interceptor (pass-through — streams
// are long-lived by design; see the type comment).
func (i *SlowRPCWatchdogInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
