package lifecyclekit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// EveryOptions configures Every. Interval is required.
type EveryOptions struct {
	// Interval is the pause between the end of one run and the start of the
	// next. Runs never overlap.
	Interval time.Duration
	// RunImmediately runs once at start instead of waiting one Interval.
	RunImmediately bool
	// Logger receives one line per failed or panicking run. nil uses
	// slog.Default().
	Logger *slog.Logger
}

// Every returns a worker that calls fn on a fixed cadence until it is
// stopped. Each run is a trace of its own — a root span named
// "worker.<name>.run" (observe.RunRoot) — so the business methods, queries
// and calls it makes nest under it instead of each being a one-span trace. A
// failed run is logged and the cadence continues; a panicking run is recovered,
// recorded on its span, and logged, so one bad run never stops the loop or
// the process.
//
// It is the loop a hand-written worker would write, with the tracing and the
// recovery it would forget.
func Every(name string, opts EveryOptions, fn func(ctx context.Context) error) serverkit.Worker {
	if opts.Interval <= 0 {
		panic(fmt.Sprintf("lifecyclekit.Every(%q): Interval must be positive", name))
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	spanName := "worker." + name + ".run"
	runOnce := func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "worker run panicked", "worker", name, "panic", r)
			}
		}()
		if err := observe.RunRoot(ctx, spanName, fn); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "worker run failed", "worker", name, "error", err)
		}
	}
	return NewContextWorkerInstance(name, func(ctx context.Context) error {
		if opts.RunImmediately {
			runOnce(ctx)
		}
		timer := time.NewTimer(opts.Interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-timer.C:
				runOnce(ctx)
				timer.Reset(opts.Interval)
			}
		}
	}, func(context.Context) error { return nil })
}
