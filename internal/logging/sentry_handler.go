// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// sentryHandler is an slog.Handler that intercepts Error-level (and above) log
// messages and forwards them to Sentry via the telemetry package. All messages
// are unconditionally passed through to the wrapped handler so normal logging
// behaviour is preserved.
//
// The handler lazily reads telemetry.GetReporter() on every Handle call so it
// works correctly even when the logger is initialised before Sentry.
type sentryHandler struct {
	inner slog.Handler
	// attrs and groups accumulated via WithAttrs / WithGroup so we can pass
	// them along to both the inner handler and Sentry.
	preformatted []slog.Attr
	groups       []string
}

// newSentryHandler wraps an existing slog.Handler with Sentry error reporting.
func newSentryHandler(inner slog.Handler) *sentryHandler {
	return &sentryHandler{inner: inner}
}

func (h *sentryHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *sentryHandler) Handle(ctx context.Context, r slog.Record) error {
	// Always delegate to the inner handler first.
	err := h.inner.Handle(ctx, r)

	// Only intercept Error level and above.
	if r.Level < slog.LevelError {
		return err
	}

	// Build the error and context for Sentry.
	go h.reportToSentry(r)

	return err
}

func (h *sentryHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &sentryHandler{
		inner:        h.inner.WithAttrs(attrs),
		preformatted: append(h.preformatted, attrs...),
		groups:       h.groups,
	}
}

func (h *sentryHandler) WithGroup(name string) slog.Handler {
	return &sentryHandler{
		inner:        h.inner.WithGroup(name),
		preformatted: h.preformatted,
		groups:       append(h.groups, name),
	}
}

// reportToSentry extracts error information from the log record and sends it
// to Sentry. Runs in a separate goroutine to avoid blocking log callers.
func (h *sentryHandler) reportToSentry(r slog.Record) {
	// Build tags and extra context from record attributes.
	tags := make(map[string]string)
	extra := make(map[string]interface{})
	var capturedErr error

	// Collect preformatted attrs (from WithAttrs calls).
	for _, a := range h.preformatted {
		h.collectAttr(a, tags, extra, &capturedErr)
	}

	// Collect record-level attrs.
	r.Attrs(func(a slog.Attr) bool {
		h.collectAttr(a, tags, extra, &capturedErr)
		return true
	})

	// If no explicit error attribute was found, create one from the message.
	if capturedErr == nil {
		capturedErr = errors.New(r.Message)
	}

	// Add the log message as extra context when we have a real error.
	extra["log_message"] = r.Message

	// Add source location if available.
	if r.PC != 0 {
		// slog.Record has source info but we keep it simple — the stack
		// trace in Sentry will be more useful.
		tags["log_source"] = "slog"
	}

	// Add group prefix if any.
	if len(h.groups) > 0 {
		tags["log_group"] = fmt.Sprintf("%v", h.groups)
	}

	telemetry.CaptureExceptionWithContext(capturedErr, tags, extra)
}

// collectAttr processes a single slog.Attr, extracting error values and
// populating tags/extra maps.
func (h *sentryHandler) collectAttr(a slog.Attr, tags map[string]string, extra map[string]interface{}, capturedErr *error) {
	key := a.Key
	val := a.Value

	// Check for error-typed attributes.
	if key == "error" || key == "err" {
		if e, ok := val.Any().(error); ok && e != nil {
			*capturedErr = e
			extra[key] = e.Error()
			return
		}
		// Even if it's a string representation of an error, capture it.
		if val.Kind() == slog.KindString {
			*capturedErr = errors.New(val.String())
			extra[key] = val.String()
			return
		}
	}

	// For group attrs, flatten into extra with dotted keys.
	if val.Kind() == slog.KindGroup {
		attrs := val.Group()
		for _, ga := range attrs {
			groupKey := key + "." + ga.Key
			extra[groupKey] = ga.Value.String()
		}
		return
	}

	// Short string values make good tags; longer values go in extra.
	s := val.String()
	if len(s) <= 64 {
		tags[key] = s
	} else {
		extra[key] = s
	}
}
