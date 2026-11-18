package observe

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/structpb"
)

const trustProcedure = "/test.v1.TrustService/Ping"

// The server continues an authenticated internal caller's trace, and starts a
// new trace linked to the caller's for anyone else — including a caller that
// merely claims to be internal.
func TestTrustRemoteFrom(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prop := propagation.TraceContext{}

	const secret = "s3cret"
	trusted := func(_ context.Context, _ connect.Peer, h http.Header) bool {
		return subtle.ConstantTimeCompare([]byte(h.Get("X-Internal-Token")), []byte(secret)) == 1
	}
	interceptor, err := TrustRemoteFrom(trusted, otelconnect.WithTracerProvider(tp), otelconnect.WithPropagator(prop))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(trustProcedure, connect.NewUnaryHandler(trustProcedure,
		func(context.Context, *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			return connect.NewResponse(&structpb.Struct{}), nil
		}, connect.WithInterceptors(interceptor)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	call := func(token string) (caller trace.SpanContext, server sdktrace.ReadOnlySpan) {
		t.Helper()
		_, span := sdktrace.NewTracerProvider().Tracer("caller").Start(context.Background(), "caller")
		defer span.End()
		req := connect.NewRequest(&structpb.Struct{})
		prop.Inject(trace.ContextWithSpan(context.Background(), span), propagation.HeaderCarrier(req.Header()))
		if token != "" {
			req.Header().Set("X-Internal-Token", token)
		}
		before := len(rec.Ended())
		client := connect.NewClient[structpb.Struct, structpb.Struct](srv.Client(), srv.URL+trustProcedure)
		if _, err := client.CallUnary(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		ended := rec.Ended()
		if len(ended) != before+1 {
			t.Fatalf("server recorded %d spans, want 1", len(ended)-before)
		}
		return span.SpanContext(), ended[len(ended)-1]
	}

	t.Run("authenticated internal caller is continued", func(t *testing.T) {
		caller, s := call(secret)
		if s.SpanContext().TraceID() != caller.TraceID() || s.Parent().SpanID() != caller.SpanID() {
			t.Errorf("server span is not the caller's child")
		}
	})
	for name, token := range map[string]string{"no credential": "", "wrong credential": "guess"} {
		t.Run(name+" is linked, not continued", func(t *testing.T) {
			caller, s := call(token)
			if s.SpanContext().TraceID() == caller.TraceID() {
				t.Errorf("server adopted an unauthenticated caller's trace")
			}
			linked := false
			for _, l := range s.Links() {
				linked = linked || l.SpanContext.TraceID() == caller.TraceID()
			}
			if !linked {
				t.Errorf("server span carries no link to the caller's trace")
			}
		})
	}
}

func TestTrustRemoteFromNilTrustsNoOne(t *testing.T) {
	i, err := TrustRemoteFrom(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, isTrust := i.(*trustRemoteInterceptor); isTrust {
		t.Error("a nil predicate built the per-request interceptor")
	}
}

// trustIDsServer serves trustProcedure behind TrustRemoteBy(classify), and
// records the server spans and the context the handler ran in.
func trustIDsServer(t *testing.T, classify TraceTrustFunc) (rec *tracetest.SpanRecorder, handlerCtx func() context.Context, url string) {
	t.Helper()
	rec = tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	interceptor, err := TrustRemoteBy(classify, otelconnect.WithTracerProvider(tp), otelconnect.WithPropagator(prop))
	if err != nil {
		t.Fatal(err)
	}
	var last context.Context
	mux := http.NewServeMux()
	mux.Handle(trustProcedure, connect.NewUnaryHandler(trustProcedure,
		func(ctx context.Context, _ *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			last = ctx
			return connect.NewResponse(&structpb.Struct{}), nil
		}, connect.WithInterceptors(interceptor)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return rec, func() context.Context { return last }, srv.URL
}

// callWithContext makes one call carrying a traceparent for caller (with its
// flags), a tracestate and a baggage member, as a browser or a hostile client
// could.
func callWithContext(t *testing.T, url string, caller trace.SpanContext) {
	t.Helper()
	req := connect.NewRequest(&structpb.Struct{})
	propagation.TraceContext{}.Inject(trace.ContextWithRemoteSpanContext(context.Background(), caller), propagation.HeaderCarrier(req.Header()))
	req.Header().Set("Tracestate", "vendor=caller-chosen")
	req.Header().Set("Baggage", "org=someone-else")
	client := connect.NewClient[structpb.Struct, structpb.Struct](http.DefaultClient, url+trustProcedure)
	if _, err := client.CallUnary(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func callerContext(sampled bool) trace.SpanContext {
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		TraceFlags: trace.TraceFlags(0).WithSampled(sampled),
		Remote:     true,
	})
}

// TrustIDs continues an authenticated end user's trace — the same trace id,
// the caller's span as parent — and adopts nothing else it claims: the
// sampling decision is the server's, and its tracestate and baggage are gone.
func TestTrustRemoteByTrustIDs(t *testing.T) {
	ids := func(context.Context, connect.Peer, http.Header) TraceTrust { return TrustIDs }

	t.Run("ids adopted, tracestate and baggage dropped", func(t *testing.T) {
		rec, handlerCtx, url := trustIDsServer(t, ids)
		caller := callerContext(true)
		callWithContext(t, url, caller)
		ended := rec.Ended()
		if len(ended) != 1 {
			t.Fatalf("server recorded %d spans, want 1", len(ended))
		}
		s := ended[0]
		if s.SpanContext().TraceID() != caller.TraceID() || s.Parent().SpanID() != caller.SpanID() {
			t.Errorf("server span is not the caller's child")
		}
		if got := s.SpanContext().TraceState().String(); got != "" {
			t.Errorf("server span adopted the caller's tracestate %q", got)
		}
		if got := baggage.FromContext(handlerCtx()).Len(); got != 0 {
			t.Errorf("handler context carries %d caller baggage members, want 0", got)
		}
	})

	t.Run("an unsampled caller cannot suppress recording", func(t *testing.T) {
		rec, _, url := trustIDsServer(t, ids)
		caller := callerContext(false)
		callWithContext(t, url, caller)
		ended := rec.Ended()
		if len(ended) != 1 {
			t.Fatalf("server recorded %d spans, want 1: the server samples every trace it starts, so it records this one", len(ended))
		}
		if ended[0].SpanContext().TraceID() != caller.TraceID() {
			t.Errorf("server span left the caller's trace")
		}
	})

	t.Run("a sampled caller cannot force recording", func(t *testing.T) {
		t.Setenv(EnvTracesSampler, "parentbased_always_off")
		rec, handlerCtx, url := trustIDsServer(t, ids)
		caller := callerContext(true)
		callWithContext(t, url, caller)
		if n := len(rec.Ended()); n != 0 {
			t.Errorf("server recorded %d spans, want 0: its own policy samples nothing", n)
		}
		if sc := trace.SpanContextFromContext(handlerCtx()); sc.TraceID() != caller.TraceID() || sc.IsSampled() {
			t.Errorf("handler span context = %v, want the caller's trace, unsampled", sc)
		}
	})

	t.Run("no traceparent starts a new trace", func(t *testing.T) {
		rec, _, url := trustIDsServer(t, ids)
		client := connect.NewClient[structpb.Struct, structpb.Struct](http.DefaultClient, url+trustProcedure)
		if _, err := client.CallUnary(context.Background(), connect.NewRequest(&structpb.Struct{})); err != nil {
			t.Fatal(err)
		}
		ended := rec.Ended()
		if len(ended) != 1 || ended[0].Parent().IsValid() {
			t.Errorf("want one root server span, got %d spans", len(ended))
		}
	})
}

// TrustAll adopts the whole context, the sampled flag and tracestate included:
// that is what separates an internal service from a signed-in user.
func TestTrustRemoteByTrustAll(t *testing.T) {
	t.Setenv(EnvTracesSampler, "parentbased_always_off")
	rec, _, url := trustIDsServer(t, func(context.Context, connect.Peer, http.Header) TraceTrust { return TrustAll })
	caller := callerContext(true)
	callWithContext(t, url, caller)
	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("server recorded %d spans, want 1: TrustAll follows the caller's sampled flag", len(ended))
	}
	if got := ended[0].SpanContext().TraceState().String(); got != "vendor=caller-chosen" {
		t.Errorf("tracestate = %q, want the caller's", got)
	}
}

func TestRootSampleRatio(t *testing.T) {
	for _, tc := range []struct {
		sampler, arg string
		want         float64
	}{
		{"", "", 1},
		{"always_on", "", 1},
		{"parentbased_always_on", "", 1},
		{"always_off", "", 0},
		{"PARENTBASED_ALWAYS_OFF", "", 0},
		{"traceidratio", "0.25", 0.25},
		{"parentbased_traceidratio", " 0.1 ", 0.1},
		{"parentbased_traceidratio", "", 1},
		{"parentbased_traceidratio", "nope", 1},
		{"parentbased_traceidratio", "1.5", 1},
		{"parentbased_traceidratio", "-0.5", 1},
		{"jaeger_remote", "", 1},
	} {
		t.Setenv(EnvTracesSampler, tc.sampler)
		t.Setenv(EnvTracesSamplerArg, tc.arg)
		if got := rootSampleRatio(); got != tc.want {
			t.Errorf("rootSampleRatio(%q, %q) = %v, want %v", tc.sampler, tc.arg, got, tc.want)
		}
	}
}
