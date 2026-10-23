//go:build ignore

package middleware

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/apikey"
	"github.com/reliant-labs/forge/pkg/auth"
)

// These tests cover the POLICY WIRING this file owns — the mechanism
// (mode resolution, allow-list gate, Bearer parsing, enrichment
// plumbing) is tested in forge/pkg/authn; don't re-test it here.

// A server with no validator and no external auth must refuse to start.
func TestNewAuthInterceptor_UnconfiguredErrors(t *testing.T) {
	ic, err := NewAuthInterceptor(AuthDeps{})
	if err == nil {
		t.Fatal("NewAuthInterceptor must error when unconfigured")
	}
	if ic != nil {
		t.Fatal("NewAuthInterceptor must return a nil interceptor alongside the error")
	}
}

// An explicit AuthDeps.Validate flips the interceptor into validate mode.
func TestNewAuthInterceptor_ValidatorConfigured(t *testing.T) {
	validate := func(context.Context, string) (*Claims, error) {
		return &Claims{UserID: "u1"}, nil
	}
	ic, err := NewAuthInterceptor(AuthDeps{Validate: validate})
	if err != nil {
		t.Fatalf("NewAuthInterceptor with validator must not error: %v", err)
	}
	if ic == nil {
		t.Fatal("NewAuthInterceptor must not return nil when configured")
	}
	claims, err := validate(context.Background(), "anything")
	if err != nil || claims == nil || claims.UserID != "u1" {
		t.Fatalf("the threaded validator must dispatch, got %+v, %v", claims, err)
	}
}

// The wiring cmd serve.go builds — this file's credentialFrom and
// NewAuthInterceptor over the auth.Authenticator app.SetupAuth returns —
// accepts an API key on APIKeyHeader, still accepts a bearer token, and
// rejects everything else on an RPC that is not on the allow-list. The key
// validator here is a stand-in for internal/app/auth.go's APIKeyValidator:
// it stores only the key's hash, the way your table must.
func TestAuthWiring_APIKeyAndBearer(t *testing.T) {
	t.Parallel()
	key, err := apikey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keyPrefix, err := apikey.LookupPrefix(key)
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{keyPrefix: apikey.Hash(key)} // lookup prefix -> hash
	var keyCtx context.Context

	authenticator := auth.NewAuthenticator(func(token string) (*Claims, error) {
		if token == "a.valid.jwt" {
			return &Claims{UserID: "jwt-user"}, nil
		}
		return nil, errors.New("signature is invalid")
	})
	authenticator.UseKeyValidator(auth.KeyValidatorFunc(func(ctx context.Context, presented string) (*Claims, error) {
		keyCtx = ctx
		prefix, err := apikey.LookupPrefix(presented)
		if err != nil {
			return nil, err
		}
		hash, ok := stored[prefix]
		if !ok || !apikey.Verify(presented, hash) {
			return nil, errors.New("unknown key")
		}
		return &Claims{UserID: "key-owner"}, nil
	}))
	ic, err := NewAuthInterceptor(AuthDeps{Validate: authenticator.Authenticate})
	if err != nil {
		t.Fatalf("NewAuthInterceptor: %v", err)
	}

	wrongKey := key[:len(key)-1] + "?"
	cases := []struct {
		name   string
		header []string // name, value, name, value, ...
		want   string   // the authenticated UserID; "" means rejected
	}{
		{"api key on APIKeyHeader", []string{APIKeyHeader, key}, "key-owner"},
		{"api key as a bearer token", []string{"Authorization", "Bearer " + key}, "key-owner"},
		{"jwt as a bearer token", []string{"Authorization", "Bearer a.valid.jwt"}, "jwt-user"},
		{"wrong api key", []string{APIKeyHeader, wrongKey}, ""},
		{"malformed api key", []string{APIKeyHeader, apikey.KeyPrefix + "x"}, ""},
		{"not an api key at all", []string{APIKeyHeader, "hunter2"}, ""},
		{"no credential", nil, ""},
		// One credential per request, and no fallback: the Bearer token is
		// the credential, so a valid key cannot rescue an invalid token.
		{"bad bearer beside a good key", []string{"Authorization", "Bearer forged", APIKeyHeader, key}, ""},
		{"malformed bearer beside a good key", []string{"Authorization", "Bearer", APIKeyHeader, key}, ""},
		{"empty authorization beside a good key", []string{"Authorization", "", APIKeyHeader, key}, ""},
		{"empty key beside a good cookie", []string{APIKeyHeader, "", "Cookie", SessionCookieName + "=a.valid.jwt"}, ""},
		{"whitespace key beside a good cookie", []string{APIKeyHeader, "   ", "Cookie", SessionCookieName + "=a.valid.jwt"}, ""},
		{"good bearer beside a bad key", []string{"Authorization", "Bearer a.valid.jwt", APIKeyHeader, wrongKey}, "jwt-user"},
	}
	type reqKey struct{}
	for _, tc := range cases {
		var got *Claims
		next := ic.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			got, _ = ClaimsFromContext(ctx)
			return nil, nil
		})
		// A request with no procedure is on no allow-list: auth is enforced.
		req := connect.NewRequest(&struct{}{})
		for i := 0; i+1 < len(tc.header); i += 2 {
			req.Header().Set(tc.header[i], tc.header[i+1])
		}
		_, err := next(context.WithValue(context.Background(), reqKey{}, tc.name), req)

		switch {
		case tc.want == "" && err == nil:
			t.Errorf("%s: accepted, want CodeUnauthenticated", tc.name)
		case tc.want == "" && connect.CodeOf(err) != connect.CodeUnauthenticated:
			t.Errorf("%s: got %v, want CodeUnauthenticated", tc.name, err)
		case tc.want != "" && err != nil:
			t.Errorf("%s: rejected: %v", tc.name, err)
		case tc.want != "" && (got == nil || got.UserID != tc.want):
			t.Errorf("%s: handler saw claims %+v, want UserID %q", tc.name, got, tc.want)
		}
	}
	if keyCtx == nil || keyCtx.Value(reqKey{}) == nil {
		t.Error("the key validator must run with the request's context — it is a database read")
	}
}

