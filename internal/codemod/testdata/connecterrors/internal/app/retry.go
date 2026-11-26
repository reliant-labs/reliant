// Package app imports handler package blocked and reads a *connect.Error in
// process: a bare svcerr kind from blocked would read differently here, so
// blocked's rewrites stay *connect.Error.
package app

import (
	"errors"

	"connectrpc.com/connect"

	"example.com/cescratch/internal/handlers/blocked"
)

// Retryable reports whether err is a *connect.Error with CodeUnavailable.
func Retryable(err error) bool {
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodeUnavailable
}

// Probe reads a handler error in process.
func Probe() bool { return Retryable(blocked.Offline()) }
