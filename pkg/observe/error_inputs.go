// File: error_inputs.go — what a server error record carries so it can be
// grouped into an issue.
//
// A log record at ERROR or above is the unit of a server error. Grouping needs
// three things the message alone does not give: WHERE it was logged, WHAT KIND
// of failure it carries, and — for a panic — WHERE it panicked. This file adds
// them to every ERROR record, as ordinary attributes, so they reach BOTH
// shipping paths: the stdout JSON line the collector's file reader parses, and
// the OTLP logs bridge. Below ERROR nothing is added and nothing is allocated.
//
//	attribute                    source
//	code.function.name,          the record's PC. A caller that builds its own
//	code.file.path,              record and calls Handler().Handle directly
//	code.line.number             (Temporal's slog adapter) still names ITS call
//	                             site, not a frame inside this package. The key
//	                             names are the OTel semconv ones the otelslog
//	                             bridge's WithSource emits.
//	exception.type,              the innermost error of the record's first
//	exception.message            error-valued attribute; %T and the message
//	                             bounded to MaxExceptionMessage bytes.
//	exception.stacktrace         LogPanic only.
//	error.group                  ErrorGroup, opt-in.
//
// otelslog.WithSource is deliberately not what produces these: it is
// bridge-only (the stdout line, the default shipping path, would carry
// nothing), all-or-nothing per bridge (every INFO record would pay a frame
// lookup), and it would leave the two paths with different attributes.

package observe

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Log attribute keys an ERROR record carries for issue grouping.
const (
	LogKeyExceptionType       = "exception.type"
	LogKeyExceptionMessage    = "exception.message"
	LogKeyExceptionStacktrace = "exception.stacktrace"
	LogKeyErrorGroup          = "error.group"
)

const (
	// MaxExceptionMessage bounds exception.message, in bytes, ellipsis
	// included.
	MaxExceptionMessage = 1 << 10
	// maxStacktrace bounds exception.stacktrace. The top of the stack — where
	// the panic happened — is the part kept.
	maxStacktrace = 16 << 10

	ellipsis = "…"
)

// ErrorGroup returns the attribute that pins a record to a named issue,
// whatever its message, call site or error type: the escape hatch when the
// derived grouping is wrong for one failure.
//
//	logger.ErrorContext(ctx, "reconcile drift", "err", err,
//		observe.ErrorGroup(ctx, "billing-reconcile-drift"))
func ErrorGroup(_ context.Context, name string) slog.Attr {
	return slog.String(LogKeyErrorGroup, name)
}

// LogPanic logs a recovered panic at ERROR on logger. Call it from the
// deferred function that called recover():
//
//	defer func() {
//		if rec := recover(); rec != nil {
//			observe.LogPanic(ctx, logger, "panic recovered", rec, slog.String("path", p))
//		}
//	}()
//
// The record's PC is the PANICKING frame, not the recover site, so a handler
// reading it (otelslog's code.function.name, slog's AddSource) groups panics
// by where they happened. The stack goes in exception.stacktrace; "panic" is
// the recovered value.
func LogPanic(ctx context.Context, logger *slog.Logger, msg string, recovered any, attrs ...slog.Attr) {
	if logger == nil {
		logger = slog.Default()
	}
	if !logger.Enabled(ctx, slog.LevelError) {
		return
	}
	r := slog.NewRecord(time.Now(), slog.LevelError, msg, panicPC())
	r.AddAttrs(attrs...)
	r.AddAttrs(slog.Any("panic", recovered))

	var typ, message string
	if err, ok := recovered.(error); ok && err != nil {
		inner := innermostError(err)
		typ, message = fmt.Sprintf("%T", inner), inner.Error()
	} else {
		typ, message = fmt.Sprintf("%T", recovered), fmt.Sprint(recovered)
	}
	r.AddAttrs(
		slog.String(LogKeyExceptionType, typ),
		slog.String(LogKeyExceptionMessage, boundString(message, MaxExceptionMessage)),
		slog.String(LogKeyExceptionStacktrace, boundString(string(debug.Stack()), maxStacktrace)),
	)
	_ = logger.Handler().Handle(ctx, r)
}

