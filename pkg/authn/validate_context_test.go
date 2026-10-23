package authn

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
)

// A validator that does I/O — an API-key lookup in the application's
// database, a token-introspection call — needs the REQUEST's context: to be
// cancelled with the request, to carry its deadline, and to open its spans
// under the request's trace. Validate(token) has nowhere to put one, so a
// DB-backed key validator could only run on context.Background(), detached
// from everything the request carries. ValidateContext is that seam.
func TestValidateContext_ReceivesTheRequestContext(t *testing.T) {
	t.Parallel()

	type probeKey struct{}
	var sawRequestCtx bool
	ic, err := NewInterceptor(Policy{
		ValidatorConfigured: true,
		ValidateContext: func(ctx context.Context, token string) (*auth.Claims, error) {
			sawRequestCtx = ctx.Value(probeKey{}) == "request-scoped"
			if token != "the-token" {
				t.Errorf("validator got %q, want the raw token", token)
			}
			return &auth.Claims{UserID: "u1"}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewInterceptor with ValidateContext: %v", err)
	}

	var handlerSaw *auth.Claims
	wrapped := ic.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		handlerSaw, _ = ClaimsFromContext(ctx)
		return nil, nil
	})
	req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}
	req.header.Set("Authorization", "Bearer the-token")

	ctx := context.WithValue(context.Background(), probeKey{}, "request-scoped")
	if _, err := wrapped(ctx, req); err != nil {
		t.Fatalf("a valid credential must authenticate: %v", err)
	}
	if !sawRequestCtx {
		t.Fatal("ValidateContext must be called with the request's context, not a detached one")
	}
	if handlerSaw == nil || handlerSaw.UserID != "u1" {
		t.Fatalf("claims from ValidateContext must reach the handler, got %+v", handlerSaw)
	}
}

// One policy, one validator. Setting both is a configuration error that
// would otherwise be settled by a precedence rule nobody reads — refuse it
// at construction, where startup aborts before the listener binds.
func TestNewInterceptor_ValidateAndValidateContextAreExclusive(t *testing.T) {
	t.Parallel()
	_, err := NewInterceptor(Policy{
		ValidatorConfigured: true,
		Validate:            func(string) (*auth.Claims, error) { return &auth.Claims{}, nil },
		ValidateContext: func(context.Context, string) (*auth.Claims, error) {
			return &auth.Claims{}, nil
		},
	})
	if err == nil {
		t.Fatal("a policy setting both Validate and ValidateContext must refuse construction")
	}
	if _, err := NewInterceptor(Policy{
		ValidatorConfigured: true,
		ValidateContext: func(context.Context, string) (*auth.Claims, error) {
			return &auth.Claims{}, nil
		},
	}); err != nil {
		t.Fatalf("ValidateContext alone is a complete validator: %v", err)
	}
}

// A validator that reports SUCCESS with no principal is a bug — the classic
// shape is a hand-written key lookup doing `if errors.Is(err, sql.ErrNoRows)
// { return nil, nil }`. Before this guard the interceptor stashed nil
// claims and called next: an auth_required RPC whose handler never calls
// GetUser (every scaffolded CRUD shim) then served an unauthenticated
// caller. No principal means no authentication, whichever validator said so.
func TestAuthenticate_ValidatorReturningNoClaimsFailsClosed(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]Policy{
		"Validate":        validatePolicy(func(string) (*auth.Claims, error) { return nil, nil }),
		"ValidateContext": {ValidatorConfigured: true, ValidateContext: func(context.Context, string) (*auth.Claims, error) { return nil, nil }, ContextWithClaims: testContextWithClaims},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ic, err := NewInterceptor(p)
			if err != nil {
				t.Fatalf("NewInterceptor: %v", err)
			}
			wrapped := ic.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				t.Error("the handler must not run for a credential no principal was proven for")
				return nil, nil
			})
			req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}
			req.header.Set("Authorization", "Bearer looks-fine")
			_, err = wrapped(context.Background(), req)
			var cerr *connect.Error
			if !errors.As(err, &cerr) || cerr.Code() != connect.CodeUnauthenticated {
				t.Fatalf("want Unauthenticated, got %v", err)
			}
		})
	}
}
