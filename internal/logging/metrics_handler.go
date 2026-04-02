// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"log/slog"
	"runtime"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// metricsHandler is an slog.Handler that increments Prometheus counters for
// Error and Warn level log messages. These represent "dead-end" errors that
// are logged but not propagated to callers.
type metricsHandler struct {
	inner slog.Handler
}

// deadEndCounter holds the Prometheus counter set via SetDeadEndErrorCounter.
// If nil the handler is a no-op pass-through.
var deadEndCounter *prometheus.CounterVec

// SetDeadEndErrorCounter sets the Prometheus counter used by the metrics handler.
// Must be called before logging setup if you want metrics. If not called, the
// handler is a no-op pass-through.
func SetDeadEndErrorCounter(counter *prometheus.CounterVec) {
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

	// Only count Warn and Error level.
	if r.Level < slog.LevelWarn {
		return err
	}

	counter := deadEndCounter
	if counter == nil {
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

	level := "error"
	if r.Level == slog.LevelWarn {
		level = "warn"
	}

	// Truncate message to avoid high-cardinality.
	msg := r.Message
	if len(msg) > 80 {
		msg = msg[:80]
	}

	counter.WithLabelValues(level, pkg, msg).Inc()
	return err
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
