package observe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
	"go.opentelemetry.io/otel/trace"
	tembedded "go.opentelemetry.io/otel/trace/embedded"
)

// newTestRequest builds a connect.AnyRequest for the given procedure.
// Using connect.NewRequest is the only supported way to satisfy the
// AnyRequest interface from a test (it has an unexported method that
// blocks third-party implementations); the procedure name has to be
// stamped via setRequestMethod which isn't exported, so the
// interceptors that read Spec().Procedure see the empty string in
// these tests. That's a bug for "interceptor sees X" assertions —
// rewrite each test to assert the parts we control via header / ctx
// / span name plumbing instead.
func newTestRequest() *connect.Request[struct{}] {
	return connect.NewRequest(&struct{}{})
}

// TestLoggingInterceptor_EmitsRPCCompletedRecord verifies the success
// path emits an INFO record with duration. The procedure attribute
// will be empty in tests (connect doesn't expose Spec stamping outside
// the Handler machinery); production traffic populates it.
func TestLoggingInterceptor_EmitsRPCCompletedRecord(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	icep := LoggingInterceptor(logger)
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&struct{}{}), nil
	})

	_, err := wrapped(context.Background(), newTestRequest())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "msg=\"rpc completed\"") {
		t.Errorf("missing msg=rpc completed in %q", out)
	}
	if !strings.Contains(out, "duration=") {
		t.Errorf("missing duration attr in %q", out)
	}
}

// TestLoggingInterceptor_EmitsErrorOnFailure asserts that errors land
// in the log record so SREs can see failure metadata in the same place
// as success records.
func TestLoggingInterceptor_EmitsErrorOnFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	icep := LoggingInterceptor(logger)
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, errors.New("boom")
	})

	if _, err := wrapped(context.Background(), newTestRequest()); err == nil {
		t.Fatal("expected error")
	}

	out := buf.String()
	if !strings.Contains(out, "error=boom") {
		t.Errorf("missing error attr in %q", out)
	}
	if !strings.Contains(out, "msg=\"rpc failed\"") {
		t.Errorf("missing msg=rpc failed in %q", out)
	}
}

// TestLoggingInterceptor_NilLoggerSafe — the interceptor falls back to
// slog.Default when given nil. Returning nil to the caller would have
// been a footgun in production where the user simply forgot to wire
// the dep.
func TestLoggingInterceptor_NilLoggerSafe(t *testing.T) {
	icep := LoggingInterceptor(nil)
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&struct{}{}), nil
	})
	_, _ = wrapped(context.Background(), newTestRequest())
	// no panic = pass
}

// fakeTracer / fakeSpan let us assert on tracer.Start without spinning
// up the full OTel SDK. We can't import contractkit's variant (they're
// in a different module) and they're small.
type fakeTracer struct {
	tembedded.Tracer
	lastName string
	lastSpan *fakeSpan
}

func (t *fakeTracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	t.lastName = name
	s := &fakeSpan{}
	t.lastSpan = s
	return ctx, s
}

type fakeSpan struct {
	tembedded.Span
	recordedErr error
	statusCode  codes.Code
	statusDesc  string
	endCalled   bool
}

func (s *fakeSpan) End(...trace.SpanEndOption)                    { s.endCalled = true }
func (s *fakeSpan) AddEvent(string, ...trace.EventOption)         {}
func (s *fakeSpan) AddLink(trace.Link)                            {}
func (s *fakeSpan) IsRecording() bool                             { return true }
func (s *fakeSpan) RecordError(err error, _ ...trace.EventOption) { s.recordedErr = err }
func (s *fakeSpan) SpanContext() trace.SpanContext                { return trace.SpanContext{} }
func (s *fakeSpan) SetStatus(c codes.Code, d string) {
	s.statusCode = c
	s.statusDesc = d
}
func (s *fakeSpan) SetName(string)                       {}
func (s *fakeSpan) SetAttributes(...attribute.KeyValue)  {}
func (s *fakeSpan) TracerProvider() trace.TracerProvider { return nil }

func TestTracingInterceptor_StartsSpan(t *testing.T) {
	tr := &fakeTracer{}
	icep := TracingInterceptor(tr)
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&struct{}{}), nil
	})

	if _, err := wrapped(context.Background(), newTestRequest()); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if tr.lastSpan == nil || !tr.lastSpan.endCalled {
		t.Error("span.End not called")
	}
}

