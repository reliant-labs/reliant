// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// metricsHandler is an slog.Handler that increments Prometheus counters for
// Error and Warn level log messages. These represent "dead-end" errors that
// are logged but not propagated to callers.
type metricsHandler struct {
	inner slog.Handler
}

// labelledCounter is the only thing this handler needs from a metric: the
// ability to increment one series identified by label values. Declared here,
// at the consumer, so logging does not depend on the observability package or
// on which concrete counter type it hands over.
type labelledCounter interface {
	WithLabelValues(values ...string) prometheus.Counter
}

// deadEndCounter holds the counter set via SetDeadEndErrorCounter.
// If nil the handler is a no-op pass-through.
var deadEndCounter labelledCounter

// SetDeadEndErrorCounter sets the counter used by the metrics handler.
// Must be called before logging setup if you want metrics. If not called, the
// handler is a no-op pass-through.
func SetDeadEndErrorCounter(counter labelledCounter) {
	deadEndCounter = counter
}

func newMetricsHandler(inner slog.Handler) *metricsHandler {
	return &metricsHandler{inner: inner}
}

func (h *metricsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *metricsHandler) Handle(ctx context.Context, r slog.Record) error {
	// Always delegate to the inner handler.
	err := h.inner.Handle(ctx, r)

	counter := deadEndCounter
	if counter == nil {
		return err
	}

	// Count Warn and Error records, and user errors. A user error no longer
	// logs at Warn or Error (the error-class policy lowers it to INFO), so
	// without its own label its volume would vanish from this counter along
	// with the page it should never have caused.
	var level string
	switch {
	case r.Level >= slog.LevelError:
		level = "error"
	case r.Level >= slog.LevelWarn:
		level = "warn"
	case isUserErrorRecord(r):
		level = "user_error"
	default:
		return err
	}

	// Extract package name from caller.
	pkg := "unknown"
	if r.PC != 0 {
		frames := runtime.CallersFrames([]uintptr{r.PC})
		if frame, _ := frames.Next(); frame.Function != "" {
			pkg = extractPackage(frame.Function)
		}
	}

	counter.WithLabelValues(level, pkg, messageLabel(r.Message)).Inc()
	return err
}

// isUserErrorRecord reports whether the error-class policy marked r as a user
// error (error_class=user). Canceled records are not counted: nobody has to
// act on a caller that went away.
func isUserErrorRecord(r slog.Record) bool {
	if r.NumAttrs() == 0 {
		return false
	}
	user := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == observe.ErrorClassKey {
			user = a.Value.String() == svcerr.ClassUser.String()
			return false
		}
		return true
	})
	return user
}

// maxMessageLabelRunes caps the message label to keep counter cardinality and
// series size bounded.
const maxMessageLabelRunes = 80

// messageLabel clamps msg to a Prometheus-safe label value.
//
// Prometheus PANICS on a label value that is not valid UTF-8, and this runs
// inside slog.Handler.Handle — so any malformed value takes down the process
// from inside a log call, at the exact moment something was already going
// wrong. Two ways a message gets there:
//
//   - Slicing bytes cuts a multi-byte rune in half. call_llm.go's "…not by a
//     real interrupt — failing the turn…" warning lands an em dash across byte
//     80, and a []byte clamp emitted "\xe2\x80", killing the worker.
//   - A message can be invalid before it arrives: raw command output, a
//     filename, or a provider payload interpolated into the message.
//
// So clamp by runes, and coerce whatever survives — strings.ToValidUTF8
// replaces malformed bytes rather than trusting the input.
func messageLabel(msg string) string {
	if utf8.RuneCountInString(msg) > maxMessageLabelRunes {
		msg = string([]rune(msg)[:maxMessageLabelRunes])
	}
	return strings.ToValidUTF8(msg, "")
}

func (h *metricsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &metricsHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *metricsHandler) WithGroup(name string) slog.Handler {
	return &metricsHandler{inner: h.inner.WithGroup(name)}
}

// extractPackage extracts a short package identifier from a fully-qualified function name.
// e.g. "github.com/reliant-labs/reliant/internal/streaming.(*NATSHub).Publish" -> "streaming"
func extractPackage(funcName string) string {
	// Remove method/function part.
	if idx := strings.LastIndex(funcName, "."); idx >= 0 {
		funcName = funcName[:idx]
	}
	// Remove receiver type if present (e.g. "pkg.(*Type)" -> "pkg").
	if idx := strings.LastIndex(funcName, "."); idx >= 0 {
		funcName = funcName[:idx]
	}
	// Get last path segment.
	if idx := strings.LastIndex(funcName, "/"); idx >= 0 {
		return funcName[idx+1:]
	}
	return funcName
}
