package svcerr

import "connectrpc.com/connect"

// New constructs a classified error from a wire code and deliberate public
// detail. Prefer the named constructors when the code is known at the call
// site; New is for transport adapters whose code is only known at runtime.
// It uses the named kind's code, class and sentinel, with detail as the exact
// public text (unlike NotFound, it appends nothing). An empty detail uses the
// category's default text. An invalid code is Unknown.
//
// detail must be application-chosen copy, never an arbitrary diagnostic's
// Error() text. Attach private diagnostics separately with WithCause.
func New(code connect.Code, detail string) error {
	var sentinel error
	switch code {
	case connect.CodeCanceled:
		sentinel = ErrCanceled
	case connect.CodeUnknown:
		sentinel = ErrUnknown
	case connect.CodeInvalidArgument:
		sentinel = ErrInvalidArgument
	case connect.CodeDeadlineExceeded:
		sentinel = ErrDeadlineExceeded
	case connect.CodeNotFound:
		sentinel = ErrNotFound
	case connect.CodeAlreadyExists:
		sentinel = ErrAlreadyExists
	case connect.CodePermissionDenied:
		sentinel = ErrPermissionDenied
	case connect.CodeResourceExhausted:
		sentinel = ErrResourceExhausted
	case connect.CodeFailedPrecondition:
		sentinel = ErrFailedPrecondition
	case connect.CodeAborted:
		sentinel = ErrAborted
	case connect.CodeOutOfRange:
		sentinel = ErrOutOfRange
	case connect.CodeUnimplemented:
		sentinel = ErrUnimplemented
	case connect.CodeInternal:
		sentinel = ErrInternal
	case connect.CodeUnavailable:
		sentinel = ErrUnavailable
	case connect.CodeDataLoss:
		sentinel = ErrDataLoss
	case connect.CodeUnauthenticated:
		sentinel = ErrUnauthenticated
	default:
		code, sentinel = connect.CodeUnknown, ErrUnknown
	}
	if faultCode(code) {
		return faulted(sentinel, detail)
	}
	return wrapped(sentinel, detail)
}
