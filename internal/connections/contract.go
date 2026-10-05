// Copyright (c) 2025 Reliant Labs

// Package connections holds a user's saved logins to external services: the
// service behind ConnectionService, the OAuth broker, the refresh-capable
// token source, and the worker-side resolver that turns a run plus a
// connection reference into an authenticated request.
//
// Design: research/CONNECTIONS_VAULT.md. Secrets are sealed by internal/vault
// and live only in connection_secrets; nothing here returns a plaintext value
// except as a vault.Secret, which cannot be printed or serialized.
//
//forge:exclude-contract: reliant is not forge-generated; consumers declare the narrow interfaces they need
package connections

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/vault"
)

// ErrorCode classifies a failure for the layer that maps it onto a wire code.
type ErrorCode int

const (
	// CodeNotFound: the connection does not exist for this caller. It is also
	// what another user's connection looks like.
	CodeNotFound ErrorCode = iota + 1
	// CodeInvalidArgument: the request is malformed.
	CodeInvalidArgument
	// CodeAlreadyExists: a name collision.
	CodeAlreadyExists
	// CodeFailedPrecondition: the request is well-formed but cannot proceed
	// in the current state (no such connection for this run, integration not
	// configured, daemon-placed caller).
	CodeFailedPrecondition
	// CodeNeedsReauth: the provider refused the refresh grant. Permanent until
	// the user reconnects, so a workflow must not retry it.
	CodeNeedsReauth
	// CodeUnavailable: transient; the caller may retry.
	CodeUnavailable
	// CodeInternal: anything else.
	CodeInternal
)

// Error is a typed failure. Message is safe to show a user; it never contains a
// provider response body, a token, or an authorization code.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches by code, so errors.Is(err, connections.ErrNeedsReauth) works
// regardless of the message.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code && t.Message == ""
}

func newError(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Sentinels for errors.Is.
var (
	ErrNotFound           = &Error{Code: CodeNotFound}
	ErrInvalidArgument    = &Error{Code: CodeInvalidArgument}
	ErrAlreadyExists      = &Error{Code: CodeAlreadyExists}
	ErrFailedPrecondition = &Error{Code: CodeFailedPrecondition}
	ErrNeedsReauth        = &Error{Code: CodeNeedsReauth}
	ErrUnavailable        = &Error{Code: CodeUnavailable}

	// ErrDaemonPlacement is returned when a daemon-placed action asks for a
	// connection (CONNECTIONS_VAULT.md decision 6). It is also a
	// FailedPrecondition.
	ErrDaemonPlacement = errors.New("connections are not available to daemon-placed actions")
)

// CodeOf returns the code of err, or CodeInternal for an untyped error.
func CodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// Sealer is the slice of the vault this package needs.
type Sealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
	OpenSecret(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) (vault.Secret, error)
}

// SecretAAD is the associated data bound to a connection_secrets ciphertext:
// connection id and field (the vault adds the tenant). A blob copied onto
// another connection's row, or into another field, does not open.
func SecretAAD(connectionID, field string) []byte {
	return []byte("connection_secrets\x00" + connectionID + "\x00" + field)
}

// flowAAD binds a sealed PKCE verifier to its oauth_flows row.
func flowAAD(stateHash []byte) []byte {
	return append([]byte("oauth_flows\x00"), stateHash...)
}
