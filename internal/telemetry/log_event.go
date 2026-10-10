// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"github.com/getsentry/sentry-go"

	"github.com/reliant-labs/reliant/internal/errclass"
)

// ERROR logs forwarded to Sentry.
//
// A log line is a message, a code location and some fields — not an
// exception. Sending it as one (errors.New(message), or the record's wrapped
// error) made every issue an "errors.errorString" (or "*fmt.wrapError") with
// no stack and no fingerprint, so Sentry grouped on whatever text the value
// happened to hold: one issue per distinct id embedded in an error string, and
// unrelated call sites that shared an error type merged into one.
//
// A LogEvent is sent as what it is: a message event, titled by the log
// message, grouped by the message and the function that logged it, located by
// the logging call's stack, and carrying the record's fields as tags and
// context. Repeats of one log line from one place are one issue, whatever ids
// and error text each carries.

// LogEventLogger is the Sentry "logger" every forwarded log record carries,
// and the first element of its fingerprint.
const LogEventLogger = "slog"

// LogEvent is one ERROR-level log record to report.
type LogEvent struct {
	// Message is the record's message: the issue's title and, with the call
	// site, its grouping key.
	Message string
	// Frames is the stack of the logging call, innermost first: Frames[0] is
	// the function that logged. Empty when it could not be captured.
	Frames []runtime.Frame
	// Err is the record's error attribute, if it had one. Its text and type
	// travel as context and a tag; it never becomes the event's exception.
	Err error
	// Tags and Extra are the record's other fields, already narrowed to what
	// may leave the process; BeforeSend scrubs them again.
	Tags  map[string]string
	Extra map[string]any
}

// LogEventReporter is a reporter that can deliver a forwarded log record.
type LogEventReporter interface {
	CaptureLogEvent(event LogEvent) string
}

// CaptureLogEvent reports event with the process reporter and returns the
// Sentry event id, or "" when nothing was sent.
//
// The same class gate as CaptureException: only a server fault is sent. The
// record's error is what is classified — or, for a record without one, its
// message, as the bridge always has.
func CaptureLogEvent(event LogEvent) string {
	return CaptureLogEventWith(GetReporter(), event)
}

// CaptureLogEventWith is CaptureLogEvent with the reporter chosen by the
// caller: one that reports asynchronously reads the process reporter when the
// record is logged, so a reporter installed afterwards never receives it.
func CaptureLogEventWith(reporter ErrorReporter, event LogEvent) string {
	classified := event.Err
	if classified == nil {
		classified = errors.New(event.Message)
	}
	if !errclass.IsServerError(classified) {
		return ""
	}
	if reporter, ok := reporter.(LogEventReporter); ok {
		return reporter.CaptureLogEvent(event)
	}
	return ""
}

// CaptureLogEvent sends a forwarded log record as one Sentry message event.
func (r *SentryReporter) CaptureLogEvent(event LogEvent) string {
	r.mu.RLock()
	initialized := r.initialized
	hub := r.hub
	r.mu.RUnlock()

	if !initialized {
		return ""
	}
	if hub == nil {
		hub = sentry.CurrentHub()
	}
	if eventID := hub.CaptureEvent(logEventToSentry(event)); eventID != nil {
		return string(*eventID)
	}
	return ""
}

// CaptureLogEvent is a no-op.
func (r *NoopReporter) CaptureLogEvent(LogEvent) string { return "" }

var (
	_ LogEventReporter = (*SentryReporter)(nil)
	_ LogEventReporter = (*NoopReporter)(nil)
)

// logEventToSentry is the event for a forwarded record, before BeforeSend
// scrubs it.
func logEventToSentry(record LogEvent) *sentry.Event {
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Logger = LogEventLogger
	event.Message = record.Message

	callSite := ""
	if len(record.Frames) > 0 {
		callSite = record.Frames[0].Function
	}
	event.Fingerprint = LogEventFingerprint(record.Message, callSite)
	if callSite != "" {
		// Rendered as the issue's culprit, under its title.
		event.Transaction = shortFunctionName(stableFunctionName(callSite))
	}

	tags := make(map[string]string, len(record.Tags)+2)
	for key, value := range record.Tags {
		tags[key] = value
	}
	tags["log_source"] = LogEventLogger
	extra := make(map[string]any, len(record.Extra)+1)
	for key, value := range record.Extra {
		extra[key] = value
	}
	if record.Err != nil {
		extra["error"] = record.Err.Error()
		if errorType := innermostErrorType(record.Err); errorType != "" {
			tags["error_type"] = errorType
		}
	}
	event.Tags = tags
	if len(extra) > 0 {
		event.Contexts["extra"] = sentry.Context(extra)
	}

	if len(record.Frames) > 0 {
		event.Threads = []sentry.Thread{{
			Current:    true,
			Stacktrace: stacktraceOf(record.Frames),
		}}
	}
	return event
}

// LogEventFingerprint is the grouping key of a forwarded log record: the
// source, the function that logged, and the message with its variable parts
// folded. Message alone would merge unrelated call sites that happen to say
// the same thing ("Failed to update status"); call site alone would merge
// every distinct line one function logs. A message that interpolates an id or
// a count stays one issue across values.
//
// The function, not the file and line: a line number moves with every edit
// above it, and each move would open a new issue for the same defect.
func LogEventFingerprint(message, function string) []string {
	if function == "" {
		function = "unknown"
	} else {
		function = stableFunctionName(function)
	}
	return []string{LogEventLogger, function, normalizeLogMessage(message)}
}

var (
	uuidPattern   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	numberPattern = regexp.MustCompile(`\d+`)

	// closureSuffix is the compiler's naming for closures and go-statement
	// wrappers (".func1", ".func2.3", ".gowrap1"). Its numbers shift when a
	// closure is added above another, so it is dropped: an issue belongs to
	// the named function.
	closureSuffix = regexp.MustCompile(`(\.(func|gowrap)\d+|\.\d+)+$`)
)

// normalizeLogMessage folds the parts of a message that vary between
// occurrences of one log line: ids and numbers.
func normalizeLogMessage(message string) string {
	message = uuidPattern.ReplaceAllString(message, "<id>")
	return numberPattern.ReplaceAllString(message, "<n>")
}

// stableFunctionName is a runtime function name without closure suffixes.
func stableFunctionName(function string) string {
	return closureSuffix.ReplaceAllString(function, "")
}

// shortFunctionName drops the import path: "reconciliation.(*Reconciler).reap"
// rather than "github.com/.../workflow/reconciliation.(*Reconciler).reap".
func shortFunctionName(function string) string {
	if slash := strings.LastIndex(function, "/"); slash >= 0 {
		return function[slash+1:]
	}
	return function
}

// innermostErrorType is the type of the deepest error in err's chain, which
// names what actually failed — the outer layers are wrapping. A leading "*"
// is dropped so the name has the shape of a tag value ("pgconn.PgError").
func innermostErrorType(err error) string {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			break
		}
		err = inner
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
}

// stacktraceOf converts innermost-first runtime frames into Sentry's order,
// outermost first.
func stacktraceOf(frames []runtime.Frame) *sentry.Stacktrace {
	out := make([]sentry.Frame, 0, len(frames))
	for i := len(frames) - 1; i >= 0; i-- {
		out = append(out, sentry.NewFrame(frames[i]))
	}
	return &sentry.Stacktrace{Frames: out}
}
