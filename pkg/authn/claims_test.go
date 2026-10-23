package authn

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
)

// The claims stash moved here from the scaffolded pkg/middleware, where it
// was copied verbatim into every project. These tests pin the behaviour
// handlers depend on: a round-trip through the context, and GetUser's
// refusal when no principal is present.

func TestClaimsContextRoundTrip(t *testing.T) {
	t.Parallel()
	if got, ok := ClaimsFromContext(context.Background()); ok || got != nil {
		t.Fatalf("a background context must carry no claims, got %+v", got)
	}
	ctx := ContextWithClaims(context.Background(), &Claims{UserID: "u-1"})
	got, ok := ClaimsFromContext(ctx)
	if !ok || got.UserID != "u-1" {
		t.Fatalf("claims round-trip failed: %+v (ok=%v)", got, ok)
	}
}

func TestGetUser(t *testing.T) {
	t.Parallel()
	ctx := ContextWithClaims(context.Background(), &Claims{UserID: "u-1"})
	user, err := GetUser(ctx)
	if err != nil {
		t.Fatalf("GetUser must resolve stashed claims: %v", err)
	}
	if user.UserID != "u-1" {
		t.Fatalf("GetUser returned %q, want %q", user.UserID, "u-1")
	}
}

// GetUser is what every handler calls to assert "must be signed in". The
// CODE it returns is load-bearing: a caller with no principal must get
// CodeUnauthenticated, not a bare error that a handler might surface as a
// 500 (or, worse, ignore).
func TestGetUser_NoClaimsIsUnauthenticated(t *testing.T) {
	t.Parallel()
	user, err := GetUser(context.Background())
	if err == nil {
		t.Fatal("GetUser must error when the context carries no principal")
	}
	if user != nil {
		t.Fatalf("GetUser must return nil claims alongside the error, got %+v", user)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("GetUser must return a *connect.Error, got %T", err)
	}
	if ce.Code() != connect.CodeUnauthenticated {
		t.Fatalf("GetUser returned code %v, want %v", ce.Code(), connect.CodeUnauthenticated)
	}
}
