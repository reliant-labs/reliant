package serverkit

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// drain_phases.go — what a graceful shutdown did, one line per phase.
//
// A rollout that misbehaves is diagnosed from the shutdown's own log, and the
// sequence used to say "server stopping" and then nothing until an error, so
// "where did the 30 seconds go" had no answer: was it the pre-stop pause, a
// stream nobody ended, a worker ignoring its context, a teardown blocked on a
// pool? Each phase now writes one INFO line, every one with `phase` and its
// `elapsed`, and the listener's carries what the drain did to its streams. The
// same durations go to forge.server.drain.duration, exported by the telemetry
// flush that ends the shutdown.
//
// Each piece of the sequence writes its own phases, with the Logger and Meter
// it was configured with: Drainer the readiness flip, the pre-stop pause and
// the total (Begin, Complete); DrainHTTP the listener (through its Streams).
// So a main that drains with those pieces gets the same lines as Run, and Run,
// which adds only the workers, operators and teardown phases, writes none of
// the others itself.

// drainMetric is the built-in histogram of shutdown phase durations.
const drainMetric = "forge.server.drain.duration"

// meterName is the instrumentation scope of serverkit's own metrics.
const meterName = "github.com/reliant-labs/forge/pkg/serverkit"

// drainPhases logs and measures the phases of one shutdown.
type drainPhases struct {
	logger *slog.Logger
	hist   metric.Float64Histogram // nil: the meter refused the instrument
}

// newDrainPhases resolves where phases go: a nil logger is slog.Default(), a
// nil meter the global OpenTelemetry meter (which Run's observe.Setup, or the
// caller's own, installs).
func newDrainPhases(logger *slog.Logger, meter metric.Meter) *drainPhases {
	if logger == nil {
		logger = slog.Default()
	}
	if meter == nil {
		meter = otel.Meter(meterName)
	}
	p := &drainPhases{logger: logger}
	s := metrics.BuiltinSpec(drainMetric)
	if h, err := meter.Float64Histogram(s.Name, metric.WithUnit(s.Unit), metric.WithDescription(s.Description)); err == nil {
		p.hist = h
	}
	return p
}

// done records that phase, begun at start, has finished: one INFO line, msg
// plus attrs, with the phase's name and how long it took. It returns the end,
// which is the next phase's start.
func (p *drainPhases) done(phase string, start time.Time, msg string, attrs ...any) time.Time {
	now := time.Now()
	elapsed := now.Sub(start)
	p.record(phase, elapsed)
	p.logger.Info(msg, append([]any{"phase", phase, "elapsed", elapsed.Round(time.Millisecond)}, attrs...)...)
	return now
}

func (p *drainPhases) record(phase string, d time.Duration) {
	if p.hist != nil {
		p.hist.Record(context.Background(), d.Seconds(), metric.WithAttributes(attribute.String("phase", phase)))
	}
}
