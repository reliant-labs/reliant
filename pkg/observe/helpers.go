package observe

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// LogCall emits a single slog.Info record summarising a wrapped
// operation that returns an error. Use it for opt-in per-method
// observability when one Service calls another and you want a log line
// at the inner boundary:
//
//	start := time.Now()
//	user, err := s.userStore.Get(ctx, id)
//	observe.LogCall(ctx, logger, "userstore.Get", start, err)
//
// The record shape (msg = method, attrs = duration + error) matches
// what the now-removed middleware_gen.go used to emit, so dashboards
// keyed on those attribute names keep working.
//
// nil-safe on logger.
func LogCall(ctx context.Context, logger *slog.Logger, method string, start time.Time, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelInfo, method,
			slog.Duration("duration", time.Since(start)),
			slog.Any("error", err),
		)
		return
	}
	logger.LogAttrs(ctx, slog.LevelInfo, method,
		slog.Duration("duration", time.Since(start)),
	)
}

// TraceCall executes fn inside a new OpenTelemetry span named
// operationName and returns the inner result. Span errors are recorded
// when fn returns a non-nil error.
//
// Use TraceCall to express the previous tracing_gen.go behaviour at
// explicit call sites:
//
//	user, err := observe.TraceCall(ctx, tracer, "userstore.Get", func(ctx context.Context) (User, error) {
//	    return s.userStore.Get(ctx, id)
//	})
//
// nil-safe on tracer (executes fn directly, no span).
func TraceCall[T any](ctx context.Context, tracer trace.Tracer, operationName string, fn func(context.Context) (T, error)) (T, error) {
	if tracer == nil {
		return fn(ctx)
	}
	ctx, span := tracer.Start(ctx, operationName)
	defer span.End()
	out, err := fn(ctx)
	RecordSpanError(span, err)
	return out, err
}

// TraceVoidCall is the no-result variant of TraceCall, for operations
// that return only an error. Same semantics, no value.
func TraceVoidCall(ctx context.Context, tracer trace.Tracer, operationName string, fn func(context.Context) error) error {
	if tracer == nil {
		return fn(ctx)
	}
	ctx, span := tracer.Start(ctx, operationName)
	defer span.End()
	err := fn(ctx)
	RecordSpanError(span, err)
	return err
}

// RecordSpanError marks span as failed when err is non-nil: the error is
// recorded as a span event and the span status is set to Error. This is
// the one-liner the owned per-package observe.go wrappers call after each
// inner method invocation:
//
//	ctx, span := o.tracer.Start(ctx, "userstore.Get")
//	defer span.End()
//	out, err := o.inner.Get(ctx, id)
//	observe.RecordSpanError(span, err)
//
// A server fault that svcerr captured a stack for (see svcerr.StackOf) carries
// it on the span's exception event as exception.stacktrace; a client-class
// error never does.
//
// nil-safe on both span and err (a nil err is the no-op success path).
func RecordSpanError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	if st, ok := svcerr.StackOf(err); ok {
		span.RecordError(err, trace.WithAttributes(attribute.String(LogKeyExceptionStacktrace, st.Trace)))
	} else {
		span.RecordError(err)
	}
	span.SetStatus(codes.Error, err.Error())
}

// CallMetrics is a triple of OpenTelemetry instruments for opt-in
// per-method instrumentation inside internal packages. Use NewCallMetrics
// to construct; reuse a single CallMetrics per package (creating
// instruments per-call is expensive).
type CallMetrics struct {
	calls    metric.Int64Counter
	errs     metric.Int64Counter
	duration metric.Float64Histogram
}

// NewCallMetrics builds a CallMetrics on meter using the canonical
// "<namespace>.{calls,errors,duration}" names — matching the metric
// names the now-removed metrics_gen.go template emitted, so dashboards
// keyed on those names keep working.
//
// Errors from meter.Int64Counter / Float64Histogram are silently
// dropped (the same posture as contractkit.NewMetrics). meter == nil
// returns a CallMetrics whose RecordCall / RecordError methods are
// no-ops, so callers can wire a CallMetrics in test harnesses without
// configuring OTel.
func NewCallMetrics(meter metric.Meter, namespace string) *CallMetrics {
	if meter == nil {
		return &CallMetrics{}
	}
	metrics.DescribeCallMetrics(namespace)
	calls, _ := meter.Int64Counter(namespace+".calls", declaredOpts[metric.Int64CounterOption](namespace+".calls")...)
	errs, _ := meter.Int64Counter(namespace+".errors", declaredOpts[metric.Int64CounterOption](namespace+".errors")...)
	dur, _ := meter.Float64Histogram(namespace+".duration", declaredOpts[metric.Float64HistogramOption](namespace+".duration")...)
	return &CallMetrics{
		calls:    calls,
		errs:     errs,
		duration: dur,
	}
}

// RecordCall increments the call counter and (if err != nil) the error
// counter, and records duration on the histogram. method is attached
// as the "method" attribute on each instrument.
//
// Idiomatic call site:
//
//	start := time.Now()
//	out, err := s.inner.Do(ctx, req)
//	s.metrics.RecordCall(ctx, "Do", start, err)
//	return out, err
//
// nil-safe on the receiver.
func (m *CallMetrics) RecordCall(ctx context.Context, method string, start time.Time, err error) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attr := metric.WithAttributes(attribute.String("method", method))
	if m.calls != nil {
		m.calls.Add(ctx, 1, attr)
	}
	if m.duration != nil {
		m.duration.Record(ctx, time.Since(start).Seconds(), attr)
	}
	if err != nil && m.errs != nil {
		// error_class keeps user errors countable now that they no longer
		// log at ERROR; see error_class.go.
		m.errs.Add(ctx, 1, metric.WithAttributes(
			attribute.String("method", method),
			attribute.String(ErrorClassKey, svcerr.Classify(err).String()),
		))
	}
}
