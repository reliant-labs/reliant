package observe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// handlerMarker is a unary body that records "handler" on trace.
func handlerMarker(trace *[]string) v2Handlers {
	return v2Handlers{unary: func(context.Context) error {
		*trace = append(*trace, "handler")
		return nil
	}}
}

// Mirrors TestChain_ThreadsExplicitCollaborators.
func TestChainV2_ThreadsExplicitCollaborators(t *testing.T) {
	var trace []string
	chain := ChainV2(DepsV2{
		Auth:      markerV2("auth", &trace),
		Audit:     markerV2("audit", &trace),
		RateLimit: markerV2("ratelimit", &trace),
		Extras:    []connectv2.ServerInterceptor{markerV2("extra", &trace)},
	})
	if _, err := serveV2(t, handlerMarker(&trace), chain...).call(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(trace, ","), "auth,audit,ratelimit,extra,handler"; got != want {
		t.Fatalf("trace = %s, want %s", got, want)
	}
}

// Mirrors TestChain_NilCollaboratorsSkipped. A v2 chain can hold no nil func
// (connectv2.NewServer would panic on it), so a nil RateLimit — what
// middleware.RateLimitInterceptorV2 returns when rate limiting is off — and a
// nil Extras entry drop out.
func TestChainV2_NilCollaboratorsSkipped(t *testing.T) {
	var trace []string
	chain := ChainV2(DepsV2{RateLimit: nil, Extras: []connectv2.ServerInterceptor{nil}})
	// observability (recovery, request-id, logging, tracing, metrics), the
	// default Deadline slot, svcerr innermost.
	if len(chain) != 7 {
		t.Errorf("nil-collaborator chain length = %d, want 7", len(chain))
	}
	for i, ic := range chain {
		if ic == nil {
			t.Fatalf("chain[%d] is nil", i)
		}
	}
	if _, err := serveV2(t, handlerMarker(&trace), chain...).call(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if len(trace) != 1 {
		t.Fatalf("trace = %v, want [handler]", trace)
	}
}

// Mirrors TestChain_OuterInterceptorsSeeWireError and
// TestChain_SvcerrReachesClientWithCode: svcerr runs inside every Extra, and
// a handler's svcerr error reaches the client with its code and message.
func TestChainV2_SvcerrInnermost(t *testing.T) {
	var sawConnect bool
	spy := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			err := next(ctx, spec, stream)
			var ce *connectv2.Error
			sawConnect = errors.As(err, &ce)
			return err
		}
	}
	s := serveV2(t, v2Handlers{unary: func(context.Context) error { return svcerr.PermissionDenied("nope") }},
		ChainV2(DepsV2{Logger: slog.New(slog.DiscardHandler), Extras: []connectv2.ServerInterceptor{spy}})...)
	_, err := s.call(t.Context(), nil)
	if v2Code(err) != connectv2.CodePermissionDenied || v2Message(err) != "nope" {
		t.Fatalf("err = %v, want permission_denied: nope", err)
	}
	if !sawConnect {
		t.Fatal("an Extras interceptor saw the raw sentinel; svcerr must sit inside every Extra")
	}
}

// syncBuffer is a bytes.Buffer safe for the watchdog's goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The Deadline slot: configured by DepsV2.Deadline (and given the chain's
// Logger), replaced in place by DeadlineOverride, dropped by NoDeadline.
func TestChainV2_DeadlineSlot(t *testing.T) {
	t.Run("default bounds a unary call", func(t *testing.T) {
		var deadline time.Time
		var ok bool
		s := serveV2(t, v2Handlers{unary: func(ctx context.Context) error {
			deadline, ok = ctx.Deadline()
			return nil
		}}, ChainV2(DepsV2{Logger: slog.New(slog.DiscardHandler)})...)
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if !ok || time.Until(deadline) > DefaultUnaryDeadline {
			t.Errorf("handler deadline = %v (set %v), want within DefaultUnaryDeadline", deadline, ok)
		}
	})
	t.Run("configured budget expires as the server's", func(t *testing.T) {
		var buf syncBuffer
		s := serveV2(t, v2Handlers{unary: func(ctx context.Context) error {
			<-ctx.Done()
			return svcerr.Wrap(ctx.Err())
		}}, ChainV2(DepsV2{
			Logger:   slog.New(slog.NewJSONHandler(&buf, nil)),
			Deadline: Deadline{Unary: 20 * time.Millisecond, SlowAfter: -1},
		})...)
		if _, err := s.call(t.Context(), nil); v2Code(err) != connectv2.CodeDeadlineExceeded {
			t.Fatalf("err = %v, want deadline_exceeded", err)
		}
		if r := recordWithMsg(t, jsonRecords(t, buf.String()), "rpc failed"); r[DeadlineKey] != DeadlineServer || r["level"] != "WARN" {
			t.Errorf("rpc failed = %v, want WARN with deadline=server", r)
		}
	})
	t.Run("a Deadline naming no Logger logs to the chain's", func(t *testing.T) {
		var buf syncBuffer
		s := serveV2(t, v2Handlers{unary: func(ctx context.Context) error {
			for !strings.Contains(buf.String(), "rpc still in flight") {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
			return nil
		}}, ChainV2(DepsV2{
			Logger:   slog.New(slog.NewJSONHandler(&buf, nil)),
			Deadline: Deadline{SlowAfter: time.Millisecond},
		})...)
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Fatalf("the slow-call report never reached the chain's logger: %v", err)
		}
	})
	t.Run("override runs in the slot's position", func(t *testing.T) {
		var trace []string
		s := serveV2(t, handlerMarker(&trace), ChainV2(DepsV2{
			DeadlineOverride: markerV2("deadline", &trace),
			Auth:             markerV2("auth", &trace),
		})...)
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(trace, ","); got != "deadline,auth,handler" {
			t.Errorf("trace = %s, want deadline,auth,handler", got)
		}
	})
	t.Run("NoDeadline drops the slot", func(t *testing.T) {
		var ok bool
		chain := ChainV2(DepsV2{NoDeadline: true, DeadlineOverride: markerV2("ignored", new([]string))})
		if len(chain) != 6 {
			t.Errorf("chain length = %d, want 6 (observability + svcerr)", len(chain))
		}
		s := serveV2(t, v2Handlers{unary: func(ctx context.Context) error {
			_, ok = ctx.Deadline()
			return nil
		}}, chain...)
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("NoDeadline: the handler still ran under a deadline")
		}
	})
}

