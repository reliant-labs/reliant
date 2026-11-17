package metrics

import (
	"context"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
)

// The instruments below are the standard OpenTelemetry interfaces, wrapped so
// the declared attribute check runs before every measurement. Callers hold the
// OTel interface types and record exactly as they would without the registry:
//
//	orders.Add(ctx, 1, metric.WithAttributes(attribute.String("status", "ok")))
//
// Each wrapper embeds the OTel `embedded` marker, which the metric API requires
// of any implementation of its instrument interfaces.

type int64Counter struct {
	embedded.Int64Counter
	g    *guard
	inst metric.Int64Counter
}

func (c *int64Counter) Add(ctx context.Context, v int64, opts ...metric.AddOption) {
	c.inst.Add(ctx, v, c.g.addOpts(ctx, opts)...)
}
func (c *int64Counter) Enabled(ctx context.Context) bool { return c.inst.Enabled(ctx) }

type float64Counter struct {
	embedded.Float64Counter
	g    *guard
	inst metric.Float64Counter
}

func (c *float64Counter) Add(ctx context.Context, v float64, opts ...metric.AddOption) {
	c.inst.Add(ctx, v, c.g.addOpts(ctx, opts)...)
}
func (c *float64Counter) Enabled(ctx context.Context) bool { return c.inst.Enabled(ctx) }

type int64UpDownCounter struct {
	embedded.Int64UpDownCounter
	g    *guard
	inst metric.Int64UpDownCounter
}

func (c *int64UpDownCounter) Add(ctx context.Context, v int64, opts ...metric.AddOption) {
	c.inst.Add(ctx, v, c.g.addOpts(ctx, opts)...)
}
func (c *int64UpDownCounter) Enabled(ctx context.Context) bool { return c.inst.Enabled(ctx) }

type float64UpDownCounter struct {
	embedded.Float64UpDownCounter
	g    *guard
	inst metric.Float64UpDownCounter
}

func (c *float64UpDownCounter) Add(ctx context.Context, v float64, opts ...metric.AddOption) {
	c.inst.Add(ctx, v, c.g.addOpts(ctx, opts)...)
}
func (c *float64UpDownCounter) Enabled(ctx context.Context) bool { return c.inst.Enabled(ctx) }

type int64Histogram struct {
	embedded.Int64Histogram
	g    *guard
	inst metric.Int64Histogram
}

func (h *int64Histogram) Record(ctx context.Context, v int64, opts ...metric.RecordOption) {
	h.inst.Record(ctx, v, h.g.recordOpts(ctx, opts)...)
}
func (h *int64Histogram) Enabled(ctx context.Context) bool { return h.inst.Enabled(ctx) }

type float64Histogram struct {
	embedded.Float64Histogram
	g    *guard
	inst metric.Float64Histogram
}

func (h *float64Histogram) Record(ctx context.Context, v float64, opts ...metric.RecordOption) {
	h.inst.Record(ctx, v, h.g.recordOpts(ctx, opts)...)
}
func (h *float64Histogram) Enabled(ctx context.Context) bool { return h.inst.Enabled(ctx) }

type int64Gauge struct {
	embedded.Int64Gauge
	g    *guard
	inst metric.Int64Gauge
}

func (x *int64Gauge) Record(ctx context.Context, v int64, opts ...metric.RecordOption) {
	x.inst.Record(ctx, v, x.g.recordOpts(ctx, opts)...)
}
func (x *int64Gauge) Enabled(ctx context.Context) bool { return x.inst.Enabled(ctx) }

type float64Gauge struct {
	embedded.Float64Gauge
	g    *guard
	inst metric.Float64Gauge
}

func (x *float64Gauge) Record(ctx context.Context, v float64, opts ...metric.RecordOption) {
	x.inst.Record(ctx, v, x.g.recordOpts(ctx, opts)...)
}
func (x *float64Gauge) Enabled(ctx context.Context) bool { return x.inst.Enabled(ctx) }

// Int64Counter declares s as a counter and returns its instrument. It panics on
// an invalid or conflicting declaration: a bad declaration is a programmer
// error and surfaces at package init, like regexp.MustCompile.
func (r *Registry) Int64Counter(s Spec) metric.Int64Counter {
	g, opts := r.instrument(s, Counter)
	inst, err := r.meter.Int64Counter(s.Name, counterOpts[metric.Int64CounterOption](opts)...)
	return &int64Counter{g: g, inst: mustCreate(inst, err, s.Name)}
}