func TestTracingInterceptor_RecordsErrorOnFailure(t *testing.T) {
	tr := &fakeTracer{}
	icep := TracingInterceptor(tr)
	wantErr := errors.New("boom")
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, wantErr
	})

	_, _ = wrapped(context.Background(), newTestRequest())
	if tr.lastSpan.recordedErr != wantErr {
		t.Errorf("recordedErr = %v, want %v", tr.lastSpan.recordedErr, wantErr)
	}
	if tr.lastSpan.statusCode != codes.Error {
		t.Errorf("statusCode = %v, want Error", tr.lastSpan.statusCode)
	}
}

func TestTracingInterceptor_NilTracerIsPassThrough(t *testing.T) {
	icep := TracingInterceptor(nil)
	called := false
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return connect.NewResponse(&struct{}{}), nil
	})
	_, _ = wrapped(context.Background(), newTestRequest())
	if !called {
		t.Error("inner not invoked when tracer is nil")
	}
}

// fakeMeter records the names of instruments created on it so tests
// can assert that MetricsInterceptor wires the canonical names.
type fakeMeter struct {
	embedded.Meter
	int64CounterNames     []string
	float64HistogramNames []string
	counters              map[string]*fakeInt64Counter
	histograms            map[string]*fakeFloat64Histogram
}

func newFakeMeter() *fakeMeter {
	return &fakeMeter{
		counters:   map[string]*fakeInt64Counter{},
		histograms: map[string]*fakeFloat64Histogram{},
	}
}

func (m *fakeMeter) Int64Counter(name string, _ ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	m.int64CounterNames = append(m.int64CounterNames, name)
	c := &fakeInt64Counter{name: name}
	m.counters[name] = c
	return c, nil
}
func (m *fakeMeter) Int64UpDownCounter(string, ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return nil, nil
}
func (m *fakeMeter) Int64Histogram(string, ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	return nil, nil
}
func (m *fakeMeter) Int64Gauge(string, ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	return nil, nil
}
func (m *fakeMeter) Int64ObservableCounter(string, ...metric.Int64ObservableCounterOption) (metric.Int64ObservableCounter, error) {
	return nil, nil
}
func (m *fakeMeter) Int64ObservableUpDownCounter(string, ...metric.Int64ObservableUpDownCounterOption) (metric.Int64ObservableUpDownCounter, error) {
	return nil, nil
}
func (m *fakeMeter) Int64ObservableGauge(string, ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	return nil, nil
}
func (m *fakeMeter) Float64Counter(string, ...metric.Float64CounterOption) (metric.Float64Counter, error) {
	return nil, nil
}
func (m *fakeMeter) Float64UpDownCounter(string, ...metric.Float64UpDownCounterOption) (metric.Float64UpDownCounter, error) {
	return nil, nil
}
func (m *fakeMeter) Float64Histogram(name string, _ ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	m.float64HistogramNames = append(m.float64HistogramNames, name)
	h := &fakeFloat64Histogram{name: name}
	m.histograms[name] = h
	return h, nil
}
func (m *fakeMeter) Float64Gauge(string, ...metric.Float64GaugeOption) (metric.Float64Gauge, error) {
	return nil, nil
}
func (m *fakeMeter) Float64ObservableCounter(string, ...metric.Float64ObservableCounterOption) (metric.Float64ObservableCounter, error) {
	return nil, nil
}
func (m *fakeMeter) Float64ObservableUpDownCounter(string, ...metric.Float64ObservableUpDownCounterOption) (metric.Float64ObservableUpDownCounter, error) {
	return nil, nil
}
func (m *fakeMeter) Float64ObservableGauge(string, ...metric.Float64ObservableGaugeOption) (metric.Float64ObservableGauge, error) {
	return nil, nil
}
func (m *fakeMeter) RegisterCallback(metric.Callback, ...metric.Observable) (metric.Registration, error) {
	return nil, nil
}

type fakeInt64Counter struct {
	embedded.Int64Counter
	name  string
	adds  []int64
	attrs []attribute.Set // one per Add
}

func (c *fakeInt64Counter) Add(_ context.Context, v int64, opts ...metric.AddOption) {
	c.adds = append(c.adds, v)
	c.attrs = append(c.attrs, metric.NewAddConfig(opts).Attributes())
}
func (c *fakeInt64Counter) Enabled(context.Context) bool { return true }

type fakeFloat64Histogram struct {
	embedded.Float64Histogram
	name    string
	records []float64
}

func (h *fakeFloat64Histogram) Record(_ context.Context, v float64, _ ...metric.RecordOption) {
	h.records = append(h.records, v)
}
func (h *fakeFloat64Histogram) Enabled(context.Context) bool { return true }

