// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"errors"
	"log/slog"
	"strings"

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

// sentrySilentPatterns are errors that should be completely silent — not
// reported to Sentry and not re-logged. These are user-initiated cancellations
// that are a normal part of operation.
var sentrySilentPatterns = []string{
	"context canceled",
	"streaming cancelled by user",
}

// sentryWarnPatterns are errors that should NOT go to Sentry but should still
// be logged as warnings for operational visibility.
var sentryWarnPatterns = []string{
	"exit status 128",
	"signal: killed",
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

	// Completely silent — user-initiated cancellations.
	if matchesAny(capturedErr, sentrySilentPatterns) {
		return
	}

	// Warn-only — log for visibility but don't send to Sentry.
	if matchesAny(capturedErr, sentryWarnPatterns) {
		Warn("[Sentry] Suppressed non-actionable error",
			"error", capturedErr.Error(),
			"log_message", r.Message,
		)
		return
	}

	// Add the log message as extra context when we have a real error.
	extra["log_message"] = r.Message

	// Add source location if available.
	if r.PC != 0 {
		// slog.Record has source info but we keep it simple — the stack
		// trace in Sentry will be more useful.
		tags["log_source"] = "slog"
	}

	// Add group prefix if any. Group names come from code, not users, but
	// they still pass the same tag check as everything else.
	if len(h.groups) > 0 {
		if group, ok := telemetry.TagValue("log_group", strings.Join(h.groups, ".")); ok {
			tags["log_group"] = group
		}
	}

	telemetry.CaptureExceptionWithContext(capturedErr, tags, extra)
}

// matchesAny returns true if the error message contains any of the given patterns.
func matchesAny(err error, patterns []string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// collectAttr processes a single slog.Attr: an "error"/"err" attribute becomes
// the captured error, and every other attribute goes through collectField.
func (h *sentryHandler) collectAttr(a slog.Attr, tags map[string]string, extra map[string]interface{}, capturedErr *error) {
	key := a.Key
	val := a.Value.Resolve()

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

	collectField(key, val, tags, extra)
}

// collectField decides what, if anything, one attribute contributes to the
// Sentry event. It is an allowlist, not a length check: a short "command",
// "file_path" or "prompt" is exactly the user content Sentry must never see,
// and being under some length does not make a value an identifier.
//
//   - A key on telemetry's tag allowlist (identifiers such as chat_id or
//     tool_call_id, enums such as provider, phase or status) whose value has
//     the shape of one becomes a tag. telemetry.TagValue is the same check the
//     BeforeSend scrubber applies, so the two lists cannot drift.
//   - Any other number or flag goes to extra: it cannot carry content.
//   - Everything else stays in the log line and never leaves the process.
//
// Group members are flattened under dotted keys (req.chat_id) and judged the
// same way.
func collectField(key string, val slog.Value, tags map[string]string, extra map[string]interface{}) {
	if val.Kind() == slog.KindGroup {
		for _, member := range val.Group() {
			collectField(joinKey(key, member.Key), member.Value.Resolve(), tags, extra)
		}
		return
	}

	if tag, ok := telemetry.TagValue(key, val.String()); ok {
		tags[key] = tag
		return
	}

	switch val.Kind() {
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool:
		extra[key] = val.Any()
	}
}

// joinKey qualifies a group member's key. An empty group key inlines its
// members, as slog's own handlers do.
func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
