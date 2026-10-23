// File: error_class.go — how loud an error is depends on WHO must act on it.
//
// A log level is a routing decision. ERROR is what pages: the alert rule
// matches it, a Sentry handler forwards it, a post-deploy log gate fails the
// release on it. So an error the user or caller caused — a disconnected
// laptop, an exhausted provider subscription, a typo in a field — must not be
// ERROR, however the call site spelled its log line. In one measured week a
// product's ERROR stream was dominated by exactly those, and the real faults
// (a nondeterministic workflow, a read-only config directory) were buried
// under them.
//
// svcerr.Classify decides who must act; this file turns that into a level:
//
//	class     level                                   reaches Sentry / ERROR alerts
//	server    ERROR (WARN: DeadlineExceeded, Aborted,  yes, at ERROR
//	          Unimplemented — retryable, not a page)
//	user      DefaultUserErrorLevel (INFO), or         no
//	          WithUserErrorLevel
//	canceled  the same level as user                  no
//
// Nothing is hidden: a user error is still written, with its error,
// error_class=user and its code, at a level production keeps. It simply
// stops claiming to be an incident. The volume stays countable — the RPC
// error counter carries error_class and code.
//
// The policy applies at three layers, so it holds however an error is logged:
// LoggingInterceptor's "rpc failed", LogMiddleware's component records, and —
// for every hand-written logger.Error in the process — NewErrorClassHandler,
// which wraps the process's slog handler. Wrapping BELOW a Sentry or metrics
// handler is the point: those handlers gate on the record's level, so the
// policy reaches them without either knowing about svcerr.

package observe

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// DefaultUserErrorLevel is the level a user error or a cancellation logs at
// unless WithUserErrorLevel says otherwise: visible under a production INFO
// handler, below anything that pages.
const DefaultUserErrorLevel = slog.LevelInfo

// ErrorClassKey is the attribute every error record carries: the
// svcerr.Class of its error ("server", "user", "canceled").
const ErrorClassKey = "error_class"

// WithUserErrorLevel sets the level for errors that are not a server fault —
// svcerr.ClassUser and svcerr.ClassCanceled — on one layer. The default is
// DefaultUserErrorLevel (INFO); slog.LevelWarn is the usual alternative for a
// deployment that wants them in a WARN view. It is a CEILING: a call site
// that logged a user error below it (DEBUG) keeps its level.
func WithUserErrorLevel(level slog.Level) LogOption {
	return func(p *logPolicy) { p.userLevel = level }
}

// WithErrorClassifier adds fault attribution svcerr.Classify cannot derive —
// an error type from a library the app does not own, such as a workflow
// engine's application error whose category travelled across a
// serialization boundary the svcerr marker cannot cross.
//
// classify returns svcerr.ClassNone for "no opinion": the next classifier is
// asked, and svcerr.Classify last. Classifiers accumulate across options, in
// order; each must be safe for concurrent use. A nil classify is ignored.
func WithErrorClassifier(classify func(err error) svcerr.Class) LogOption {
	return func(p *logPolicy) {
		if classify != nil {
			p.classifiers = append(p.classifiers, classify)
		}
	}
}

// classOf is svcerr.Classify behind the layer's declared classifiers.
func (p *logPolicy) classOf(err error) svcerr.Class {
	if err == nil {
		return svcerr.ClassNone
	}
	for _, classify := range p.classifiers {
		if class := classify(err); class != svcerr.ClassNone {
			return class
		}
	}
	return svcerr.Classify(err)
}

// failureLevel is the level of a record carrying err, a failure of class.
func (p *logPolicy) failureLevel(err error, class svcerr.Class) slog.Level {
	switch class {
	case svcerr.ClassNone:
		return slog.LevelInfo
	case svcerr.ClassUser, svcerr.ClassCanceled:
		return p.userLevel
	}
	// A server fault. The retryable codes are WARN: a deadline or a
	// transaction conflict says "try again", and an unimplemented rpc is a
	// gap in the API, not an outage. Everything else — Internal, Unknown,
	// DataLoss, Unavailable, an unrecognised error, and a 4xx explicitly
	// marked ClassServer — is a page.
	switch svcerr.Code(err) {
	case connect.CodeDeadlineExceeded, connect.CodeAborted, connect.CodeUnimplemented:
		return slog.LevelWarn
	default:
		return slog.LevelError
	}
}

