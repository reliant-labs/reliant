package observe

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
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
