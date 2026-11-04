package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// stack_test.go — a server fault's svcerr stack reaches the error record and
// the span as exception.stacktrace, and a client-class error's never does.

//go:noinline
func failsHere() error { return svcerr.Internal("create order failed") }

//go:noinline
func failsThere() error { return svcerr.Internal("create order failed") }

// logsFailure is the one place the "rpc failed"-style record is written from,
// as an interceptor is: every record's own call site is this function.
//
//go:noinline
func logsFailure(l *slog.Logger, err error) { l.Error("rpc failed", "error", err) }

func TestErrorRecordCarriesTheServerFaultStack(t *testing.T) {
	clearOTelEnv(t)
	var buf bytes.Buffer
	logger := traceJSONLogger(&buf, Config{})
	logsFailure(logger, svcerr.Wrap(failsHere()))

	line := decodeLines(t, &buf)[0]
	st, _ := line[LogKeyExceptionStacktrace].(string)
	if !strings.HasPrefix(st, "goroutine ") || !strings.Contains(st, "observe.failsHere") {
		t.Fatalf("exception.stacktrace = %q, want Go's format naming failsHere", st)
	}
	if len(st) > svcerr.MaxStackBytes {
		t.Errorf("stacktrace is %d bytes, bound is %d", len(st), svcerr.MaxStackBytes)
	}
	// The call site is where the failure began, not the logging function.
	const origin = "github.com/reliant-labs/forge/pkg/observe.failsHere"
	if got := line[keyFunc]; got != origin {
		t.Errorf("%s = %v, want %s", keyFunc, got, origin)
	}
	if got := line[keyFile]; got != "pkg/observe/stack_test.go" {
		t.Errorf("%s = %v, want a module-relative path", keyFile, got)
	}
	if got, _ := line[keyLine].(float64); got == 0 {
		t.Errorf("%s = %v", keyLine, got)
	}
}

func TestErrorRecordHasNoStackForClientErrors(t *testing.T) {
	clearOTelEnv(t)
	for name, err := range map[string]error{
		"invalid argument":  svcerr.InvalidArgument("bad"),
		"not found":         svcerr.Wrap(svcerr.NotFound("job")),
		"permission denied": svcerr.PermissionDenied("no"),
		"user-class":        svcerr.WithClass(svcerr.Unavailable("offline"), svcerr.ClassUser),
		"canceled":          fmt.Errorf("query: %w", context.Canceled),
		"plain error":       errors.New("boom"),
	} {
		var buf bytes.Buffer
		logsFailure(traceJSONLogger(&buf, Config{}), err)
		line := decodeLines(t, &buf)[0]
		if v, ok := line[LogKeyExceptionStacktrace]; ok {
			t.Errorf("%s: carries a stack: %v", name, v)
		}
		if got := line[keyFunc]; got != "github.com/reliant-labs/forge/pkg/observe.logsFailure" {
			t.Errorf("%s: %s = %v, want the log call site", name, keyFunc, got)
		}
	}
}

func TestErrorRecordKeepsACallerSuppliedStack(t *testing.T) {
	clearOTelEnv(t)
	var buf bytes.Buffer
	logger := traceJSONLogger(&buf, Config{})
	logger.Error("x", "error", failsHere(), LogKeyExceptionStacktrace, "custom", keyFunc, "pkg.Mine")

	line := decodeLines(t, &buf)[0]
	if line[LogKeyExceptionStacktrace] != "custom" || line[keyFunc] != "pkg.Mine" {
		t.Errorf("caller's attributes were overwritten: %v", line)
	}
}

func TestErrorRecordAtWarnHasNoStack(t *testing.T) {
	clearOTelEnv(t)
	var buf bytes.Buffer
	traceJSONLogger(&buf, Config{}).Warn("slow", "error", failsHere())
	if _, ok := decodeLines(t, &buf)[0][LogKeyExceptionStacktrace]; ok {
		t.Error("only ERROR records carry error inputs")
	}
}

func spanWith(t *testing.T) (*tracetest.SpanRecorder, func(error) tracetest.SpanStub) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return rec, func(err error) tracetest.SpanStub {
		_, span := tp.Tracer("t").Start(context.Background(), "op")
		RecordSpanError(span, err)
		span.End()
		ended := rec.Ended()
		return tracetest.SpanStubFromReadOnlySpan(ended[len(ended)-1])
	}
}

func TestSpanExceptionEventCarriesTheStack(t *testing.T) {
	_, record := spanWith(t)

	stub := record(svcerr.Wrap(failsThere()))
	st := eventAttr(t, stub, LogKeyExceptionStacktrace)
	if !strings.Contains(st, "observe.failsThere") || !strings.HasPrefix(st, "goroutine ") {
		t.Errorf("span exception.stacktrace = %q", st)
	}

	for name, err := range map[string]error{
		"not found":   svcerr.NotFound("job"),
		"invalid":     svcerr.InvalidArgument("x"),
		"plain error": errors.New("boom"),
	} {
		stub := record(err)
		if got := eventAttr(t, stub, LogKeyExceptionStacktrace); got != "" {
			t.Errorf("%s: span carries a stack: %q", name, got)
		}
		if len(stub.Events) != 1 || stub.Events[0].Name != "exception" {
			t.Errorf("%s: events = %v, want the exception event", name, stub.Events)
		}
	}
}

func eventAttr(t *testing.T, stub tracetest.SpanStub, key string) string {
	t.Helper()
	for _, ev := range stub.Events {
		for _, a := range ev.Attributes {
			if string(a.Key) == key {
				return a.Value.AsString()
			}
		}
	}
	return ""
}
