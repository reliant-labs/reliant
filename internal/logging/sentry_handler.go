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
	// its own, whose stack says nothing about who logged. So is the
	// reporter: the record belongs to the one in force when it was logged.
	// Read on the report goroutine, a record logged just before a reporter
	// swap reached the reporter installed after it.
	frames := callerFrames(r.PC)
	go h.reportToSentry(telemetry.GetReporter(), r, frames)

	return err
}

// maxLogStackDepth bounds the stack captured for a forwarded record.
const maxLogStackDepth = 64

// slogFramePrefix marks slog's own frames. For a record with no PC, the
// frames below the outermost slog frame are taken to be handler machinery.
const slogFramePrefix = "log/slog."

// loggerWrapperPrefixes are the loggers that adapt into slog from the call
// site's side: this package's Error/Warn/... and the Temporal loggers. A
// record's PC can name one of them — every logging.Error call records
// logging.Error as its caller, and a workflow logger can record the SDK's
// replay wrapper — so the call site is the first frame from the PC outward
// that is not one of these.
var loggerWrapperPrefixes = []string{
	reflect.TypeOf(sentryHandler{}).PkgPath() + ".",
	"go.temporal.io/sdk/log.",
	"go.temporal.io/sdk/internal/log.",
}

// callerFrames is the stack of the code that logged, innermost first, so
// frames[0] is the call site. pc is the record's.
//
// The record's PC is the anchor. Every front end records it outside the
// handler machinery: slog's Logger records the caller of Logger.Error; the
// Temporal SDK's adapter records the caller of its Error, building the record
// itself and calling the handler directly, so no slog frame is on the stack
// at all; this package's Error records logging.Error. So the call site is the
// frame the PC names, moved outward past any logger wrapper, and every handler
// between there and here — this one, the metrics bridge, forge's error-class
// policy, whatever wraps them next — is passed over without being named.
//
// The call site used to be found as the first frame past slog's. On a
// Temporal-logged record there is none, so forge's errorClassHandler.Handle,
// the outermost handler, was named the culprit of every ERROR the workflow
// and activity loggers wrote (ELECTRON-CM, CK, CE). That rule survives only
// for a record that carries no PC.
func callerFrames(pc uintptr) []runtime.Frame {
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

	anchor, ok := frameAt(pc)
	if !ok {
		return pastLoggerWrappers(stack[pastSlog(stack):])
	}
	for i, frame := range stack {
		if sameLocation(frame, anchor) {
			return pastLoggerWrappers(stack[i:])
		}
	}
	// The record was built on another goroutine and handed to this one —
	// a handler that queues records does that — so this stack says nothing
	// about who logged. The PC still does.
	return []runtime.Frame{anchor}
}

// frameAt is the frame pc names: when the call was inlined, the innermost
// function, which is the frame runtime.Callers gave that PC.
func frameAt(pc uintptr) (runtime.Frame, bool) {
	if pc == 0 {
		return runtime.Frame{}, false
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	return frame, frame.Function != ""
}

// sameLocation compares frames by where they are, not by PC: a front end that
// records the PC of the call instruction (runtime.Caller's) rather than the
// return address (runtime.Callers') still names the same line.
func sameLocation(a, b runtime.Frame) bool {
	return a.Function == b.Function && a.File == b.File && a.Line == b.Line
}

// pastSlog is the index of the first frame past the first run of slog
// frames, or 0 when slog is not on the stack.
func pastSlog(stack []runtime.Frame) int {
	inSlog := false
	for i, frame := range stack {
		if strings.HasPrefix(frame.Function, slogFramePrefix) {
			inSlog = true
			continue
		}
		if inSlog {
			return i
		}
	}
	return 0
}

// pastLoggerWrappers drops the leading frames that are loggers adapting into
// slog rather than the code that logged.
func pastLoggerWrappers(stack []runtime.Frame) []runtime.Frame {
	start := 0
	for start < len(stack) && isLoggerWrapperFrame(stack[start]) {
		start++
	}
	return stack[start:]
}

// isLoggerWrapperFrame reports whether frame is a logger adapting into slog
// (or, for a record with no PC, this package's own machinery) rather than the
// code that logged. A test file is never a wrapper: a test in this package
// that logs is the call site.
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

// reportToSentry sends the record to Sentry, through reporter, as a log event:
// titled and grouped by its message and call site (frames, captured on the
// logging goroutine), with its fields as tags and context. Runs in a separate
// goroutine to avoid blocking log callers.
func (h *sentryHandler) reportToSentry(reporter telemetry.ErrorReporter, r slog.Record, frames []runtime.Frame) {
	// Every attribute, resolved once: the logger's (from WithAttrs calls),
	// then the record's own.
	attrs := make([]slog.Attr, 0, len(h.preformatted)+r.NumAttrs())
	for _, a := range h.preformatted {
		attrs = append(attrs, slog.Attr{Key: a.Key, Value: a.Value.Resolve()})
	}
	own := len(attrs)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, slog.Attr{Key: a.Key, Value: a.Value.Resolve()})
		return true
	})

	// The error travels as the event's context; everything else is judged
	// by collectField.
	capturedErr := recordError(attrs[own:], attrs[:own])
	tags := make(map[string]string)
	extra := make(map[string]interface{})
	for _, a := range attrs {
		if !isErrorAttr(a) {
			collectField(a.Key, a.Value, tags, extra)
		}
	}

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

	telemetry.CaptureLogEventWith(reporter, telemetry.LogEvent{
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

// recordError is the error a forwarded record carries, whose text and type
// the event shows as context and a tag: the record's first error-valued
// attribute whatever its key, else the logger's. That is the rule forge's
// error-class policy classifies a record by, so Sentry shows the error that
// decided the record's level. Failing an error value, an "error"/"err" string
// is the error's text.
//
// Only "error"/"err" used to be read. The Temporal SDK logs its errors under
// "Error", so its "Activity error." line reached Sentry with no error at all
// (ELECTRON-CK).
func recordError(own, logger []slog.Attr) error {
	for _, attrs := range [][]slog.Attr{own, logger} {
		for _, a := range attrs {
			if err := attrError(a); err != nil {
				return err
			}
		}
	}
	for _, attrs := range [][]slog.Attr{own, logger} {
		for _, a := range attrs {
			if isErrorText(a) {
				return errors.New(a.Value.String())
			}
		}
	}
	return nil
}

// isErrorAttr reports whether a is one recordError reads. Error text is
// content: whichever error the event carries, none becomes a tag.
func isErrorAttr(a slog.Attr) bool {
	return attrError(a) != nil || isErrorText(a)
}

func attrError(a slog.Attr) error {
	if err, ok := a.Value.Any().(error); ok && err != nil {
		return err
	}
	return nil
}

func isErrorText(a slog.Attr) bool {
	return (a.Key == "error" || a.Key == "err") && a.Value.Kind() == slog.KindString
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
