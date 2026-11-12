// Package interceptors runs OUTSIDE svcerr.Interceptor, so a bare svcerr
// kind returned here would reach connect-go as unknown: every rewrite stays
// a *connect.Error.
package interceptors

import (
	"errors"

	"connectrpc.com/connect"
)

// Deny is what an auth interceptor returns for a request with no token.
func Deny() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
}
