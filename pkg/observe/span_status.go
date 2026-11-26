// File: span_status.go — how a failed call reads on its server span.
//
// A server span's status answers one question for every span-based view —
// `forge obs endpoints`, `traces`, `status`, release health, HyperDX's own
// error filter: did the server fail? The Trace slot cannot answer it.
// otelconnect knows only the wire code, and marks a server span Error for
// Unknown, DeadlineExceeded, Unimplemented, Internal, Unavailable and DataLoss
// whoever was at fault. But a user-class Unavailable ("your machine is waking
// up") is the API working, and so is a canceled-class one (a drain, a peer cut
// so it redials). In reliant prod the first alone was thousands of Error spans
// a day, while logs (which honour the class) and rpc.server.errors (which
// labels it) said nobody had to act.
//
// So the server span of a failed call carries the svcerr class and reason, and
// its status follows the class, not the code:
//
//   - user or canceled class: status OK. OK is the one status a later layer
//     cannot overwrite (the SDK ignores Error after OK), and otelconnect sets
//     its status after the chain returns, so Unset would lose to its Error.
//   - any other class (a server fault): status Error, as before.
//
// Either way the error is recorded as an exception event (with the stack svcerr
// captured for a server fault), error_class names who must act, and
// rpc.error_reason carries the svcerr.WithReason code the client received.
// An expected close (svcerr.ExpectedClose) keeps its own shape (RecordSpanError).
//
// The wire is untouched: the client receives the same code, message and reason.

package observe

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// AttrErrorReason is the server-span attribute carrying the svcerr.WithReason
// code of a failed call — the svcerr.ReasonHeader value the client received.
const AttrErrorReason = "rpc.error_reason"

// ServerSpanOption tunes how a failed call reads on its server span
// (RequestSpanInterceptorWith; Deps.ServerSpan in Chain).
type ServerSpanOption func(*serverSpanPolicy)

// WithUserErrorsAsSpanErrors marks the server span of a call that ended with a
// user-class error Error, as for a server fault. The default leaves it OK: a
// user error is the server correctly refusing, which no error rate should
// count. Use it only when the project's span-based error rates must count user
// refusals; rpc.server.errors already counts them, labelled error_class=user.
// Canceled-class endings stay OK either way.
func WithUserErrorsAsSpanErrors() ServerSpanOption {
	return func(p *serverSpanPolicy) { p.userErrorsAsErrors = true }
}

type serverSpanPolicy struct {
	userErrorsAsErrors bool
}

func newServerSpanPolicy(opts []ServerSpanOption) serverSpanPolicy {
	var p serverSpanPolicy
	for _, opt := range opts {
		if opt != nil {
			opt(&p)
		}
	}
	return p
}

// settle records how a server call that ended with err reads on its server
// span (see the file comment). nil-safe on both.
func (p serverSpanPolicy) settle(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	if reason, ok := svcerr.CloseReason(err); ok {
		recordSpanClose(span, err, reason)
		return
	}
	class := svcerr.Classify(err)
	attrs := []attribute.KeyValue{attribute.String(ErrorClassKey, class.String())}
	if reason, ok := svcerr.ReasonOf(err); ok {
		attrs = append(attrs, attribute.String(AttrErrorReason, reason))
	}
	span.SetAttributes(attrs...)
	if st, ok := svcerr.StackOf(err); ok {
		span.RecordError(err, trace.WithAttributes(attribute.String(LogKeyExceptionStacktrace, st.Trace)))
	} else {
		span.RecordError(err)
	}
	if class == svcerr.ClassCanceled || (class == svcerr.ClassUser && !p.userErrorsAsErrors) {
		span.SetStatus(codes.Ok, "")
		return
	}
	span.SetStatus(codes.Error, err.Error())
}
