package observe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestNewHTTPClientTracesPropagatesAndForwardsRequestID(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	var gotTraceparent, gotRequestID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		gotRequestID = r.Header.Get(RequestIDHeader)
	}))
	defer srv.Close()

	ctx, parent := tp.Tracer("t").Start(context.Background(), "rpc")
	ctx = context.WithValue(ctx, requestIDContextKey{}, "req-123")
	// A Base is set so this test's client does not share the process
	// transport other tests may have built against a different provider.
	client := NewHTTPClient(HTTPClientOptions{Base: http.DefaultTransport})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/charges?id=secret", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	parent.End()

	var span sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.SpanKind() == trace.SpanKindClient {
			span = s
		}
	}
	if span == nil {
		t.Fatalf("no client span recorded")
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if span.Name() != "POST "+host {
		t.Errorf("span name = %q, want %q", span.Name(), "POST "+host)
	}
	if span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("client span is not a child of the caller's span")
	}
	if !strings.Contains(gotTraceparent, span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q does not carry the trace", gotTraceparent)
	}
	if gotRequestID != "req-123" {
		t.Errorf("X-Request-Id = %q, want req-123", gotRequestID)
	}
}

func TestNewHTTPClientTimeouts(t *testing.T) {
	if got := NewHTTPClient(HTTPClientOptions{}).Timeout; got != defaultClientTimeout {
		t.Errorf("default timeout = %v", got)
	}
	if got := NewHTTPClient(HTTPClientOptions{Timeout: -1}).Timeout; got != 0 {
		t.Errorf("negative timeout = %v, want none", got)
	}
	if got := NewHTTPClient(HTTPClientOptions{Timeout: time.Second}).Timeout; got != time.Second {
		t.Errorf("timeout = %v", got)
	}
	a, b := NewHTTPClient(HTTPClientOptions{}), NewHTTPClient(HTTPClientOptions{})
	if a == b || a.Transport != b.Transport {
		t.Error("clients must be fresh but share one transport")
	}
}
