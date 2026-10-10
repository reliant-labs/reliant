// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"runtime"
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

	// The stack is read here, on the logging goroutine: the report runs on
	// its own, whose stack says nothing about who logged.
	frames := callerFrames()
	go h.reportToSentry(r, frames)

	return err
}

// maxLogStackDepth bounds the stack captured for a forwarded record.
const maxLogStackDepth = 64

// slogFramePrefix marks slog's own frames. Every handler in the chain — this
// one, the metrics bridge, forge's error-class policy, whatever wraps them
// next — runs inside slog's Logger.log, so the frames below the outermost
// slog frame are handler machinery whichever handlers are installed.
const slogFramePrefix = "log/slog."

// loggerWrapperPrefixes are the loggers that adapt into slog from the call
// site's side: this package's Error/Warn/... and the Temporal loggers. r.PC
// cannot locate the call site — every logging.Error call records
// logging.Error as its caller — so the call site is the first frame past slog
// that is not one of these.
var loggerWrapperPrefixes = []string{
	reflect.TypeOf(sentryHandler{}).PkgPath() + ".",
	"go.temporal.io/sdk/log.",
	"go.temporal.io/sdk/internal/log.",
}

// callerFrames is the stack of the code that logged, innermost first, so
// frames[0] is the call site.
func callerFrames() []runtime.Frame {
	var pcs [maxLogStackDepth]uintptr
	n := runtime.Callers(1, pcs[:])
	iter := runtime.CallersFrames(pcs[:n])
	var stack []runtime.Frame
	for {
		frame, more := iter.Next()
		stack = append(stack, frame)
		if !more {
			break
		}
	}
	// Past the first run of slog frames — or, for a handler driven directly
	// with no slog in the stack, from the top — then past the wrappers that
	// adapt into slog.
	start, inSlog := 0, false
	for i, frame := range stack {
		if strings.HasPrefix(frame.Function, slogFramePrefix) {
			inSlog = true
			continue
		}
		if inSlog {
			start = i
			break
		}
	}
	for start < len(stack) && isLoggerWrapperFrame(stack[start]) {
		start++
	}
	return stack[start:]
}

// isLoggerWrapperFrame reports whether frame is a logger adapting into slog
// (or, for a handler driven directly, this package's own machinery) rather
// than the code that logged. A test file is never a wrapper: a test in this
// package that logs is the call site.
func isLoggerWrapperFrame(frame runtime.Frame) bool {
	if strings.HasSuffix(frame.File, "_test.go") {
		return false
	}
	for _, prefix := range loggerWrapperPrefixes {
		if strings.HasPrefix(frame.Function, prefix) {
			return true
		}
	}
	return false
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

// reportToSentry sends the record to Sentry as a log event: titled and
// grouped by its message and call site (frames, captured on the logging
// goroutine), with its fields as tags and context. Runs in a separate
// goroutine to avoid blocking log callers.
func (h *sentryHandler) reportToSentry(r slog.Record, frames []runtime.Frame) {
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

	// The suppression patterns match the error when there is one, and the
	// message otherwise.
	matchErr := capturedErr
	if matchErr == nil {
		matchErr = errors.New(r.Message)
	}

	// Completely silent — user-initiated cancellations.
	if matchesAny(matchErr, sentrySilentPatterns) {
		return
	}

	// Warn-only — log for visibility but don't send to Sentry.
	if matchesAny(matchErr, sentryWarnPatterns) {
		Warn("[Sentry] Suppressed non-actionable error",
			"error", matchErr.Error(),
			"log_message", r.Message,
		)
		return
	}

	// Add group prefix if any. Group names come from code, not users, but
	// they still pass the same tag check as everything else.
	if len(h.groups) > 0 {
		if group, ok := telemetry.TagValue("log_group", strings.Join(h.groups, ".")); ok {
			tags["log_group"] = group
		}
	}

	telemetry.CaptureLogEvent(telemetry.LogEvent{
		Message: r.Message,
		Frames:  frames,
		Err:     capturedErr,
		Tags:    tags,
		Extra:   extra,
	})
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
// the captured error (whose text and type the event carries as context and a
// tag), and every other attribute goes through collectField.
func (h *sentryHandler) collectAttr(a slog.Attr, tags map[string]string, extra map[string]interface{}, capturedErr *error) {
	key := a.Key
	val := a.Value.Resolve()

	// Check for error-typed attributes.
	if key == "error" || key == "err" {
		if e, ok := val.Any().(error); ok && e != nil {
			*capturedErr = e
			return
		}
		// Even if it's a string representation of an error, capture it.
		if val.Kind() == slog.KindString {
			*capturedErr = errors.New(val.String())
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
