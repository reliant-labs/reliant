package contractkit

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics groups the three OpenTelemetry instruments used by every
// metric_gen.go wrapper: a call-count counter, an error-count counter,
// and a duration histogram (in seconds).
//
// The instruments are named according to the canonical
// "<package>.calls", "<package>.errors", "<package>.duration" pattern
// that the previous generated wrapper emitted. Callers construct a
// Metrics via NewMetrics and embed it in the per-interface generated
// wrapper.
type Metrics struct {
	callCount metric.Int64Counter
	errCount  metric.Int64Counter
	duration  metric.Float64Histogram
}

// NewMetrics constructs a Metrics by creating three instruments on
// meter using the canonical "<packageName>.{calls,errors,duration}"
// names and the same descriptions/units that the previous generated
// code used.
//
// Errors from meter.Int64Counter / Float64Histogram are silently
// dropped (mirroring the previous generated code, which discarded
// them via _, _ assignments). This preserves the existing behavioural
// fingerprint.
func NewMetrics(meter metric.Meter, packageName string) *Metrics {
	callCount, _ := meter.Int64Counter(
		packageName+".calls",
		metric.WithDescription("Total method calls"),
	)
	errCount, _ := meter.Int64Counter(
		packageName+".errors",
		metric.WithDescription("Total method errors"),
	)
	duration, _ := meter.Float64Histogram(
		packageName+".duration",
		metric.WithDescription("Method duration in seconds"),
		metric.WithUnit("s"),
	)
	return &Metrics{
		callCount: callCount,
		errCount:  errCount,
		duration:  duration,
	}
}

// RecordCall registers a method invocation on the call-count counter
// with a "method" attribute. Should be called immediately before
// invoking the inner method (matching the previous generated code's
// ordering: increment count, then call inner, then record duration).
//
// nil-safe on the receiver.
func (m *Metrics) RecordCall(ctx context.Context, method string) {
	if m == nil || m.callCount == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.callCount.Add(ctx, 1, metric.WithAttributes(attribute.String("method", method)))
}

// RecordDuration registers the elapsed duration since start on the
// duration histogram with a "method" attribute. Should be called
// immediately after the inner method returns.
//
// nil-safe on the receiver.
func (m *Metrics) RecordDuration(ctx context.Context, method string, start time.Time) {
	if m == nil || m.duration == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.duration.Record(
		ctx,
		time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("method", method)),
	)
}

// RecordError increments the error counter with a "method" attribute
// when err is non-nil. nil-safe on err and on the receiver.
func (m *Metrics) RecordError(ctx context.Context, method string, err error) {
	if err == nil || m == nil || m.errCount == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.errCount.Add(ctx, 1, metric.WithAttributes(attribute.String("method", method)))
}