// Mirrors TestChain_TraceCoversAuthRejectionAndItsLogLine and
// TestChain_TraceSitsOutsideEverythingElse: the Trace slot is outermost, so a
// call auth rejects still has its span, and its "rpc failed" line carries it.
func TestChainV2_TraceCoversAuthRejectionAndItsLogLine(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otelIC, err := otelconnect.NewServerInterceptor(otelconnect.WithTracerProvider(tp), otelconnect.WithPropagator(propagation.TraceContext{}), otelconnect.WithTrustRemote())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	reject := func(connectv2.ServerFunc) connectv2.ServerFunc {
		return func(context.Context, connectv2.Spec, connectv2.ServerStream) error {
			return connectv2.NewError(connectv2.CodeUnauthenticated, "missing Authorization header")
		}
	}
	handlerRan := false
	s := serveV2(t, v2Handlers{unary: func(context.Context) error {
		handlerRan = true
		return nil
	}}, ChainV2(DepsV2{Logger: logger, Trace: otelIC, Auth: reject})...)

	const browserTrace = "0af7651916cd43dd8448eb211c80319c"
	const browserSpan = "b7ad6b7169203331"
	_, err = s.call(t.Context(), map[string]string{"traceparent": "00-" + browserTrace + "-" + browserSpan + "-01"})
	if v2Code(err) != connectv2.CodeUnauthenticated {
		t.Fatalf("err = %v, want unauthenticated", err)
	}
	if handlerRan {
		t.Fatal("auth rejected the call, yet the handler ran")
	}
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("a rejected call must still have its span: got %d spans, want 1", len(ended))
	}
	sp := ended[0]
	if sp.SpanContext().TraceID().String() != browserTrace || sp.Parent().SpanID().String() != browserSpan {
		t.Errorf("span = %s parent %s, want the caller's trace %s / span %s", sp.SpanContext().TraceID(), sp.Parent().SpanID(), browserTrace, browserSpan)
	}
	failed := recordWithMsg(t, jsonRecords(t, logs.String()), "rpc failed")
	if failed["trace_id"] != browserTrace || failed["span_id"] != sp.SpanContext().SpanID().String() {
		t.Errorf("the \"rpc failed\" line must carry the span it ran in, got %v", failed)
	}
}

// DepsV2.Caller is how the identity layers read a caller kept in a context of
// the app's own (authn.Policy.ClaimsReader for authn.NewInterceptorV2).
func TestChainV2_CallerFeedsTheIdentitySpan(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	otelIC, err := otelconnect.NewServerInterceptor(otelconnect.WithTracerProvider(tp))
	if err != nil {
		t.Fatal(err)
	}
	type ownKey struct{}
	auth := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			return next(context.WithValue(ctx, ownKey{}, &authn.Claims{UserID: "u-own"}), spec, stream)
		}
	}
	caller := func(ctx context.Context) (*authn.Claims, bool) {
		c, ok := ctx.Value(ownKey{}).(*authn.Claims)
		return c, ok
	}
	s := serveV2(t, v2Handlers{}, ChainV2(DepsV2{Logger: slog.New(slog.DiscardHandler), Trace: otelIC, Auth: auth, Caller: caller})...)
	if _, err := s.call(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var userID string
	for _, kv := range spans.Ended()[0].Attributes() {
		if kv.Key == attribute.Key(AttrUserID) {
			userID = kv.Value.AsString()
		}
	}
	if userID != "u-own" {
		t.Errorf("server span user.id = %q, want u-own from DepsV2.Caller", userID)
	}
}