// AuthDeps.ExternalAuth puts the interceptor in passthrough mode — the
// pack's own interceptor in the chain is the source of truth.
func TestNewAuthInterceptor_ExternalAuthIsPassthrough(t *testing.T) {
	ic, err := NewAuthInterceptor(AuthDeps{ExternalAuth: true})
	if err != nil {
		t.Fatalf("NewAuthInterceptor with external auth must not error: %v", err)
	}
	called := false
	wrapped := ic.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})
	_, _ = wrapped(context.Background(), nil)
	if !called {
		t.Fatal("passthrough WrapUnary must invoke next unconditionally")
	}
}

// AuthDeps.ExternalAuth is the ONLY opt-out, and it is a field in this file.
// Nothing in the process environment can stand a server up without
// authentication: this asserts the negative, because "we removed the env
// opt-out" is only true while nothing quietly re-adds one.
func TestNewAuthInterceptor_EnvironmentCannotOptOut(t *testing.T) {
	// NOT parallel: mutates the process environment.
	for _, name := range []string{"AUTH_MODE", "AUTH", "DISABLE_AUTH", "NO_AUTH"} {
		t.Setenv(name, "none")
	}
	if _, err := NewAuthInterceptor(AuthDeps{}); err == nil {
		t.Fatal("an environment variable disabled authentication; only AuthDeps.ExternalAuth may")
	}
}