// panicPC returns the PC of the frame that panicked: the first frame above
// runtime.gopanic that is not itself runtime machinery (sigpanic, the
// goPanicIndex family). Called outside a panic it returns 0, and the record
// then has no source.
func panicPC() uintptr {
	var pcs [48]uintptr
	n := runtime.Callers(2, pcs[:])
	seenPanic := false
	for _, pc := range pcs[:n] {
		f, _ := runtime.CallersFrames([]uintptr{pc}).Next()
		switch {
		case !seenPanic:
			seenPanic = f.Function == "runtime.gopanic"
		case !strings.HasPrefix(f.Function, "runtime."):
			return pc
		}
	}
	return 0
}

// innermostError is the error a record is really about: it prefers the
// server-only cause svcerr withheld from the client (otherwise every redacted
// internal error is the same "internal server error" string), then follows
// Unwrap to the end, taking the first branch of a multi-error.
func innermostError(err error) error {
	if cause := svcerr.Cause(err); cause != nil {
		err = cause
	}
	for {
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			next := u.Unwrap()
			if next == nil {
				return err
			}
			err = next
		case interface{ Unwrap() []error }:
			errs := u.Unwrap()
			if len(errs) == 0 || errs[0] == nil {
				return err
			}
			err = errs[0]
		default:
			return err
		}
	}
}

// addErrorInputs adds the source location and, when the record carries an
// error, exception.type and exception.message to an ERROR record. An
// attribute the caller already set (LogPanic, or a call site naming its own
// exception.type) is left alone.
func addErrorInputs(r slog.Record) slog.Record {
	var haveType, haveFunc bool
	var err error
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case LogKeyExceptionType:
			haveType = true
		case string(semconv.CodeFunctionNameKey):
			haveFunc = true
		}
		if err == nil {
			if e, ok := a.Value.Resolve().Any().(error); ok && e != nil {
				err = e
			}
		}
		return true
	})
	wantSource := r.PC != 0 && !haveFunc
	wantException := err != nil && !haveType
	if !wantSource && !wantException {
		return r
	}
	// Clone before mutating: the record's attribute storage may be shared
	// with the caller's copy.
	r = r.Clone()
	if wantSource {
		f, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		r.AddAttrs(
			slog.String(string(semconv.CodeFunctionNameKey), f.Function),
			slog.String(string(semconv.CodeFilePathKey), f.File),
			slog.Int(string(semconv.CodeLineNumberKey), f.Line),
		)
	}
	if wantException {
		err = innermostError(err)
		r.AddAttrs(
			slog.String(LogKeyExceptionType, fmt.Sprintf("%T", err)),
			slog.String(LogKeyExceptionMessage, boundString(err.Error(), MaxExceptionMessage)),
		)
	}
	return r
}

// boundString cuts s to at most max bytes, ellipsis included, on a rune
// boundary.
func boundString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return truncate(s, max-len(ellipsis))
}

// errorInputsHandler adds the grouping inputs to ERROR records for next. It
// wraps the whole log handler rather than one branch: slog.MultiHandler hands
// each branch its own copy of the record, so what one branch adds never
// reaches the other.
//
// Under logger.WithGroup the attributes nest in the group, as trace_id does.
type errorInputsHandler struct{ next slog.Handler }

func (h errorInputsHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h errorInputsHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelError {
		return h.next.Handle(ctx, r)
	}
	return h.next.Handle(ctx, addErrorInputs(r))
}

func (h errorInputsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return errorInputsHandler{next: h.next.WithAttrs(attrs)}
}

func (h errorInputsHandler) WithGroup(name string) slog.Handler {
	return errorInputsHandler{next: h.next.WithGroup(name)}
}
