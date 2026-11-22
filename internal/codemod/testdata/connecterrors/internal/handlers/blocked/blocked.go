// Package blocked is a handler package with no reader of its own, imported
// by internal/app, which has one.
package blocked

import (
	"errors"

	"connectrpc.com/connect"
)

// Offline is what a daemon proxy returns when the machine is away.
func Offline() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected"))
}