// Mirrors the DefaultMiddlewares contract: a fixed observability prefix with
// nil deps, Extras after it, svcerr innermost.
func TestDefaultMiddlewaresV2(t *testing.T) {
	var trace []string
	chain := DefaultMiddlewaresV2(DefaultMiddlewareDepsV2{
		Logger: slog.New(slog.DiscardHandler),
		Extras: []connectv2.ServerInterceptor{nil, markerV2("extra", &trace)},
	})
	if len(chain) != 7 {
		t.Errorf("chain length = %d, want 7 (5 observability + extra + svcerr)", len(chain))
	}
	s := serveV2(t, v2Handlers{unary: func(context.Context) error { return svcerr.NotFound("widget") }}, chain...)
	if _, err := s.call(t.Context(), nil); v2Code(err) != connectv2.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
	if len(trace) != 1 {
		t.Errorf("extras ran %v, want once", trace)
	}
}

// The parity proof: one rpc served behind Chain (connect-go v1) and behind
// ChainV2 (v2) reaches the wire with the same error, writes the same log
// record and opens a server span of the same name, status and error class —
// for a user error, a server fault, an unwrapped error and a panic.
func TestChainV2MatchesChain(t *testing.T) {
	type outcome struct {
		code    uint32
		message string
		record  map[string]any
		span    string
		status  string
		class   string
	}
	// record is the failure's log record without the fields that differ per
	// call or per stack: a panic's record is recovery's, written outside
	// logging.
	record := func(t *testing.T, logs *bytes.Buffer, panicked bool) map[string]any {
		t.Helper()
		msg := "rpc failed"
		if panicked {
			msg = "panic recovered"
		}
		out := map[string]any{}
		for k, v := range recordWithMsg(t, jsonRecords(t, logs.String()), msg) {
			switch k {
			case "time", "duration", "request_id", "trace_id", "span_id", LogKeyExceptionStacktrace, "source":
			default:
				out[k] = v
			}
		}
		return out
	}
	settle := func(t *testing.T, logs *bytes.Buffer, spans *tracetest.SpanRecorder, panicked bool, code uint32, msg string) outcome {
		t.Helper()
		o := outcome{code: code, message: msg, record: record(t, logs, panicked)}
		ended := spans.Ended()
		if len(ended) != 1 {
			t.Fatalf("spans = %d, want 1", len(ended))
		}
		o.span, o.status = ended[0].Name(), ended[0].Status().Code.String()
		for _, kv := range ended[0].Attributes() {
			if kv.Key == attribute.Key(ErrorClassKey) {
				o.class = kv.Value.AsString()
			}
		}
		return o
	}

	v1 := func(t *testing.T, fail func() error, panicked bool) outcome {
		var logs bytes.Buffer
		spans := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
		otelIC, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp))
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle(v2Unary, connect.NewUnaryHandler(v2Unary,
			func(context.Context, *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
				return nil, fail()
			},
			connect.WithInterceptors(Chain(Deps{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Trace: otelIC})...),
		))
		hs := httptest.NewServer(mux)
		t.Cleanup(hs.Close)
		_, err = connect.NewClient[structpb.Struct, structpb.Struct](hs.Client(), hs.URL+v2Unary).CallUnary(t.Context(), connect.NewRequest(&structpb.Struct{}))
		var ce *connect.Error
		if !errors.As(err, &ce) {
			t.Fatalf("v1 err = %v", err)
		}
		return settle(t, &logs, spans, panicked, uint32(ce.Code()), ce.Message())
	}
	v2 := func(t *testing.T, fail func() error, panicked bool) outcome {
		var logs bytes.Buffer
		spans := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
		otelIC, err := otelconnect.NewServerInterceptor(otelconnect.WithTracerProvider(tp))
		if err != nil {
			t.Fatal(err)
		}
		s := serveV2(t, v2Handlers{unary: func(context.Context) error { return fail() }},
			ChainV2(DepsV2{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Trace: otelIC})...)
		_, err = s.call(t.Context(), nil)
		return settle(t, &logs, spans, panicked, uint32(v2Code(err)), v2Message(err))
	}

	for _, tc := range []struct {
		name     string
		fail     func() error
		panicked bool
	}{
		{"user error", func() error { return svcerr.NotFound("widget") }, false},
		{"server fault", func() error { return svcerr.Wrap(errors.New("dial tcp: connection refused")) }, false},
		{"unwrapped error", func() error { return errors.New("forgot to wrap") }, false},
		{"panic", func() error { panic("boom") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := v1(t, tc.fail, tc.panicked), v2(t, tc.fail, tc.panicked)
			if a.code != b.code || a.message != b.message {
				t.Errorf("wire: v1 %d %q, v2 %d %q", a.code, a.message, b.code, b.message)
			}
			if a.span != b.span || a.status != b.status || a.class != b.class {
				t.Errorf("span: v1 %s %s class=%s, v2 %s %s class=%s", a.span, a.status, a.class, b.span, b.status, b.class)
			}
			if len(a.record) != len(b.record) {
				t.Errorf("log record:\n v1 %v\n v2 %v", a.record, b.record)
			}
			for k, v := range a.record {
				if b.record[k] != v {
					t.Errorf("log record %q: v1 %v, v2 %v", k, v, b.record[k])
				}
			}
		})
	}
}
