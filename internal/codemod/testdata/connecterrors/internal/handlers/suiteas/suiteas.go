// Package suiteas is testifyas with the As-style helper spelled as a METHOD —
// testify's require.New(t).ErrorAs, or a suite's s.ErrorAs. The callee is no
// package-level function, so only the argument's type names the reader.
package suiteas

import (
	"errors"

	"connectrpc.com/connect"
)

// Deny is what the rpc returns for an unknown approval.
func Deny() error {
	return connect.NewError(connect.CodeNotFound, errors.New("approval not found"))
}
