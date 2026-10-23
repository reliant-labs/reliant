package observe

import (
	"context"
	"log/slog"
	"unicode/utf8"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Log attribute keys carrying the active span, matching what the audit
// middleware already writes.
const (
	LogKeyTraceID = "trace_id"
	LogKeySpanID  = "span_id"
)

// NewTraceHandler wraps next so every record logged with a context holding a
// valid span carries trace_id and span_id. Records without a span, and records
// that already carry a trace_id attribute, pass through unchanged. The ids are
// only available to the *Context logging methods (InfoContext, LogAttrs, ...):
// slog.Info and friends have no context to read.
//
// Under logger.WithGroup the ids are nested in the group, as with any
// attribute added after the group opens.
//
// A record at WARN or above, logged with a recording span in its context, is
// also added to that span as an event — "log.warn" or "log.error", carrying
// the message, the level and the error (see LogEventWarn). The gateway's tail
// sampler keeps a trace that has one, so a request whose logs complained is
// never sampled away while its log lines survive. Wrap the handler INSIDE
// NewErrorClassHandler (as serverkit does) so the event follows the level the
// line is written at: a user error lowered to INFO adds none.
func NewTraceHandler(next slog.Handler) slog.Handler { return &traceHandler{next: next} }

// NewLogHandler is the handler serverkit builds for the process logger: next
// (the stdout JSON or text handler) wrapped by NewTraceHandler. Every record
// at ERROR or above also gains the attributes an issue is grouped by —
// code.function.name, code.file.path, code.line.number, and for a record
// carrying an error exception.type and exception.message (see error_inputs.go)
// — on both shipping paths. When
// the OTLP logs switch is on (Config.OTLPLogs or OTEL_LOGS_EXPORTER=otlp) it is also teed into the OpenTelemetry logs bridge,
// which Setup connects to an OTLP exporter.
//
// OTLP logs export is OFF by default and is the SECOND shipping path, not an
// addition to the first: the stdout stream is still written, so a workload that
// enables the OTLP exporter must not also be collected from its stdout/log files
// (the local collector's file reader, a cluster node agent), or every line
// arrives twice. Exactly one shipping path per environment.
func NewLogHandler(next slog.Handler, cfg Config) slog.Handler {
	h := NewTraceHandler(next)
	if !exportEnabled(signalLogs, cfg) {
		return errorInputsHandler{next: h}
	}
	return errorInputsHandler{next: slog.NewMultiHandler(h, otelslog.NewHandler(logScopeName(cfg)))}
}

func logScopeName(cfg Config) string {
	if cfg.ServiceName != "" {
		return cfg.ServiceName
	}
	return "unknown"
}

type traceHandler struct{ next slog.Handler }

func (h *traceHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() && !hasAttr(r, LogKeyTraceID) {
		r.AddAttrs(
			slog.String(LogKeyTraceID, sc.TraceID().String()),
			slog.String(LogKeySpanID, sc.SpanID().String()),
		)
	}
	if r.Level >= slog.LevelWarn {
		addLogEvent(trace.SpanFromContext(ctx), r)
	}
	return h.next.Handle(ctx, r)
}

// Span event names NewTraceHandler adds for a WARN or ERROR record. The
// gateway's tail-sampling policy matches them, so they are part of the
// contract between the SDK and the collector config forge renders.
const (
	LogEventWarn  = "log.warn"
	LogEventError = "log.error"
)

// maxLogEventMessage caps the message copied onto the span: the log line has
// the whole of it.
const maxLogEventMessage = 512

func addLogEvent(span trace.Span, r slog.Record) {
	if !span.IsRecording() {
		return
	}
	name := LogEventWarn
	if r.Level >= slog.LevelError {
		name = LogEventError
	}
	attrs := []attribute.KeyValue{
		attribute.String("log.severity", r.Level.String()),
		attribute.String("log.message", truncate(r.Message, maxLogEventMessage)),
	}
	r.Attrs(func(a slog.Attr) bool {
		if err, ok := a.Value.Any().(error); ok && err != nil {
			attrs = append(attrs, attribute.String("exception.message", truncate(err.Error(), maxLogEventMessage)))
			return false
		}
		return true
	})
	span.AddEvent(name, trace.WithAttributes(attrs...))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary so the attribute stays valid UTF-8.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{next: h.next.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{next: h.next.WithGroup(name)}
}

func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}
