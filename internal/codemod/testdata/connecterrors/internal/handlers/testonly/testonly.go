// Package testonly is a handler package only a TEST imports: the tests of
// internal/handlers/reader, whose code reads a *connect.Error. reader's code
// never imports testonly, so it never sees testonly's errors: a test-only
// import is not an edge, and testonly gets bare kinds.
package testonly

import (
	"errors"

	"connectrpc.com/connect"
)

// Missing fails the way a by-key get does.
func Missing() error {
	return connect.NewError(connect.CodeNotFound, errors.New("thing not found"))
}