// errorAttrs renders a failed call's error for the LOG, which is not the
// same thing as rendering it for the client.
//
// `error` is what the caller was told. `cause` is what actually happened:
// svcerr redacts the message of an unrecognised internal failure before it
// reaches the wire, so without this attribute the driver text — the SQLSTATE,
// the constraint, the panic value — would exist in exactly no place. The
// generated ORM records it on the active span too, but OTEL_EXPORTER_OTLP_
// ENDPOINT is empty by default, so that span goes nowhere in the default
// configuration and cannot be the only copy. `error_class` and `code` say who
// must act and what the client received, so a query can split the two.
//
// SANITIZE THE WIRE, NEVER THE LOG.
func errorAttrs(err error, class svcerr.Class) []slog.Attr {
	attrs := []slog.Attr{
		slog.Any("error", err),
		slog.String(ErrorClassKey, class.String()),
		slog.String("code", svcerr.Code(err).String()),
	}
	if cause := svcerr.Cause(err); cause != nil {
		attrs = append(attrs, slog.String("cause", cause.Error()))
	}
	return attrs
}

// NewErrorClassHandler wraps inner so that EVERY record in the process obeys
// the error-class policy, not only the ones forge's interceptors write. A
// record above the user-error level that carries an error is classified:
//
//   - it gains error_class=<class> (unless it already carries one — a layer
//     that classified it, such as LoggingInterceptor, is trusted);
//   - if the error is not a server fault, its level is lowered to the
//     user-error level. A level is only ever lowered: a server error logged
//     at WARN stays WARN, because the call site chose that.
//
// The error is the first attribute whose value is an `error`, whatever its
// key ("error", "err", logr's "err"). Attributes bound earlier with
// Logger.With are not searched; an error belongs on the record that reports
// it. A record with no error attribute, or at or below the user-error level,
// passes through untouched and is not scanned.
//
// Wrap it OUTSIDE anything that routes on level — a Sentry forwarder, an
// error counter — so they see the classified level:
//
//	handler := slog.NewJSONHandler(os.Stdout, opts)
//	handler = sentryHandler(handler)           // forwards >= ERROR
//	handler = observe.NewErrorClassHandler(handler)
//	slog.SetDefault(slog.New(handler))
//
// opts are WithUserErrorLevel and WithErrorClassifier; other LogOptions
// concern call outcomes and are ignored here.
func NewErrorClassHandler(inner slog.Handler, opts ...LogOption) slog.Handler {
	return &errorClassHandler{inner: inner, policy: newLogPolicy(slog.LevelInfo, opts)}
}

type errorClassHandler struct {
	inner  slog.Handler
	policy *logPolicy
}

func (h *errorClassHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *errorClassHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level <= h.policy.userLevel {
		return h.inner.Handle(ctx, r)
	}
	err, alreadyClassified := recordError(r)
	if err == nil || alreadyClassified {
		return h.inner.Handle(ctx, r)
	}
	class := h.policy.classOf(err)
	// Clone before mutating: a record's attribute storage may be shared
	// with the caller's copy.
	r = r.Clone()
	r.AddAttrs(slog.String(ErrorClassKey, class.String()))
	if class == svcerr.ClassUser || class == svcerr.ClassCanceled {
		r.Level = min(r.Level, h.policy.userLevel)
		// The caller asked Enabled about the level it wrote; the record
		// now has a lower one, which the inner handler may not want.
		if !h.inner.Enabled(ctx, r.Level) {
			return nil
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *errorClassHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &errorClassHandler{inner: h.inner.WithAttrs(attrs), policy: h.policy}
}

func (h *errorClassHandler) WithGroup(name string) slog.Handler {
	return &errorClassHandler{inner: h.inner.WithGroup(name), policy: h.policy}
}

// recordError returns the first error-valued attribute of r, and whether r
// already carries an error_class.
func recordError(r slog.Record) (err error, classified bool) {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == ErrorClassKey {
			classified = true
			return false
		}
		if err == nil {
			if e, ok := a.Value.Resolve().Any().(error); ok && e != nil {
				err = e
			}
		}
		return true
	})
	return err, classified
}
