package authn

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
)

// Claims is the canonical claims type carried through an authenticated
// request. It is an alias for [auth.Claims] so library code and project
// code name the same type — a project that wants extra identity fields
// hydrates them in its enricher hook rather than forking the type.
type Claims = auth.Claims

// claimsContextKey is the private context key the claims stash uses.
// Unexported on purpose: the only ways to write or read a principal are
// [ContextWithClaims] and [ClaimsFromContext], so there is exactly one
// key in play and a caller cannot install claims the reader will miss.
type claimsContextKey struct{}

// ContextWithClaims returns a new context carrying claims. This is the
// stash [NewInterceptor] uses in Validate mode when Policy.ContextWithClaims
// is nil.
func ContextWithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext retrieves the claims installed by [ContextWithClaims].
// It returns nil, false when the request carried no principal — an
// unauthenticated caller on an allow-listed procedure, or any caller when
// Policy.AnonymousOK let a credential-less request through.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(claimsContextKey{}).(*Claims)
	return claims, ok
}

// GetUser resolves the signed-in caller, or returns CodeUnauthenticated
// when the context carries no principal.
//
// This is the seam between authentication and access control. The
// interceptor decides whether a request may proceed AT ALL; deciding what
// this caller may do, and which rows they may see, is handler logic — call
// GetUser to resolve the principal and then apply your own checks and
// query filters. forge generates none of that, because no annotation can
// express it.
func GetUser(ctx context.Context) (*Claims, error) {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("no authenticated user"))
	}
	return claims, nil
}