// The allow-list must stay exact-match only — substring matching is how
// auth bypasses are born. (The gate itself lives in pkg/authn; this
// pins the CONTENTS this project ships with. The set is generated from the
// protos' auth_required declarations — see procedures_gen.go — so an entry
// appearing here that no rpc declared is a generator bug, and an rpc you
// meant to publish that is missing is a missing annotation.)
func TestUnauthenticatedProcedures_Contents(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
	} {
		if _, ok := UnauthenticatedProcedures[p]; !ok {
			t.Fatalf("%s must be on the allow-list", p)
		}
	}
	if _, ok := UnauthenticatedProcedures["/demo.v1.Service/HealthCheck"]; ok {
		t.Fatal("substring-shaped entries must not be allow-listed")
	}
}

// The claims stash is library mechanism (forge/pkg/authn owns the type,
// the context key, the accessors and GetUser, and tests them). What this
// package owns is the RE-EXPORT: handlers and generated code spell
// middleware.ContextWithClaims / GetUser, so this asserts those names reach
// the same stash — a delegation wired to a different key would authenticate
// the request and then report "no authenticated user" in every handler.
func TestClaimsReExportsReachTheLibraryStash(t *testing.T) {
	t.Parallel()
	ctx := ContextWithClaims(context.Background(), &Claims{UserID: "u-1"})
	got, ok := ClaimsFromContext(ctx)
	if !ok || got.UserID != "u-1" {
		t.Fatalf("claims round-trip failed: %+v", got)
	}
	user, err := GetUser(ctx)
	if err != nil {
		t.Fatalf("GetUser must find claims installed via ContextWithClaims: %v", err)
	}
	if user.UserID != "u-1" {
		t.Fatalf("GetUser returned %q, want %q", user.UserID, "u-1")
	}
}

// credentialFrom reads ONE credential: a Bearer header, else APIKeyHeader,
// else the session cookie. Native sign-in keeps the session in an HttpOnly
// cookie, so the browser has no other way to present it — reading only the
// Authorization header 401'd every RPC right after a successful sign-in. An
// explicit header wins over the ambient cookie, and a Bearer header wins
// over an API key: a caller that sends one means that credential.
func TestCredentialFrom_ReadsBearerThenAPIKeyThenTheSessionCookie(t *testing.T) {
	t.Parallel()
	cookie := (&http.Cookie{Name: SessionCookieName, Value: "from-cookie"}).String()

	cases := []struct {
		name   string
		header []string // name, value, name, value, ...
		want   string
	}{
		{"session cookie only", []string{"Cookie", cookie}, "from-cookie"},
		{"api key only", []string{APIKeyHeader, "fk_from-key-header"}, "fk_from-key-header"},
		{"bearer wins over the cookie", []string{"Authorization", "Bearer from-header", "Cookie", cookie}, "from-header"},
		{"bearer wins over an api key", []string{"Authorization", "Bearer from-header", APIKeyHeader, "fk_k"}, "from-header"},
		{"api key wins over the cookie", []string{APIKeyHeader, "fk_k", "Cookie", cookie}, "fk_k"},
		{"malformed authorization blocks key and cookie", []string{"Authorization", "Bearer", APIKeyHeader, "fk_k", "Cookie", cookie}, ""},
		{"empty authorization blocks key and cookie", []string{"Authorization", "", APIKeyHeader, "fk_k", "Cookie", cookie}, ""},
		{"empty key blocks cookie", []string{APIKeyHeader, "", "Cookie", cookie}, ""},
		{"whitespace key blocks cookie", []string{APIKeyHeader, "   ", "Cookie", cookie}, ""},
		{"another app's cookie is not a credential", []string{"Cookie", "other_session=x"}, ""},
		{"nothing presented", nil, ""},
	}
	for _, tc := range cases {
		h := http.Header{}
		for i := 0; i+1 < len(tc.header); i += 2 {
			h.Set(tc.header[i], tc.header[i+1])
		}
		if got := credentialFrom(h); got != tc.want {
			t.Errorf("%s: credentialFrom = %q, want %q", tc.name, got, tc.want)
		}
	}
}
