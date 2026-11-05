// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

// Sentinel errors for errors.Is. Every error the client returns for a failed
// call is an *Error; these are the conditions callers branch on.
var (
	// ErrNotFound: the run, trigger or question does not exist, or is not the
	// caller's. The server answers both the same way so ids do not leak.
	ErrNotFound = errors.New("reliant: not found")
	// ErrUnauthenticated: the token is missing, malformed, expired or revoked.
	ErrUnauthenticated = errors.New("reliant: unauthenticated")
	// ErrPermissionDenied: the token is valid but may not do this.
	ErrPermissionDenied = errors.New("reliant: permission denied")
	// ErrUnavailable: the server could not be reached or is not ready. Safe to
	// retry a read; a mutation may or may not have happened.
	ErrUnavailable = errors.New("reliant: unavailable")
	// ErrInvalidArgument: the request was rejected as malformed.
	ErrInvalidArgument = errors.New("reliant: invalid argument")
	// ErrFailedPrecondition: the request is well formed but the resource is not
	// in a state that allows it (resuming a run that is not paused, say).
	ErrFailedPrecondition = errors.New("reliant: failed precondition")
	// ErrAmbiguous: a trigger name matches triggers in more than one project;
	// use an id.
	ErrAmbiguous = errors.New("reliant: ambiguous name")
	// ErrNotApplied: the server answered, and said it did not do what was
	// asked (success=false). The message says why.
	ErrNotApplied = errors.New("reliant: not applied")
)

// Error is a failed call. Op names the operation, in the client's own words
// ("runs.start"); Server is the base URL it was sent to, so a caller with two
// clients can tell which one failed.
type Error struct {
	// Op is the client operation, e.g. "runs.get".
	Op string
	// Server is the base URL the call went to.
	Server string
	// Code is the Connect code, or connect.CodeUnknown for a failure that was
	// not an RPC status (ErrAmbiguous, ErrNotApplied).
	Code connect.Code
	// Message is the server's explanation, without the "code:" prefix.
	Message string

	kind  error
	cause error
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.kind.Error()
	}
	return fmt.Sprintf("reliant: %s on %s: %s", e.Op, e.Server, msg)
}

// Is makes errors.Is(err, ErrNotFound) and friends work.
func (e *Error) Is(target error) bool { return target == e.kind }

// Unwrap exposes the underlying Connect error, so connect.CodeOf works through
// it.
func (e *Error) Unwrap() error { return e.cause }

// wrap turns a call error into an *Error. Context cancellation and deadlines
// pass through unwrapped: they are the caller's own doing, and wrapping them
// would make errors.Is(err, context.Canceled) a question about Connect codes.
func (c *Client) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return &Error{Op: op, Server: c.baseURL, Code: connect.CodeUnavailable, Message: err.Error(), kind: ErrUnavailable, cause: err}
	}
	e := &Error{Op: op, Server: c.baseURL, Code: ce.Code(), Message: ce.Message(), cause: err}
	switch ce.Code() {
	case connect.CodeNotFound:
		e.kind = ErrNotFound
	case connect.CodeUnauthenticated:
		e.kind = ErrUnauthenticated
	case connect.CodePermissionDenied:
		e.kind = ErrPermissionDenied
	case connect.CodeUnavailable:
		e.kind = ErrUnavailable
	case connect.CodeInvalidArgument:
		e.kind = ErrInvalidArgument
	case connect.CodeFailedPrecondition:
		e.kind = ErrFailedPrecondition
	default:
		e.kind = fmt.Errorf("reliant: %s", ce.Code())
	}
	return e
}

// fail builds an *Error for a condition the client itself found.
func (c *Client) fail(op string, kind error, format string, args ...any) error {
	return &Error{Op: op, Server: c.baseURL, Code: connect.CodeUnknown, Message: fmt.Sprintf(format, args...), kind: kind}
}

// applied turns a success=false mutation answer into an error. A server that
// answers 200 with success=false has declined, and a caller that ignores the
// flag believes it cancelled a run it did not.
func (c *Client) applied(op string, success bool, message string) error {
	if success {
		return nil
	}
	if message == "" {
		message = "the server did not apply the request"
	}
	return c.fail(op, ErrNotApplied, "%s", message)
}
