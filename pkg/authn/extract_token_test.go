package authn

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
)

// The credential channel is a POLICY decision, not a fixed mechanism.
//
// The bug this pins: a project whose browser session is an HttpOnly cookie
// (the token is unreadable to scripts, so no client code CAN attach an
// Authorization header) had no way to tell the interceptor where its
// credential lives. Every RPC 401'd with "missing Authorization header"
// while the very same token authenticated fine as a Bearer — the token was
// valid, it just arrived in a header this package did not read.
//
// ExtractToken is that seam. It receives the whole request header, so a
// project can read a cookie, a bespoke header, or anything else, and
// returns the RAW token — the library still owns validation, the claims
// stash, the allow-list gate and the error envelope.
func TestExtractToken_ReadsCredentialFromCookie(t *testing.T) {
	t.Parallel()

	const token = "cookie-carried-token"
	claims := &auth.Claims{UserID: "operator-1"}

	var validated string
	p := validatePolicy(func(tok string) (*auth.Claims, error) {
		validated = tok
		return claims, nil
	})
	// The project owns the channel: read the session cookie, fall back to
	// nothing. Returning "" means "no credential presented", which the
	// library treats exactly as a missing Authorization header.
	p.ExtractToken = func(h http.Header) string {
		req := http.Request{Header: h}
		c, err := req.Cookie("session")
		if err != nil {
			return ""
		}
		return c.Value
	}
	a := &interceptor{policy: p}

	var got context.Context
	wrapped := a.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		got = ctx
		return nil, nil
	})

	req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}
	req.header.Set("Cookie", "session="+token)

	if _, err := wrapped(context.Background(), req); err != nil {
		t.Fatalf("a cookie-carried credential must authenticate, got %v", err)
	}
	if validated != token {
		t.Fatalf("validator got %q, want the raw cookie value %q", validated, token)
	}
	if c, ok := testClaimsFromContext(got); !ok || c.UserID != claims.UserID {
		t.Fatalf("claims must reach the handler, got %+v", c)
	}
}

// The Authorization header stays the DEFAULT. A project that sets no
// ExtractToken must behave exactly as before — this is what makes the seam
// additive rather than a behavior change every existing service inherits.
func TestExtractToken_DefaultsToAuthorizationHeader(t *testing.T) {
	t.Parallel()

	var validated string
	a := &interceptor{policy: validatePolicy(func(tok string) (*auth.Claims, error) {
		validated = tok
		return &auth.Claims{UserID: "u1"}, nil
	})}

	wrapped := a.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})

	req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}
	req.header.Set("Authorization", "Bearer header-token")

	if _, err := wrapped(context.Background(), req); err != nil {
		t.Fatalf("default Authorization-header extraction must still work, got %v", err)
	}
	if validated != "header-token" {
		t.Fatalf("validator got %q, want %q", validated, "header-token")
	}
}

// A custom extractor returns the RAW token, so it must NOT be subjected to
// the "Bearer " prefix rule. A cookie value is a bare token; requiring the
// scheme would mean every project stuffing a fake "Bearer " in front of its
// own cookie to satisfy a parser that should not be looking.
func TestExtractToken_RawTokenNeedsNoBearerPrefix(t *testing.T) {
	t.Parallel()

	p := validatePolicy(func(tok string) (*auth.Claims, error) {
		if tok != "bare-token" {
			t.Errorf("validator got %q, want the raw token with no scheme stripping", tok)
		}
		return &auth.Claims{UserID: "u1"}, nil
	})
	p.ExtractToken = func(http.Header) string { return "bare-token" }
	a := &interceptor{policy: p}

	wrapped := a.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})
	req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}

	if _, err := wrapped(context.Background(), req); err != nil {
		t.Fatalf("a bare extracted token must authenticate, got %v", err)
	}
}

// An extractor that finds nothing is a MISSING credential, not a malformed
// one — same 401 as an absent Authorization header, and still subject to
// AnonymousOK. Fail-closed is preserved: a broken extractor cannot turn
// into an anonymous pass under the strict posture.
func TestExtractToken_EmptyIsMissingCredential(t *testing.T) {
	t.Parallel()

	p := validatePolicy(func(string) (*auth.Claims, error) {
		t.Error("validator must not run when no credential was extracted")
		return nil, nil
	})
	p.ExtractToken = func(http.Header) string { return "" }
	a := &interceptor{policy: p}

	wrapped := a.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		t.Error("handler must not run without a credential")
		return nil, nil
	})
	req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}

	_, err := wrapped(context.Background(), req)
	var cerr *connect.Error
	if err == nil {
		t.Fatal("no extracted credential must be rejected")
	}
	if !asConnectError(err, &cerr) || cerr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("want CodeUnauthenticated, got %v", err)
	}
}

// Decorate receives what the project can actually use for outbound
// propagation. With a custom extractor there may be no Authorization header
// at all, so the raw token is what gets handed through — a Decorate that
// forwards identity must not be handed an empty string just because the
// credential arrived by another channel.
func TestExtractToken_DecorateReceivesExtractedCredential(t *testing.T) {
	t.Parallel()

	p := validatePolicy(func(string) (*auth.Claims, error) { return &auth.Claims{UserID: "u1"}, nil })
	p.ExtractToken = func(http.Header) string { return "cookie-token" }

	var seen string
	p.Decorate = func(ctx context.Context, _ *auth.Claims, authorization string) context.Context {
		seen = authorization
		return ctx
	}
	a := &interceptor{policy: p}

	wrapped := a.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})
	req := &fakeReq{procedure: "/demo.v1.Service/Method", header: http.Header{}}

	if _, err := wrapped(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen != "Bearer cookie-token" {
		t.Fatalf("Decorate got authorization %q, want a usable Bearer credential", seen)
	}
}

func asConnectError(err error, target **connect.Error) bool {
	ce, ok := err.(*connect.Error)
	if ok {
		*target = ce
	}
	return ok
}
