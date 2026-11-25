// Package reader is a handler package that reads a *connect.Error in
// process (IsMissing). A bare svcerr kind would read as unknown there, so
// every rewrite in this package stays a *connect.Error.
package reader

import (
	"errors"

	"connectrpc.com/connect"
)

// Lookup fails the way a by-key get does.
func Lookup() error {
	return connect.NewError(connect.CodeNotFound, errors.New("row not found"))
}

// IsMissing is the in-process reader.
func IsMissing(err error) bool { return connect.CodeOf(err) == connect.CodeNotFound }
