// Package observetest holds test helpers that pin a process's observability
// wiring from a unit test.
//
// The one it starts with answers "are this process's logs joined to its
// traces?" A forge server gets that from serverkit's logger. An application
// that builds its own logger has to install observe.NewTraceHandler itself, and
// nothing fails when it does not: the lines are written, they just carry no
// trace_id, and the gap shows up weeks later as a log search that cannot reach
// a trace. AssertLogCorrelation turns that into a failing test.
package observetest

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/forge/pkg/observe"
)

// AssertLogCorrelation logs through logger with a span in the context and
// fails t unless:
//
//   - the INFO line written carries that span's trace_id and span_id, and a
//     line logged without a span carries no trace_id;
//   - the WARN line is also recorded on the span as an observe.LogEventWarn
//     event, which is what lets the tail sampler keep the trace.
//
// output returns everything logger has written so far; lines may be JSON or
// slog's text format. Use the process's real logger constructor — the point is
// to test the chain the process installs, not a fresh one:
//
//	var buf bytes.Buffer
//	logger := logging.New(&buf, logging.Settings{Format: "json"})
//	observetest.AssertLogCorrelation(t, logger, buf.String)
func AssertLogCorrelation(t testing.TB, logger *slog.Logger, output func() string) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	ctx, span := tp.Tracer("observetest").Start(context.Background(), "observetest.correlation")
	sc := span.SpanContext()
	const (
		infoMsg  = "observetest correlated info"
		warnMsg  = "observetest correlated warn"
		plainMsg = "observetest uncorrelated"
	)
	logger.InfoContext(ctx, infoMsg)
	logger.WarnContext(ctx, warnMsg)
	logger.InfoContext(context.Background(), plainMsg)
	span.End()

	lines := strings.Split(output(), "\n")
	line := func(msg string) map[string]string {
		for _, l := range lines {
			if fields := parseLine(l); fields["msg"] == msg {
				return fields
			}
		}
		t.Fatalf("observetest: no log line with msg %q in output:\n%s", msg, output())
		return nil
	}
	for _, msg := range []string{infoMsg, warnMsg} {
		fields := line(msg)
		if got := fields[observe.LogKeyTraceID]; got != sc.TraceID().String() {
			t.Errorf("log line %q: %s = %q, want %s — is observe.NewTraceHandler in the logger chain, and does the call site pass ctx?",
				msg, observe.LogKeyTraceID, got, sc.TraceID())
		}
		if got := fields[observe.LogKeySpanID]; got != sc.SpanID().String() {
			t.Errorf("log line %q: %s = %q, want %s", msg, observe.LogKeySpanID, got, sc.SpanID())
		}
	}
	if got, ok := line(plainMsg)[observe.LogKeyTraceID]; ok {
		t.Errorf("log line with no span carries %s=%q", observe.LogKeyTraceID, got)
	}

	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("observetest: recorded %d spans, want 1", len(ended))
	}
	found := false
	for _, ev := range ended[0].Events() {
		if ev.Name == observe.LogEventWarn {
			found = true
		}
	}
	if !found {
		t.Errorf("the WARN line was not recorded on its span as a %q event — the tail sampler cannot keep traces whose logs warned", observe.LogEventWarn)
	}
}

// parseLine reads one log line as a flat map: a JSON object, or slog's text
// format (key=value, values optionally quoted).
func parseLine(line string) map[string]string {
	line = strings.TrimSpace(line)
	out := map[string]string{}
	if line == "" {
		return out
	}
	if strings.HasPrefix(line, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			for k, v := range m {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
		}
		return out
	}
	for len(line) > 0 {
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(line[:eq])
		rest := line[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := 1
			for end < len(rest) && (rest[end] != '"' || rest[end-1] == '\\') {
				end++
			}
			val = strings.ReplaceAll(rest[1:min(end, len(rest))], `\"`, `"`)
			rest = rest[min(end+1, len(rest)):]
		} else {
			sp := strings.IndexByte(rest, ' ')
			if sp < 0 {
				sp = len(rest)
			}
			val, rest = rest[:sp], rest[sp:]
		}
		out[key] = val
		line = strings.TrimSpace(rest)
	}
	return out
}
