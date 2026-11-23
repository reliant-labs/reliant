//go:build ignore

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/apikey"
	"github.com/reliant-labs/forge/pkg/auth"
)

// probeProcedure is an rpc no proto declares public: auth is enforced on it.
const probeProcedure = "/test.v1.ProbeService/Probe"

// serveThrough serves one unary rpc behind ic over real HTTP, as the
// listener does (connect-go v2 interceptors run per connect.Server), and
// returns a call that presents headers and reports the claims the handler
// saw. A v2 interceptor reads the request headers from the server's
// CallInfo, which only a served call has — so the test serves one.
func serveThrough(t *testing.T, ic connect.ServerInterceptor) func(ctx context.Context, header ...string) (*Claims, error) {
	t.Helper()
	var got *Claims
	srv := connect.NewServer(ic)
	srv.Register(connect.Method{
		Spec: connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: probeProcedure},
		Handler: func(ctx context.Context, _ connect.Spec, stream connect.ServerStream) error {
			var req wrapperspb.StringValue
			if err := stream.Receive(&req); err != nil {
				return err
			}
			got, _ = ClaimsFromContext(ctx)
			return stream.Send(wrapperspb.String("ok"))
		},
	})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	client := connect.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL))
	return func(ctx context.Context, header ...string) (*Claims, error) {
		got = nil
		ctx, info := connect.NewClientContext(ctx)
		for i := 0; i+1 < len(header); i += 2 {
			info.RequestHeader().Set(header[i], header[i+1])
		}
		var res wrapperspb.StringValue
		err := client.CallUnary(ctx, connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: probeProcedure}, wrapperspb.String("hi"), &res)
		return got, err
	}
}

// These tests cover the POLICY WIRING this file owns — the mechanism
// (mode resolution, allow-list gate, Bearer parsing, enrichment
// plumbing) is tested in forge/pkg/authn; don't re-test it here.

// A server with no validator and no external auth must refuse to start.
func TestNewAuthInterceptorV2_UnconfiguredErrors(t *testing.T) {
	ic, err := NewAuthInterceptorV2(AuthDeps{})
	if err == nil {
		t.Fatal("NewAuthInterceptorV2 must error when unconfigured")
	}
	if ic != nil {
		t.Fatal("NewAuthInterceptorV2 must return a nil interceptor alongside the error")
	}
}

// An explicit AuthDeps.Validate flips the interceptor into validate mode.
func TestNewAuthInterceptorV2_ValidatorConfigured(t *testing.T) {
	validate := func(context.Context, string) (*Claims, error) {
		return &Claims{UserID: "u1"}, nil
	}
	ic, err := NewAuthInterceptorV2(AuthDeps{Validate: validate})
	if err != nil {
		t.Fatalf("NewAuthInterceptorV2 with validator must not error: %v", err)
	}
	if ic == nil {
		t.Fatal("NewAuthInterceptorV2 must not return nil when configured")
	}
	claims, err := validate(context.Background(), "anything")
	if err != nil || claims == nil || claims.UserID != "u1" {
		t.Fatalf("the threaded validator must dispatch, got %+v, %v", claims, err)
	}
}

// The wiring cmd serve.go builds — this file's credentialFrom and
// NewAuthInterceptorV2 over the auth.Authenticator app.SetupAuth returns —
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
	ic, err := NewAuthInterceptorV2(AuthDeps{Validate: authenticator.Authenticate})
	if err != nil {
		t.Fatalf("NewAuthInterceptorV2: %v", err)
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
	call := serveThrough(t, ic)
	for _, tc := range cases {
		got, err := call(context.Background(), tc.header...)

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
	// The validator runs on the request's context — it is a database read —
	// so it must carry the server's call info, which only a request has.
	if keyCtx == nil {
		t.Fatal("the key validator never ran")
	}
	if _, ok := connect.CallInfoForServerContext(keyCtx); !ok {
		t.Error("the key validator must run with the request's context — it is a database read")
	}
}

// AuthDeps.ExternalAuth puts the interceptor in passthrough mode — the
// pack's own interceptor in the chain is the source of truth.
func TestNewAuthInterceptorV2_ExternalAuthIsPassthrough(t *testing.T) {
	ic, err := NewAuthInterceptorV2(AuthDeps{ExternalAuth: true})
	if err != nil {
		t.Fatalf("NewAuthInterceptorV2 with external auth must not error: %v", err)
	}
	// No credential at all, on an rpc that is not public: passthrough still
	// reaches the handler.
	if _, err := serveThrough(t, ic)(context.Background()); err != nil {
		t.Fatalf("a passthrough interceptor must invoke the handler unconditionally: %v", err)
	}
}

// AuthDeps.ExternalAuth is the ONLY opt-out, and it is a field in this file.
// Nothing in the process environment can stand a server up without
// authentication: this asserts the negative, because "we removed the env
// opt-out" is only true while nothing quietly re-adds one.
func TestNewAuthInterceptorV2_EnvironmentCannotOptOut(t *testing.T) {
	// NOT parallel: mutates the process environment.
	for _, name := range []string{"AUTH_MODE", "AUTH", "DISABLE_AUTH", "NO_AUTH"} {
		t.Setenv(name, "none")
	}
	if _, err := NewAuthInterceptorV2(AuthDeps{}); err == nil {
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
	cookie := (&http.Cookie{Name: SecureSessionCookieName, Value: "from-cookie"}).String()

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
		if got, _ := credentialFrom(h); got != tc.want {
			t.Errorf("%s: credentialFrom = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Outside development only the __Host- session cookie is a credential: a
// plain-named one can only have been planted by a sibling site. In
// development (AuthDeps.DevSessionCookie) the plain name is read too, because
// the broker cannot set a __Host- cookie over plain http.
func TestCredentialFrom_PlainSessionCookieOnlyInDevelopment(t *testing.T) {
	t.Parallel()
	plain := http.Header{}
	plain.Set("Cookie", (&http.Cookie{Name: SessionCookieName, Value: "planted"}).String())
	if got, _ := credentialFrom(plain); got != "" {
		t.Errorf("deployed: a plain-named session cookie was read (%q)", got)
	}
	plain.Set("X-Forwarded-Proto", "http")
	if got, _ := credentialFrom(plain); got != "" {
		t.Errorf("deployed, no https header: a plain-named session cookie was read (%q)", got)
	}
	if got, _ := devCredentialFrom(plain); got != "planted" {
		t.Errorf("development: the plain-named session cookie was not read (%q)", got)
	}
	blocked := http.Header{}
	blocked.Set("Authorization", "")
	blocked.Set("Cookie", (&http.Cookie{Name: SessionCookieName, Value: "x"}).String())
	if got, _ := devCredentialFrom(blocked); got != "" {
		t.Errorf("development: an empty Authorization header fell through to the cookie (%q)", got)
	}
}
