package serverkit_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

func TestNewLoggerCarriesTraceIDsFromContext(t *testing.T) {
	t.Setenv("OTEL_LOGS_EXPORTER", "")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	logger := serverkit.NewLogger(serverkit.Config{})
	os.Stdout = orig

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	logger.InfoContext(ctx, "hello")
	span.End()
	_ = w.Close()

	var line map[string]any
	if err := json.NewDecoder(r).Decode(&line); err != nil {
		t.Fatalf("logger did not emit JSON: %v", err)
	}
	if line["trace_id"] != span.SpanContext().TraceID().String() || line["span_id"] != span.SpanContext().SpanID().String() {
		t.Errorf("trace/span ids missing from log line: %v", line)
	}
}
