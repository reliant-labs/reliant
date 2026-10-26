package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	daemonv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
)

// The default client traces both kinds of call exactly once: a Connect rpc by
// its otelconnect interceptor, the plain run-resume POST by its transport.
// Sharing one otelhttp client between them would nest a second CLIENT span
// under every rpc; sharing one bare client would leave the POST untraced.
// Either way the callee must receive the caller's traceparent.
func TestDefaultClientTracesEachCallOnce(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	var traceparents []string
	mux := http.NewServeMux()
	mux.Handle(daemonv1connect.NewDaemonServiceHandler(&resumeRecorder{}))
	mux.HandleFunc(runResumePath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resumed":true}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparents = append(traceparents, r.Header.Get("Traceparent"))
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})
	clientSpans := func() []sdktrace.ReadOnlySpan {
		var out []sdktrace.ReadOnlySpan
		for _, s := range rec.Ended() {
			if s.SpanKind() == trace.SpanKindClient {
				out = append(out, s)
			}
		}
		return out
	}

	if err := c.ResumeDaemon(context.Background(), "jwt", "daemon-1"); err != nil {
		t.Fatalf("ResumeDaemon: %v", err)
	}
	if got := clientSpans(); len(got) != 1 || !strings.Contains(got[0].Name(), "DaemonService/ResumeDaemon") {
		t.Fatalf("a Connect rpc must be one client span named for the rpc; got %d: %v", len(got), spanNames(got))
	}

	if _, err := c.ResumeDaemonForRun(context.Background(), "user-1", "daemon-1"); err != nil {
		t.Fatalf("ResumeDaemonForRun: %v", err)
	}
	if got := clientSpans()[1:]; len(got) != 1 || !strings.HasPrefix(got[0].Name(), "POST ") {
		t.Fatalf("the run-resume POST must be one \"POST <host>\" client span; got %d: %v", len(got), spanNames(got))
	}

	for i, tp := range traceparents {
		if tp == "" {
			t.Errorf("request %d reached the control plane without a traceparent", i)
		}
	}
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name()
	}
	return names
}
