package middleware

import (
	"context"

	forgeauth "github.com/reliant-labs/forge/pkg/auth"

	"github.com/reliant-labs/reliant/internal/auth"
)

// ContextWithClaims installs claims on ctx the way reliant's API auth
// interceptor installs a verified caller (auth.WithUser), so a handler
// reading auth.GetUserIDFromContext sees them.
//
// forge's generated handler test harness
// (internal/handlers/<svc>/helpers_gen_test.go) authenticates through this
// exact symbol: AuthedContext puts the default test principal on a context,
// and NewTest<Svc>Server installs it server-side on every call. Until the
// serving path moves onto forge's auth middleware (P3c in
// research/forge-migration/PLAN.md), this is the bridge that makes those
// principals mean the same thing to reliant's handlers as a real request.
func ContextWithClaims(ctx context.Context, claims *forgeauth.Claims) context.Context {
	if claims == nil {
		return ctx
	}
	return auth.WithUser(ctx, claims.UserID, claims.Role, claims.Email)
}
