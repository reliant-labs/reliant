package observe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// markerInterceptor is a test collaborator that records, on a shared
// trace slice, that it ran — so a test can prove an explicit Auth/Audit/
// RateLimit collaborator was actually threaded into the chain (no global
// needed) and that ordering is correct.
type markerInterceptor struct {
	name  string
	trace *[]string
}

func (m markerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		*m.trace = append(*m.trace, m.name)
		return next(ctx, req)
	}
}

func (m markerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (m markerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// fakeRequest is the minimal connect.AnyRequest needed to drive a unary
// chain in-process without a real transport.
type fakeRequest struct{ connect.AnyRequest }

func (fakeRequest) Spec() connect.Spec  { return connect.Spec{Procedure: "/test.v1.Svc/M"} }
func (fakeRequest) Header() http.Header { return http.Header{} }

// run threads a request through the chain, innermost handler last, and
// returns the recorded interceptor execution order.
func runChain(t *testing.T, chain []connect.Interceptor, trace *[]string) {
	t.Helper()
	// Compose the chain the way connect does: the first interceptor is
	// outermost. Build a terminal handler, then wrap from the inside out.
	var handler connect.UnaryFunc = func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		*trace = append(*trace, "handler")
		return nil, nil
	}
	for i := len(chain) - 1; i >= 0; i-- {
		handler = chain[i].WrapUnary(handler)
	}
	_, _ = handler(context.Background(), fakeRequest{})
}

func TestChain_ThreadsExplicitCollaborators(t *testing.T) {
	var trace []string
	auth := markerInterceptor{name: "auth", trace: &trace}
	audit := markerInterceptor{name: "audit", trace: &trace}
	rl := markerInterceptor{name: "ratelimit", trace: &trace}
	extra := markerInterceptor{name: "extra", trace: &trace}

	chain := observe.Chain(observe.Deps{
		// Logger/Tracer/Meter nil → observability layers degrade to
		// pass-through / slog.Default; they don't append to the trace.
		Auth:      auth,
		Audit:     audit,
		RateLimit: rl,
		Extras:    []connect.Interceptor{extra},
	})

	runChain(t, chain, &trace)

	// The application collaborators must all have run (no global needed —
	// they were passed in directly), and in the documented order:
	// auth → audit → ratelimit → extra → handler.
	want := []string{"auth", "audit", "ratelimit", "extra", "handler"}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace = %v, want %v", trace, want)
		}
	}
}

func TestChain_NilCollaboratorsSkipped(t *testing.T) {
	// All application collaborators nil → only the observability layer
	// (no-op with nil deps) + handler. Nothing appended by markers.
	var trace []string
	chain := observe.Chain(observe.Deps{})
	runChain(t, chain, &trace)
	// Only the terminal handler records.
	if len(trace) != 1 || trace[0] != "handler" {
		t.Fatalf("nil collaborators: trace = %v, want [handler]", trace)
	}
	// Chain still has the fixed observability prefix (recovery, request-id,
	// logging, tracing, metrics) = 5 interceptors, none of which is a
	// marker, plus the Deadline slot's default (nil is forge's budget, not
	// none) and the always-last svcerr interceptor.
	if len(chain) != 7 {
		t.Errorf("nil-collaborator chain length = %d, want 7 (observability + the default deadline + svcerr)", len(chain))
	}
	if _, ok := chain[5].(observe.Deadline); !ok {
		t.Errorf("chain[5] = %T, want the default observe.Deadline", chain[5])
	}
	if !svcerr.IsInterceptor(chain[6]) {
		t.Errorf("chain[6] = %T, want svcerr.Interceptor innermost", chain[6])
	}
}

func TestChain_AuditOnlyThreaded(t *testing.T) {
	// Prove a SINGLE explicit collaborator threads in without the others —
	// the core "no global" assertion for audit specifically.
	var trace []string
	audit := markerInterceptor{name: "audit", trace: &trace}
	chain := observe.Chain(observe.Deps{Audit: audit})
	runChain(t, chain, &trace)
	if len(trace) != 2 || trace[0] != "audit" || trace[1] != "handler" {
		t.Fatalf("audit-only: trace = %v, want [audit handler]", trace)
	}
}

// rejectAuth is an auth interceptor that refuses every call, the way a missing
// or bad token does.
type rejectAuth struct{ markerInterceptor }

func (rejectAuth) WrapUnary(connect.UnaryFunc) connect.UnaryFunc {
	return func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing Authorization header"))
	}
}

// A request that auth rejects is exactly the one you want to find in a trace.
// With the span opened INSIDE auth (otelconnect in Extras, the old scaffold) the
// rejection had no span, and the "rpc failed" line, written outside the span,
// had no trace_id to find it by. Deps.Trace is the outermost slot.
func TestChain_TraceCoversAuthRejectionAndItsLogLine(t *testing.T) {
	const procedure = "/observe.test.v1.TraceService/Do"
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	otelInt, err := otelconnect.NewInterceptor(otelconnect.WithTracerProvider(tp), otelconnect.WithPropagator(propagation.TraceContext{}), otelconnect.WithTrustRemote())
	if err != nil {
		t.Fatalf("otelconnect: %v", err)
	}

	var logs bytes.Buffer
	logger := slog.New(observe.NewTraceHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	handlerRan := false
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
			handlerRan = true
			return connect.NewResponse(&structpb.Struct{}), nil
		},
		connect.WithInterceptors(observe.Chain(observe.Deps{
			Logger: logger,
			Trace:  otelInt,
			Auth:   rejectAuth{},
		})...),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	const browserTrace = "0af7651916cd43dd8448eb211c80319c"
	const browserSpan = "b7ad6b7169203331"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+procedure, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", "00-"+browserTrace+"-"+browserSpan+"-01")
	resp, err := srv.Client().Do(req) //nolint:bodyclose // closed below
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if handlerRan {
		t.Fatal("auth rejected the call, yet the handler ran")
	}

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("a rejected call must still have its span: got %d spans, want 1", len(ended))
	}
	sp := ended[0]
	if got := sp.SpanContext().TraceID().String(); got != browserTrace {
		t.Errorf("span trace id = %s, want the caller's %s (WithTrustRemote parents it)", got, browserTrace)
	}
	if got := sp.Parent().SpanID().String(); got != browserSpan {
		t.Errorf("span parent = %s, want the caller's span %s", got, browserSpan)
	}

	var rpcFailed map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if rec["msg"] == "rpc failed" {
			rpcFailed = rec
		}
	}
	if rpcFailed == nil {
		t.Fatalf("no \"rpc failed\" line in:\n%s", logs.String())
	}
	if rpcFailed["trace_id"] != browserTrace || rpcFailed["span_id"] != sp.SpanContext().SpanID().String() {
		t.Errorf("the \"rpc failed\" line must carry the span it ran in (trace_id=%s span_id=%s), got %v",
			browserTrace, sp.SpanContext().SpanID(), rpcFailed)
	}
}

func TestChain_TraceSitsOutsideEverythingElse(t *testing.T) {
	var trace []string
	chain := observe.Chain(observe.Deps{
		Trace: markerInterceptor{name: "trace", trace: &trace},
		Auth:  markerInterceptor{name: "auth", trace: &trace},
	})
	runChain(t, chain, &trace)
	want := []string{"trace", "auth", "handler"}
	if len(trace) != len(want) || trace[0] != want[0] || trace[1] != want[1] || trace[2] != want[2] {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	if _, ok := chain[0].(markerInterceptor); !ok {
		t.Errorf("chain[0] = %T, want the Trace interceptor outermost", chain[0])
	}
}
