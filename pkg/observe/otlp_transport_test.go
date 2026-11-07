package observe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

// An OTLP/HTTP receiver closes a connection idle for 60s unless configured
// otherwise (the collector's confighttp default).
const collectorDefaultIdleTimeout = 60 * time.Second

// freshOTLPTransports empties the shared transport cache, so the next exporter
// builds one with the current otlpIdleConnTimeout, and restores it afterwards.
func freshOTLPTransports(t *testing.T) {
	t.Helper()
	baseTransportsMu.Lock()
	prev := baseTransports
	baseTransports = map[string]*http.Transport{}
	baseTransportsMu.Unlock()
	t.Cleanup(func() {
		baseTransportsMu.Lock()
		for _, tr := range baseTransports {
			tr.CloseIdleConnections()
		}
		baseTransports = prev
		baseTransportsMu.Unlock()
	})
}

func TestOTLPIdleConnTimeoutIsBelowTheCollectors(t *testing.T) {
	if otlpIdleConnTimeout <= 0 || otlpIdleConnTimeout >= collectorDefaultIdleTimeout {
		t.Fatalf("otlpIdleConnTimeout = %s, want above 0 and below the collector's %s", otlpIdleConnTimeout, collectorDefaultIdleTimeout)
	}
}

// The SDK's default transport keeps an idle connection for 90s, longer than a
// collector does, so a 60s metric export reused a connection the collector was
// closing and failed with EOF. Every OTLP/HTTP exporter, with or without a
// headers file, must therefore close its idle connections first. The receiver
// here never closes one itself (IdleTimeout 0), so a closed connection is the
// client's doing.
func TestOTLPHTTPExportersCloseIdleConnectionsFirst(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	prev := otlpIdleConnTimeout
	otlpIdleConnTimeout = 50 * time.Millisecond
	t.Cleanup(func() { otlpIdleConnTimeout = prev })
	freshOTLPTransports(t)

	var mu sync.Mutex
	opened, closed := 0, 0
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		switch s {
		case http.StateNew:
			opened++
		case http.StateClosed, http.StateHijacked:
			closed++
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_PROFILES_EXPORTER", "none")

	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	// Shutdown closes idle connections itself, so it runs only after the check.
	defer flush(t, shutdown)

	emitSpan(context.Background())
	type flusher interface{ ForceFlush(context.Context) error }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, p := range []any{otel.GetTracerProvider(), otel.GetMeterProvider()} {
		f, ok := p.(flusher)
		if !ok {
			t.Fatalf("%T cannot be flushed", p)
		}
		if err := f.ForceFlush(ctx); err != nil {
			t.Fatalf("flush %T: %v", p, err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		o, c := opened, closed
		mu.Unlock()
		if o > 0 && c == o {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after exporting, %d of %d connections are closed: the exporter keeps idle connections open longer than a collector does", c, o)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