func TestMetricsInterceptor_RecordsCallsAndDuration(t *testing.T) {
	m := newFakeMeter()
	icep := MetricsInterceptor(m)

	if got := m.int64CounterNames; len(got) != 2 || got[0] != "rpc.server.calls" || got[1] != "rpc.server.errors" {
		t.Errorf("counter names = %v, want [rpc.server.calls rpc.server.errors]", got)
	}
	if got := m.float64HistogramNames; len(got) != 1 || got[0] != "rpc.server.duration" {
		t.Errorf("histogram names = %v, want [rpc.server.duration]", got)
	}

	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&struct{}{}), nil
	})
	if _, err := wrapped(context.Background(), newTestRequest()); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := m.counters["rpc.server.calls"].adds; len(got) != 1 || got[0] != 1 {
		t.Errorf("calls adds = %v, want [1]", got)
	}
	if got := len(m.histograms["rpc.server.duration"].records); got != 1 {
		t.Errorf("duration records = %d, want 1", got)
	}
	if got := m.counters["rpc.server.errors"].adds; len(got) != 0 {
		t.Errorf("error counter incremented on success: %v", got)
	}
}

func TestMetricsInterceptor_RecordsErrors(t *testing.T) {
	m := newFakeMeter()
	icep := MetricsInterceptor(m)
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, errors.New("boom")
	})
	_, _ = wrapped(context.Background(), newTestRequest())
	if got := m.counters["rpc.server.errors"].adds; len(got) != 1 || got[0] != 1 {
		t.Errorf("errors adds = %v, want [1]", got)
	}
}

func TestMetricsInterceptor_NilMeterIsPassThrough(t *testing.T) {
	icep := MetricsInterceptor(nil)
	called := false
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return connect.NewResponse(&struct{}{}), nil
	})
	_, _ = wrapped(context.Background(), newTestRequest())
	if !called {
		t.Error("inner not invoked when meter is nil")
	}
}

func TestRecoveryInterceptor_RecoversFromPanic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	icep := RecoveryInterceptor(logger)
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		panic("kaboom")
	})

	resp, err := wrapped(context.Background(), newTestRequest())
	if err == nil {
		t.Fatal("expected error after panic")
	}
	if resp != nil {
		t.Errorf("expected nil resp on panic, got %v", resp)
	}
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", connect.CodeOf(err))
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Errorf("missing 'panic recovered' log: %q", buf.String())
	}
}

func TestRecoveryInterceptor_PanicWithErrorPreservesChain(t *testing.T) {
	icep := RecoveryInterceptor(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	inner := errors.New("inner-cause")
	wrapped := icep.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		panic(inner)
	})
	_, err := wrapped(context.Background(), newTestRequest())
	if !errors.Is(err, inner) {
		t.Errorf("errors.Is(err, inner) = false; expected wrapped chain")
	}
}

func TestRequestIDInterceptor_GeneratesIDWhenAbsent(t *testing.T) {
	icep := RequestIDInterceptor()
	var seen string
	wrapped := icep.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		seen = RequestIDFromContext(ctx)
		return connect.NewResponse(&struct{}{}), nil
	})
	resp, err := wrapped(context.Background(), newTestRequest())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if seen == "" {
		t.Error("expected request id on ctx")
	}
	if got := resp.Header().Get(RequestIDHeader); got != seen {
		t.Errorf("response %s = %q, want %q (echo)", RequestIDHeader, got, seen)
	}
}

func TestRequestIDInterceptor_TrustsInboundID(t *testing.T) {
	icep := RequestIDInterceptor()
	req := newTestRequest()
	req.Header().Set(RequestIDHeader, "rid-from-edge")

	var seen string
	wrapped := icep.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		seen = RequestIDFromContext(ctx)
		return connect.NewResponse(&struct{}{}), nil
	})
	if _, err := wrapped(context.Background(), req); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if seen != "rid-from-edge" {
		t.Errorf("expected inbound id to be trusted, got %q", seen)
	}
}

// TestContextWithRequestID_NilCtxSafe — the helper accepts nil ctx so
// non-HTTP entrypoints (CLI, workers) can use the same plumbing without
// having to remember to construct context.Background first.
func TestContextWithRequestID_NilCtxSafe(t *testing.T) {
	ctx := ContextWithRequestID(nil, "rid")
	if RequestIDFromContext(ctx) != "rid" {
		t.Errorf("nil-ctx with-id round-trip failed")
	}
}
