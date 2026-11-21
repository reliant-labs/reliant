package observe

import (
	"context"
	"crypto/subtle"
	"net/http"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// trustServerV2 serves the v2 test procedures behind ic, recording server
// spans into the returned recorder's provider, and the context the unary
// handler ran in.
func trustServerV2(t *testing.T, build func(...otelconnect.Option) (connectv2.ServerInterceptor, error)) (*tracetest.SpanRecorder, func() context.Context, *v2Server) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	ic, err := build(otelconnect.WithTracerProvider(tp), otelconnect.WithPropagator(prop))
	if err != nil {
		t.Fatal(err)
	}
	var last context.Context
	s := serveV2(t, v2Handlers{unary: func(ctx context.Context) error {
		last = ctx
		return nil
	}}, ic)
	return rec, func() context.Context { return last }, s
}

// callWithContextV2 makes one call carrying a traceparent for caller, a
// tracestate and a baggage member, plus extra headers.
func callWithContextV2(t *testing.T, s *v2Server, caller trace.SpanContext, extra map[string]string) {
	t.Helper()
	h := http.Header{}
	propagation.TraceContext{}.Inject(trace.ContextWithRemoteSpanContext(context.Background(), caller), propagation.HeaderCarrier(h))
	header := map[string]string{"Traceparent": h.Get("Traceparent"), "Tracestate": "vendor=caller-chosen", "Baggage": "org=someone-else"}
	for k, v := range extra {
		header[k] = v
	}
	if _, err := s.call(t.Context(), header); err != nil {
		t.Fatal(err)
	}
}

// Mirrors TestTrustRemoteFrom: an authenticated internal caller is
// continued; anyone else is linked, not continued. The predicate sees the
// peer's address.
func TestTrustRemoteFromV2(t *testing.T) {
	const secret = "s3cret"
	var peer string
	trusted := func(_ context.Context, peerAddr string, h HeaderReader) bool {
		peer = peerAddr
		return subtle.ConstantTimeCompare([]byte(h.Get("X-Internal-Token")), []byte(secret)) == 1
	}
	rec, _, s := trustServerV2(t, func(opts ...otelconnect.Option) (connectv2.ServerInterceptor, error) {
		return TrustRemoteFromV2(trusted, opts...)
	})
	call := func(token string) (trace.SpanContext, sdktrace.ReadOnlySpan) {
		t.Helper()
		caller := callerContext(true)
		before := len(rec.Ended())
		callWithContextV2(t, s, caller, map[string]string{"X-Internal-Token": token})
		ended := rec.Ended()
		if len(ended) != before+1 {
			t.Fatalf("server recorded %d spans, want 1", len(ended)-before)
		}
		return caller, ended[len(ended)-1]
	}

	caller, sp := call(secret)
	if sp.SpanContext().TraceID() != caller.TraceID() || sp.Parent().SpanID() != caller.SpanID() {
		t.Errorf("authenticated caller: server span is not the caller's child")
	}
	if peer == "" {
		t.Error("the predicate was given no peer address")
	}
	for _, token := range []string{"", "guess"} {
		caller, sp := call(token)
		if sp.SpanContext().TraceID() == caller.TraceID() {
			t.Errorf("token %q: server adopted an unauthenticated caller's trace", token)
		}
		linked := false
		for _, l := range sp.Links() {
			linked = linked || l.SpanContext.TraceID() == caller.TraceID()
		}
		if !linked {
			t.Errorf("token %q: server span carries no link to the caller's trace", token)
		}
	}
}

// Mirrors TestTrustRemoteFromNilTrustsNoOne: nil trusts no one — the caller
// is linked.
func TestTrustRemoteFromV2NilTrustsNoOne(t *testing.T) {
	rec, _, s := trustServerV2(t, func(opts ...otelconnect.Option) (connectv2.ServerInterceptor, error) {
		return TrustRemoteFromV2(nil, opts...)
	})
	caller := callerContext(true)
	callWithContextV2(t, s, caller, nil)
	if sp := rec.Ended()[0]; sp.SpanContext().TraceID() == caller.TraceID() {
		t.Error("a nil predicate adopted the caller's trace")
	}
}

// Mirrors TestTrustRemoteByTrustIDs.
func TestTrustRemoteByV2TrustIDs(t *testing.T) {
	ids := func(context.Context, string, HeaderReader) TraceTrust { return TrustIDs }
	build := func(opts ...otelconnect.Option) (connectv2.ServerInterceptor, error) {
		return TrustRemoteByV2(ids, opts...)
	}

	t.Run("ids adopted, tracestate and baggage dropped", func(t *testing.T) {
		rec, handlerCtx, s := trustServerV2(t, build)
		caller := callerContext(true)
		callWithContextV2(t, s, caller, nil)
		ended := rec.Ended()
		if len(ended) != 1 {
			t.Fatalf("server recorded %d spans, want 1", len(ended))
		}
		if ended[0].SpanContext().TraceID() != caller.TraceID() || ended[0].Parent().SpanID() != caller.SpanID() {
			t.Errorf("server span is not the caller's child")
		}
		if got := ended[0].SpanContext().TraceState().String(); got != "" {
			t.Errorf("server span adopted the caller's tracestate %q", got)
		}
		if got := baggage.FromContext(handlerCtx()).Len(); got != 0 {
			t.Errorf("handler context carries %d caller baggage members, want 0", got)
		}
	})
	t.Run("an unsampled caller cannot suppress recording", func(t *testing.T) {
		rec, _, s := trustServerV2(t, build)
		caller := callerContext(false)
		callWithContextV2(t, s, caller, nil)
		if ended := rec.Ended(); len(ended) != 1 || ended[0].SpanContext().TraceID() != caller.TraceID() {
			t.Errorf("want one recorded server span in the caller's trace, got %d", len(ended))
		}
	})
	t.Run("a sampled caller cannot force recording", func(t *testing.T) {
		t.Setenv(EnvTracesSampler, "parentbased_always_off")
		rec, handlerCtx, s := trustServerV2(t, build)
		caller := callerContext(true)
		callWithContextV2(t, s, caller, nil)
		if n := len(rec.Ended()); n != 0 {
			t.Errorf("server recorded %d spans, want 0: its own policy samples nothing", n)
		}
		if sc := trace.SpanContextFromContext(handlerCtx()); sc.TraceID() != caller.TraceID() || sc.IsSampled() {
			t.Errorf("handler span context = %v, want the caller's trace, unsampled", sc)
		}
	})
	t.Run("no traceparent starts a new trace", func(t *testing.T) {
		rec, _, s := trustServerV2(t, build)
		if _, err := s.call(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if ended := rec.Ended(); len(ended) != 1 || ended[0].Parent().IsValid() {
			t.Errorf("want one root server span, got %d spans", len(ended))
		}
	})
}

// Mirrors TestTrustRemoteByTrustAll.
func TestTrustRemoteByV2TrustAll(t *testing.T) {
	t.Setenv(EnvTracesSampler, "parentbased_always_off")
	rec, _, s := trustServerV2(t, func(opts ...otelconnect.Option) (connectv2.ServerInterceptor, error) {
		return TrustRemoteByV2(func(context.Context, string, HeaderReader) TraceTrust { return TrustAll }, opts...)
	})
	callWithContextV2(t, s, callerContext(true), nil)
	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("server recorded %d spans, want 1: TrustAll follows the caller's sampled flag", len(ended))
	}
	if got := ended[0].SpanContext().TraceState().String(); got != "vendor=caller-chosen" {
		t.Errorf("tracestate = %q, want the caller's", got)
	}
}
