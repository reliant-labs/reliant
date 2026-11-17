package serverkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// TestNewLogger_UserErrorsDoNotLogAtError pins the working default: the
// process logger serverkit builds classifies every record, so a hand-written
// logger.Error carrying a user error is not an ERROR line — not only the
// records forge's own interceptors write.
//
// Not parallel: the handler writes to os.Stdout, which the test swaps.
func TestNewLogger_UserErrorsDoNotLogAtError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	logger := newLogger(Config{LogLevel: slog.LevelInfo})
	os.Stdout = stdout

	logger.Error("charge rejected", "error", svcerr.InsufficientBalance("wallet empty"))
	logger.Error("charge failed", "error", svcerr.Internal("stripe 500"))
	_ = w.Close()
	var out bytes.Buffer
	if _, err := io.Copy(&out, r); err != nil {
		t.Fatal(err)
	}

	levels := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		levels[rec["msg"].(string)] = rec["level"].(string) + "/" + rec["error_class"].(string)
	}
	if got := levels["charge rejected"]; got != "INFO/user" {
		t.Errorf("user error = %s, want INFO/user", got)
	}
	if got := levels["charge failed"]; got != "ERROR/server" {
		t.Errorf("server error = %s, want ERROR/server", got)
	}
}

// TestNewLogger_ComposesErrorClassAndTraceID pins that both wrappers are on
// the process logger: a user error logged inside a span is lowered to INFO
// AND still carries the span's trace_id/span_id.
//
// Not parallel: the handler writes to os.Stdout, which the test swaps.
func TestNewLogger_ComposesErrorClassAndTraceID(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	logger := newLogger(Config{LogLevel: slog.LevelInfo})
	os.Stdout = stdout

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	sc := span.SpanContext()

	logger.ErrorContext(ctx, "charge rejected", "error", svcerr.InsufficientBalance("wallet empty"))
	span.End()
	_ = w.Close()
	var out bytes.Buffer
	if _, err := io.Copy(&out, r); err != nil {
		t.Fatal(err)
	}

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
		t.Fatalf("not JSON: %q: %v", out.String(), err)
	}
	if rec["level"] != "INFO" || rec["error_class"] != "user" {
		t.Errorf("level/error_class = %v/%v, want INFO/user", rec["level"], rec["error_class"])
	}
	if rec["trace_id"] != sc.TraceID().String() || rec["span_id"] != sc.SpanID().String() {
		t.Errorf("trace_id/span_id = %v/%v, want %s/%s", rec["trace_id"], rec["span_id"], sc.TraceID(), sc.SpanID())
	}
}
