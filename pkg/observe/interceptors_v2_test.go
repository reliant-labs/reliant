package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Mirrors TestLoggingInterceptor_EmitsRPCCompletedRecord /
// _EmitsErrorOnFailure, plus the stream records the v1 tests pin elsewhere.
func TestLoggingInterceptorV2_Records(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fail := false
	s := serveV2(t, v2Handlers{
		unary: func(context.Context) error {
			if fail {
				return svcerr.NotFound("widget")
			}
			return nil
		},
		stream: func(_ context.Context, send func() error) error {
			if fail {
				return svcerr.NotFound("widget")
			}
			return send()
		},
	}, LoggingInterceptorV2(logger), svcerr.InterceptorV2())

	if _, err := s.call(t.Context(), map[string]string{RequestIDHeader: "rid-1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.watch(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err := s.call(t.Context(), nil); v2Code(err) != connectv2.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
	if _, _, err := s.watch(t.Context(), nil); v2Code(err) != connectv2.CodeNotFound {
		t.Fatalf("stream err = %v, want not_found", err)
	}

	recs := jsonRecords(t, buf.String())
	ok := recordWithMsg(t, recs, "rpc completed")
	if ok["procedure"] != v2Unary || ok["duration"] == nil || ok["request_id"] != "rid-1" || ok["level"] != "INFO" {
		t.Errorf("rpc completed = %v, want procedure, duration, request_id=rid-1 at INFO", ok)
	}
	if r := recordWithMsg(t, recs, "stream completed"); r["procedure"] != v2Stream {
		t.Errorf("stream completed = %v", r)
	}
	failed := recordWithMsg(t, recs, "rpc failed")
	if failed["code"] != "not_found" || failed[ErrorClassKey] != "user" || failed["error"] == nil || failed["level"] != "INFO" {
		t.Errorf("rpc failed = %v, want code=not_found error_class=user with the error, at the user-error level", failed)
	}
	if r := recordWithMsg(t, recs, "stream failed"); r["code"] != "not_found" || r["procedure"] != v2Stream {
		t.Errorf("stream failed = %v", r)
	}
}

func TestLoggingInterceptorV2_NilLoggerSafe(t *testing.T) {
	s := serveV2(t, v2Handlers{}, LoggingInterceptorV2(nil))
	if _, err := s.call(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

// A server fault — a database down — logs at ERROR, as in
// TestLoggingInterceptor_DatabaseOutageLogsAtError.
func TestLoggingInterceptorV2_ServerFaultLogsAtError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	s := serveV2(t, v2Handlers{unary: func(context.Context) error {
		return svcerr.Wrap(errors.New("dial tcp 10.0.0.5:5432: connection refused"))
	}}, LoggingInterceptorV2(logger), svcerr.InterceptorV2())
	_, err := s.call(t.Context(), nil)
	if v2Code(err) != connectv2.CodeInternal || v2Message(err) != svcerr.InternalMessage {
		t.Fatalf("err = %v, want internal with the redacted message", err)
	}
	if r := recordWithMsg(t, jsonRecords(t, buf.String()), "rpc failed"); r["level"] != "ERROR" || r[ErrorClassKey] != "server" {
		t.Errorf("rpc failed = %v, want ERROR error_class=server", r)
	}
}

func TestTracingInterceptorV2_SpanPerRPC(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	fail := false
	s := serveV2(t, v2Handlers{unary: func(context.Context) error {
		if fail {
			return svcerr.Wrap(errors.New("boom"))
		}
		return nil
	}}, TracingInterceptorV2(tp.Tracer("test")), svcerr.InterceptorV2())

	if _, err := s.call(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.watch(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	fail = true
	_, _ = s.call(t.Context(), nil)

	ended := rec.Ended()
	if len(ended) != 3 {
		t.Fatalf("spans = %d, want 3", len(ended))
	}
	if ended[0].Name() != v2Unary || ended[1].Name() != v2Stream {
		t.Errorf("span names = %q, %q; want the procedures", ended[0].Name(), ended[1].Name())
	}
	if ended[0].Status().Code == codes.Error {
		t.Errorf("a successful call's span is Error")
	}
	if ended[2].Status().Code != codes.Error {
		t.Errorf("a server fault's span status = %v, want Error", ended[2].Status())
	}
}

func TestTracingAndMetricsInterceptorV2_NilIsPassThrough(t *testing.T) {
	for name, ic := range map[string]connectv2.ServerInterceptor{
		"tracing": TracingInterceptorV2(nil),
		"metrics": MetricsInterceptorV2(nil),
	} {
		if ic == nil {
			t.Fatalf("%s: nil dep returned a nil interceptor; connectv2.NewServer would panic on it", name)
		}
		s := serveV2(t, v2Handlers{}, ic)
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMetricsInterceptorV2_RecordsCallsErrorsAndDuration(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	fail := false
	s := serveV2(t, v2Handlers{unary: func(context.Context) error {
		if fail {
			return svcerr.NotFound("widget")
		}
		return nil
	}}, MetricsInterceptorV2(mp.Meter("test")), svcerr.InterceptorV2())

	if _, err := s.call(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	fail = true
	_, _ = s.call(t.Context(), nil)
	// A declared health probe on the health procedure records nothing; this
	// one is on another procedure, so it counts.
	_, _ = s.call(t.Context(), map[string]string{HeaderHealthProbe: "env-up"})

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]metricdata.Aggregation{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			got[m.Name] = m.Data
		}
	}
	calls, _ := got["rpc.server.calls"].(metricdata.Sum[int64])
	if len(calls.DataPoints) != 1 || calls.DataPoints[0].Value != 3 {
		t.Errorf("rpc.server.calls = %+v, want 3 for the procedure", calls.DataPoints)
	}
	if v, _ := calls.DataPoints[0].Attributes.Value("procedure"); v.AsString() != v2Unary {
		t.Errorf("procedure label = %q", v.AsString())
	}
	errs, _ := got["rpc.server.errors"].(metricdata.Sum[int64])
	if len(errs.DataPoints) != 1 || errs.DataPoints[0].Value != 2 {
		t.Fatalf("rpc.server.errors = %+v, want 2", errs.DataPoints)
	}
	attrs := errs.DataPoints[0].Attributes
	if c, _ := attrs.Value("code"); c.AsString() != "not_found" {
		t.Errorf("code label = %q, want not_found", c.AsString())
	}
	if c, _ := attrs.Value(attribute.Key(ErrorClassKey)); c.AsString() != "user" {
		t.Errorf("error_class label = %q, want user", c.AsString())
	}
	dur, _ := got["rpc.server.duration"].(metricdata.Histogram[float64])
	if len(dur.DataPoints) != 1 || dur.DataPoints[0].Count != 3 {
		t.Errorf("rpc.server.duration = %+v, want 3 samples", dur.DataPoints)
	}
}

// A declared health probe (IsHealthProbe) records no rpc.server.* sample; an
// undeclared health check does.
func TestMetricsInterceptorV2_SkipsDeclaredHealthProbes(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	srv := connectv2.NewServer(MetricsInterceptorV2(mp.Meter("test")))
	srv.Register(connectv2.Method{
		Spec:    connectv2.Spec{Procedure: HealthCheckProcedure, StreamType: connectv2.StreamTypeUnary},
		Handler: func(context.Context, connectv2.Spec, connectv2.ServerStream) error { return nil },
	})
	for _, probe := range []bool{true, false} {
		info := &connectv2.CallInfo{}
		if probe {
			info.RequestHeader().Set(HeaderHealthProbe, "env-up")
		}
		if err := srv.Call(t.Context(), HealthCheckProcedure, info, nil); err != nil {
			t.Fatal(err)
		}
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "rpc.server.calls" {
				continue
			}
			if dps := m.Data.(metricdata.Sum[int64]).DataPoints; len(dps) != 1 || dps[0].Value != 1 {
				t.Errorf("rpc.server.calls = %+v, want 1: the undeclared check only", dps)
			}
			return
		}
	}
	t.Fatal("no rpc.server.calls recorded")
}

// Mirrors TestRecoveryInterceptor_RecoversFromPanic and
// _PanicWithErrorPreservesChain: the client sees CodeInternal with the
// redacted message; the server keeps the panic value as the cause.
func TestRecoveryInterceptorV2_RecoversFromPanic(t *testing.T) {
	sentinel := errors.New("nil map write")
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	var seen error
	capture := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			seen = next(ctx, spec, stream)
			return seen
		}
	}
	s := serveV2(t, v2Handlers{
		unary:  func(context.Context) error { panic(sentinel) },
		stream: func(context.Context, func() error) error { panic("stream boom") },
	}, capture, RecoveryInterceptorV2(logger))

	_, err := s.call(t.Context(), nil)
	if v2Code(err) != connectv2.CodeInternal || v2Message(err) != svcerr.InternalMessage {
		t.Fatalf("err = %v, want internal %q", err, svcerr.InternalMessage)
	}
	if strings.Contains(err.Error(), "nil map") {
		t.Errorf("the panic detail reached the client: %v", err)
	}
	if !errors.Is(seen, sentinel) {
		t.Errorf("server-side error %v lost the panic value", seen)
	}
	if _, _, err := s.watch(t.Context(), nil); v2Code(err) != connectv2.CodeInternal {
		t.Fatalf("stream err = %v, want internal", err)
	}
	recs := jsonRecords(t, buf.String())
	if r := recordWithMsg(t, recs, "panic recovered"); r["procedure"] != v2Unary {
		t.Errorf("panic recovered = %v", r)
	}
	if r := recordWithMsg(t, recs, "panic recovered in stream"); r["procedure"] != v2Stream {
		t.Errorf("panic recovered in stream = %v", r)
	}
}

// Mirrors TestRequestIDInterceptor_GeneratesIDWhenAbsent / _TrustsInboundID,
// and pins v1's echo rules: an id from the context is not echoed, a failed
// unary call echoes none, every stream echoes.
func TestRequestIDInterceptorV2(t *testing.T) {
	var inHandler string
	fail := false
	h := v2Handlers{
		unary: func(ctx context.Context) error {
			inHandler = RequestIDFromContext(ctx)
			if fail {
				return svcerr.NotFound("widget")
			}
			return nil
		},
		stream: func(ctx context.Context, send func() error) error {
			inHandler = RequestIDFromContext(ctx)
			return send()
		},
	}
	s := serveV2(t, h, RequestIDInterceptorV2(), svcerr.InterceptorV2())

	t.Run("generated when absent", func(t *testing.T) {
		info, err := s.call(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		got := info.ResponseHeader().Get(RequestIDHeader)
		if len(got) != 32 || got != inHandler {
			t.Errorf("echo = %q, handler saw %q; want one 32-hex id", got, inHandler)
		}
	})
	t.Run("inbound trusted", func(t *testing.T) {
		info, err := s.call(t.Context(), map[string]string{RequestIDHeader: "from-proxy"})
		if err != nil {
			t.Fatal(err)
		}
		if got := info.ResponseHeader().Values(RequestIDHeader); len(got) != 1 || got[0] != "from-proxy" || inHandler != "from-proxy" {
			t.Errorf("echo = %v, handler %q; want exactly from-proxy", got, inHandler)
		}
	})
	t.Run("stream echoes", func(t *testing.T) {
		info, _, err := s.watch(t.Context(), map[string]string{RequestIDHeader: "rid-stream"})
		if err != nil {
			t.Fatal(err)
		}
		if got := info.ResponseHeader().Get(RequestIDHeader); got != "rid-stream" {
			t.Errorf("stream echo = %q", got)
		}
	})
	t.Run("failed unary call echoes none", func(t *testing.T) {
		fail = true
		defer func() { fail = false }()
		info, err := s.call(t.Context(), map[string]string{RequestIDHeader: "rid-fail"})
		if v2Code(err) != connectv2.CodeNotFound {
			t.Fatal(err)
		}
		if got := info.ResponseHeader().Get(RequestIDHeader); got != "" {
			t.Errorf("failed call echoed %q; v1 echoes only on success", got)
		}
	})
	t.Run("id from the context is not echoed", func(t *testing.T) {
		edge := func(next connectv2.ServerFunc) connectv2.ServerFunc {
			return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
				return next(ContextWithRequestID(ctx, "edge-id"), spec, stream)
			}
		}
		s := serveV2(t, h, edge, RequestIDInterceptorV2())
		info, err := s.call(t.Context(), map[string]string{RequestIDHeader: "client-id"})
		if err != nil {
			t.Fatal(err)
		}
		if inHandler != "edge-id" || info.ResponseHeader().Get(RequestIDHeader) != "" {
			t.Errorf("handler %q, echo %q; want edge-id adopted and no echo", inHandler, info.ResponseHeader().Get(RequestIDHeader))
		}
	})
}

// Mirrors TestTraceResponseNamesTheServerSpan, for a unary success, a unary
// failure and a stream.
func TestTraceResponseInterceptorV2_NamesTheServerSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otelIC, err := otelconnect.NewServerInterceptor(otelconnect.WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}
	fail := false
	s := serveV2(t, v2Handlers{unary: func(context.Context) error {
		if fail {
			return connectv2.NewError(connectv2.CodeFailedPrecondition, "not yet")
		}
		return nil
	}}, otelIC, TraceResponseInterceptorV2())

	lastServerSpan := func() string {
		t.Helper()
		ended := rec.Ended()
		for i := len(ended) - 1; i >= 0; i-- {
			if ended[i].SpanKind() == trace.SpanKindServer {
				return TraceResponseValue(ended[i].SpanContext())
			}
		}
		t.Fatal("no server span recorded")
		return ""
	}
	info, err := s.call(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.ResponseHeader().Get(TraceResponseHeader), lastServerSpan(); got == "" || got != want {
		t.Errorf("success: traceresponse = %q, want %q", got, want)
	}
	info, _, err = s.watch(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.ResponseHeader().Get(TraceResponseHeader), lastServerSpan(); got == "" || got != want {
		t.Errorf("stream: traceresponse = %q, want %q", got, want)
	}
	fail = true
	info, err = s.call(t.Context(), nil)
	if v2Code(err) != connectv2.CodeFailedPrecondition {
		t.Fatalf("err = %v", err)
	}
	if got, want := info.ResponseHeader().Get(TraceResponseHeader), lastServerSpan(); got == "" || got != want {
		t.Errorf("failure: traceresponse = %q, want %q", got, want)
	}
}

// Mirrors TestRequestAndIdentitySpans: session.id from the baggage and
// user.id from the authenticated caller land on the server span.
func TestRequestAndIdentitySpansV2(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	otelIC, err := otelconnect.NewServerInterceptor(otelconnect.WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}
	auth := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			return next(authn.ContextWithClaims(ctx, &authn.Claims{UserID: "u-42"}), spec, stream)
		}
	}
	s := serveV2(t, v2Handlers{}, otelIC, RequestSpanInterceptorV2(), auth, IdentitySpanInterceptorV2(nil))
	const session = "0123456789abcdef0123456789abcdef"
	if _, err := s.call(t.Context(), map[string]string{"Baggage": "other=x," + AttrSessionID + "=" + session}); err != nil {
		t.Fatal(err)
	}
	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d, want 1", len(ended))
	}
	attrs := map[attribute.Key]string{}
	for _, kv := range ended[0].Attributes() {
		attrs[kv.Key] = kv.Value.Emit()
	}
	if attrs[AttrSessionID] != session || attrs[AttrUserID] != "u-42" {
		t.Errorf("server span attrs = %v, want session.id and user.id", attrs)
	}
}

// Mirrors TestEdgeContextInterceptorReadsConnectHeaders.
func TestEdgeContextInterceptorV2ReadsHeaders(t *testing.T) {
	var seen EdgeContext
	h := v2Handlers{unary: func(ctx context.Context) error {
		seen = EdgeContextFrom(ctx)
		return nil
	}}
	header := map[string]string{HeaderVisitorDay: testVisitorDay}
	if _, err := serveV2(t, h, edgeContextInterceptorV2(true)).call(t.Context(), header); err != nil {
		t.Fatal(err)
	}
	if seen.VisitorDay != testVisitorDay {
		t.Errorf("trusted: %+v", seen)
	}
	seen = EdgeContext{}
	if _, err := serveV2(t, h, edgeContextInterceptorV2(false)).call(t.Context(), header); err != nil {
		t.Fatal(err)
	}
	if seen != (EdgeContext{}) {
		t.Errorf("untrusted: %+v", seen)
	}
}

// Mirrors the heartbeat harness tests: one auth_signed_in {kind: resumed} per
// user per UTC day, none for an anonymous caller. The heartbeat gets its own
// memory and a fixed clock, as the v1 harness's does, so neither another
// test's callers nor a real midnight can move the count.
func TestAuthHeartbeatInterceptorV2RecordsOncePerUserPerUTCDay(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	user := ""
	claims := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			ctx = WithLogger(ctx, logger)
			if user != "" {
				ctx = authn.ContextWithClaims(ctx, &authn.Claims{UserID: user})
			}
			return next(ctx, spec, stream)
		}
	}
	now := time.Date(2026, 11, 10, 23, 59, 0, 0, time.UTC)
	hb := AuthHeartbeatInterceptor(nil, "supabase").(authHeartbeat)
	hb.seen = &resumedSet{}
	hb.now = func() time.Time { return now }
	s := serveV2(t, v2Handlers{}, claims, authHeartbeatV2(hb))
	events := func() []map[string]any {
		var out []map[string]any
		for _, r := range jsonRecords(t, buf.String()) {
			if r["product.provider"] != nil {
				out = append(out, r)
			}
		}
		return out
	}
	for _, u := range []string{"", "u-1", "u-1"} {
		user = u
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if evs := events(); len(evs) != 1 || evs[0]["product.provider"] != "supabase" {
		t.Fatalf("events = %v, want one auth_signed_in for u-1 with provider supabase", evs)
	}
	now = now.Add(2 * time.Minute) // past midnight UTC
	if _, err := s.call(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if evs := events(); len(evs) != 2 {
		t.Fatalf("events = %v, want a second auth_signed_in on the next UTC day", evs)
	}
}

// Mirrors deadline_origin_test.go for Deadline.InterceptorV2: its own budget
// is marked server, the caller's is marked caller, a stream is bounded only
// when named, and the watchdog reports a slow call.
func TestDeadlineInterceptorV2(t *testing.T) {
	stall := func(ctx context.Context) error {
		<-ctx.Done()
		return svcerr.Wrap(fmt.Errorf("update orders: %w", ctx.Err()))
	}
	var got error
	capture := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			got = next(ctx, spec, stream)
			return got
		}
	}

	t.Run("own expiry is the server's", func(t *testing.T) {
		d := Deadline{Unary: 20 * time.Millisecond, SlowAfter: -1, ExpiryIsError: true}
		s := serveV2(t, v2Handlers{unary: stall}, capture, d.InterceptorV2(), svcerr.InterceptorV2())
		_, err := s.call(t.Context(), nil)
		if v2Code(err) != connectv2.CodeDeadlineExceeded {
			t.Fatalf("wire err = %v, want deadline_exceeded", err)
		}
		if o := deadlineOf(context.Background(), got).String(); o != DeadlineServer {
			t.Errorf("deadline = %q, want %q", o, DeadlineServer)
		}
		if l := LevelForError(got); l != slog.LevelError {
			t.Errorf("LevelForError = %v, want ERROR under ExpiryIsError", l)
		}
	})
	t.Run("caller's expiry is the caller's", func(t *testing.T) {
		callerDeadline := func(next connectv2.ServerFunc) connectv2.ServerFunc {
			return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
				ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
				return next(ctx, spec, stream)
			}
		}
		s := serveV2(t, v2Handlers{unary: stall}, capture, callerDeadline, Deadline{SlowAfter: -1}.InterceptorV2(), svcerr.InterceptorV2())
		_, _ = s.call(t.Context(), nil)
		if o := deadlineOf(context.Background(), got).String(); o != DeadlineCaller {
			t.Errorf("deadline = %q, want %q", o, DeadlineCaller)
		}
	})
	t.Run("a stream is bounded only when named", func(t *testing.T) {
		stallStream := func(ctx context.Context, _ func() error) error {
			select {
			case <-ctx.Done():
				return svcerr.Wrap(ctx.Err())
			case <-time.After(100 * time.Millisecond):
				return nil
			}
		}
		s := serveV2(t, v2Handlers{stream: stallStream}, Deadline{Unary: time.Millisecond}.InterceptorV2(), svcerr.InterceptorV2())
		if _, _, err := s.watch(t.Context(), nil); err != nil {
			t.Fatalf("unnamed stream got a deadline: %v", err)
		}
		named := Deadline{Procedures: map[string]time.Duration{v2Stream: 10 * time.Millisecond}}
		s = serveV2(t, v2Handlers{stream: stallStream}, named.InterceptorV2(), svcerr.InterceptorV2())
		if _, _, err := s.watch(t.Context(), nil); v2Code(err) != connectv2.CodeDeadlineExceeded {
			t.Fatalf("named stream err = %v, want deadline_exceeded", err)
		}
	})
	t.Run("watchdog reports a slow call", func(t *testing.T) {
		reports := make(chan SlowCall, 8)
		d := Deadline{SlowAfter: 5 * time.Millisecond, SlowRepeat: time.Hour, OnSlow: func(_ context.Context, c SlowCall) { reports <- c }}
		var inFlight SlowCall
		// The handler runs until the watchdog has reported it, so the
		// in-flight report cannot race the call's end.
		s := serveV2(t, v2Handlers{unary: func(context.Context) error {
			inFlight = <-reports
			return nil
		}}, d.InterceptorV2())
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		done := <-reports
		if inFlight.Procedure != v2Unary || inFlight.Done || !done.Done || done.Err != nil {
			t.Errorf("reports = %+v, %+v; want in-flight then done", inFlight, done)
		}
	})
}
