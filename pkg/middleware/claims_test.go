package middleware_test

import (
	"context"
	"testing"

	forgeauth "github.com/reliant-labs/forge/pkg/auth"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/pkg/middleware"
)

// The generated handler test harness authenticates through
// ContextWithClaims, so its principal must be the caller reliant's handlers
// read, not just a value on the context.
func TestContextWithClaimsInstallsTheCaller(t *testing.T) {
	ctx := middleware.ContextWithClaims(context.Background(), &forgeauth.Claims{
		UserID: "user-1",
		Email:  "user-1@example.com",
		Role:   "admin",
	})

	if got, ok := auth.GetUserIDFromContext(ctx); !ok || got != "user-1" {
		t.Fatalf("user id = %q, %v; want user-1", got, ok)
	}
	if got, ok := auth.GetUserEmailFromContext(ctx); !ok || got != "user-1@example.com" {
		t.Fatalf("email = %q, %v; want user-1@example.com", got, ok)
	}
	if got, _ := ctx.Value(auth.UserRoleContextKey).(string); got != "admin" {
		t.Fatalf("role = %q, want admin", got)
	}
}

func TestContextWithClaimsNilIsAnonymous(t *testing.T) {
	ctx := middleware.ContextWithClaims(context.Background(), nil)
	if _, ok := auth.GetUserIDFromContext(ctx); ok {
		t.Fatal("nil claims installed a caller")
	}
}