// Float64Counter is Int64Counter for fractional increments.
func (r *Registry) Float64Counter(s Spec) metric.Float64Counter {
	g, opts := r.instrument(s, Counter)
	inst, err := r.meter.Float64Counter(s.Name, counterOpts[metric.Float64CounterOption](opts)...)
	return &float64Counter{g: g, inst: mustCreate(inst, err, s.Name)}
}

// Int64UpDownCounter declares s as an up-down counter (a value that rises and
// falls, such as in-flight requests) and returns its instrument.
func (r *Registry) Int64UpDownCounter(s Spec) metric.Int64UpDownCounter {
	g, opts := r.instrument(s, UpDownCounter)
	inst, err := r.meter.Int64UpDownCounter(s.Name, counterOpts[metric.Int64UpDownCounterOption](opts)...)
	return &int64UpDownCounter{g: g, inst: mustCreate(inst, err, s.Name)}
}

// Float64UpDownCounter is Int64UpDownCounter for fractional deltas.
func (r *Registry) Float64UpDownCounter(s Spec) metric.Float64UpDownCounter {
	g, opts := r.instrument(s, UpDownCounter)
	inst, err := r.meter.Float64UpDownCounter(s.Name, counterOpts[metric.Float64UpDownCounterOption](opts)...)
	return &float64UpDownCounter{g: g, inst: mustCreate(inst, err, s.Name)}
}

// Int64Histogram declares s as a histogram and returns its instrument.
// Spec.Buckets, when set, replaces the default bucket boundaries; a histogram
// in seconds that sets none gets SecondsBuckets.
func (r *Registry) Int64Histogram(s Spec) metric.Int64Histogram {
	g, opts := r.instrument(s, Histogram)
	o := counterOpts[metric.Int64HistogramOption](opts)
	if b := histogramBuckets(s, g.d.spec); len(b) > 0 {
		o = append(o, metric.WithExplicitBucketBoundaries(b...))
	}
	inst, err := r.meter.Int64Histogram(s.Name, o...)
	return &int64Histogram{g: g, inst: mustCreate(inst, err, s.Name)}
}

// Float64Histogram is Int64Histogram for fractional measurements (durations in
// seconds).
func (r *Registry) Float64Histogram(s Spec) metric.Float64Histogram {
	g, opts := r.instrument(s, Histogram)
	o := counterOpts[metric.Float64HistogramOption](opts)
	if b := histogramBuckets(s, g.d.spec); len(b) > 0 {
		o = append(o, metric.WithExplicitBucketBoundaries(b...))
	}
	inst, err := r.meter.Float64Histogram(s.Name, o...)
	return &float64Histogram{g: g, inst: mustCreate(inst, err, s.Name)}
}

// Int64Gauge declares s as a synchronous gauge (the last value recorded, such
// as a queue's current depth when it changes) and returns its instrument.
func (r *Registry) Int64Gauge(s Spec) metric.Int64Gauge {
	g, opts := r.instrument(s, Gauge)
	inst, err := r.meter.Int64Gauge(s.Name, counterOpts[metric.Int64GaugeOption](opts)...)
	return &int64Gauge{g: g, inst: mustCreate(inst, err, s.Name)}
}

// Float64Gauge is Int64Gauge for fractional values.
func (r *Registry) Float64Gauge(s Spec) metric.Float64Gauge {
	g, opts := r.instrument(s, Gauge)
	inst, err := r.meter.Float64Gauge(s.Name, counterOpts[metric.Float64GaugeOption](opts)...)
	return &float64Gauge{g: g, inst: mustCreate(inst, err, s.Name)}
}

// counterOpts converts the shared unit/description options to the option type
// one instrument constructor takes. Every metric.Int64CounterOption (and the
// other per-instrument option types) is satisfied by a metric.InstrumentOption.
func counterOpts[T any](opts []metric.InstrumentOption) []T {
	out := make([]T, 0, len(opts)+1)
	for _, o := range opts {
		out = append(out, o.(T))
	}
	return out
}

// histogramBuckets is the boundaries a histogram is created with: the
// constructor's own Spec.Buckets when it names any (an explicit override
// always wins, even over an earlier declaration of the same metric, since
// buckets are not part of a metric's identity), else the registered
// declaration's — its explicit Buckets, or SecondsBuckets for a seconds
// histogram that names none.
func histogramBuckets(s, declared Spec) []float64 {
	if len(s.Buckets) > 0 {
		return append([]float64(nil), s.Buckets...)
	}
	return declared.normalized().Buckets
}
