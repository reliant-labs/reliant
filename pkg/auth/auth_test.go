package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
)

func TestNewValidator_Providers(t *testing.T) {
	for _, p := range []string{ProviderNone, ProviderJWT, ProviderAPIKey, ProviderBoth, ""} {
		if _, err := NewValidator(Config{Provider: p}); err != nil {
			t.Errorf("provider %q: NewValidator unexpected error: %v", p, err)
		}
	}
	if _, err := NewValidator(Config{Provider: "magic"}); err == nil {
		t.Errorf("expected error for unknown provider")
	}
}

func TestIsUnauthenticatedProcedure_Health(t *testing.T) {
	v, _ := NewValidator(Config{Provider: ProviderJWT})
	for _, p := range []string{
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
	} {
		if !v.IsUnauthenticatedProcedure(p, nil) {
			t.Errorf("%q should be unauthenticated", p)
		}
	}
	// Application RPCs that merely contain "Health" MUST still require auth:
	// the skip is an exact gRPC health-check service-path match, not a
	// substring, so a "GetPatientHealthRecord" endpoint is never exempted.
	for _, p := range []string{
		"/svc/Foo",
		"/myapp/HealthCheck",
		"/myapp.v1.Records/GetPatientHealthRecord",
		"/myapp.v1.Notes/MentalHealthNote",
		"/myapp.v1.Metrics/HealthMetrics",
	} {
		if v.IsUnauthenticatedProcedure(p, nil) {
			t.Errorf("%q must require auth (not a health-check procedure)", p)
		}
	}
}

func TestIsUnauthenticatedProcedure_SkipList(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider:    ProviderJWT,
		SkipMethods: []string{"/svc/Public"},
	})
	if !v.IsUnauthenticatedProcedure("/svc/Public", nil) {
		t.Error("config skip method not honoured")
	}
	if v.IsUnauthenticatedProcedure("/svc/Private", nil) {
		t.Error("non-skip method should require auth")
	}
	// Override via opts
	if v.IsUnauthenticatedProcedure("/svc/Public", []string{"/svc/Other"}) {
		t.Error("opts override should disable cfg skip")
	}
	if !v.IsUnauthenticatedProcedure("/svc/Other", []string{"/svc/Other"}) {
		t.Error("opts override skip not honoured")
	}
}

func TestAuthenticateHeaders_None(t *testing.T) {
	v, _ := NewValidator(Config{Provider: ProviderNone})
	c, err := v.AuthenticateHeaders(context.Background(), http.Header{}, InterceptorOptions{})
	if err != nil || c != nil {
		t.Errorf("none provider: claims=%v err=%v", c, err)
	}
}

func TestAuthenticateHeaders_JWT_MissingAuthHeader(t *testing.T) {
	v, _ := NewValidator(Config{Provider: ProviderJWT})
	_, err := v.AuthenticateHeaders(context.Background(), http.Header{}, InterceptorOptions{})
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestAuthenticateHeaders_JWT_InvalidFormat(t *testing.T) {
	v, _ := NewValidator(Config{Provider: ProviderJWT})
	h := http.Header{}
	h.Set("Authorization", "Token abc")
	_, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

// signHS256 mints a JWT signed with the provided secret.
func signHS256(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestAuthenticateHeaders_JWT_HS256_Valid(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT: JWTConfig{
			SigningMethod: "HS256",
			Secret:        "topsecret",
		},
	})
	tokStr := signHS256(t, "topsecret", jwt.MapClaims{
		"sub":    "user-1",
		"email":  "u@example.com",
		"org_id": "org-7",
		"role":   "admin",
		"roles":  []string{"a", "b"},
		"exp":    time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tokStr)
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c == nil || c.UserID != "user-1" || c.OrgID != "org-7" || c.Role != "admin" || c.Email != "u@example.com" {
		t.Errorf("claims mismatch: %+v", c)
	}
	if len(c.Roles) != 2 || c.Roles[0] != "a" {
		t.Errorf("roles slice mismatch: %+v", c.Roles)
	}
}

// A validator trusts EXACTLY the key its constructor was handed. An ambient
// JWT_SECRET in the process environment is not signing material — this
// package reads no environment variable at all.
//
// It is what makes "no signing material configured means no credential is
// accepted" provable: a JWT_SECRET left over in a developer's shell, in a CI
// job, or in a Deployment that outlived its IdP migration must not silently
// re-authenticate anyone. The key reaches this package one way — the typed
// config field the app declares, handed to the constructor.
func TestAuthenticateHeaders_JWT_IgnoresAmbientEnvSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "envsecret")
	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "HS256"},
	})
	tokStr := signHS256(t, "envsecret", jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tokStr)
	if _, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{}); err == nil {
		t.Fatal("a token signed with an ambient JWT_SECRET was accepted; the library must trust only the key it was given")
	}
}

func TestAuthenticateHeaders_JWT_NoSecret_ProducesError(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "HS256"},
	})
	tokStr := signHS256(t, "anything", jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tokStr)
	_, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err == nil {
		t.Fatal("expected error when no secret available")
	}
}

func TestAuthenticateHeaders_JWT_WrongSigningMethod(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT: JWTConfig{
			SigningMethod: "HS384",
			Secret:        "x",
		},
	})
	tokStr := signHS256(t, "x", jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tokStr)
	_, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err == nil {
		t.Fatal("expected signing-method mismatch error")
	}
}

type stubKV struct {
	want string
	out  *Claims
	err  error
}

func (s *stubKV) ValidateKey(_ context.Context, key string) (*Claims, error) {
	if key != s.want {
		return nil, errors.New("bad key")
	}
	return s.out, s.err
}

func TestAuthenticateHeaders_APIKey_Valid(t *testing.T) {
	want := &Claims{UserID: "k1", OrgID: "org-9"}
	v, _ := NewValidator(Config{
		Provider:     ProviderAPIKey,
		APIKey:       APIKeyConfig{Header: "X-Secret"},
		KeyValidator: &stubKV{want: "abc", out: want},
	})
	h := http.Header{}
	h.Set("X-Secret", "abc")
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c != want {
		t.Errorf("claims mismatch: got %+v want %+v", c, want)
	}
}

func TestAuthenticateHeaders_APIKey_MissingHeader(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider:     ProviderAPIKey,
		KeyValidator: &stubKV{},
	})
	_, err := v.AuthenticateHeaders(context.Background(), http.Header{}, InterceptorOptions{})
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestAuthenticateHeaders_APIKey_NoValidator(t *testing.T) {
	v, _ := NewValidator(Config{Provider: ProviderAPIKey})
	h := http.Header{}
	h.Set("X-API-Key", "abc")
	_, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected Unauthenticated when no validator wired, got %v", err)
	}
}

func TestAuthenticateHeaders_Both_FallsBackToAPIKey(t *testing.T) {
	want := &Claims{UserID: "k1"}
	v, _ := NewValidator(Config{
		Provider:     ProviderBoth,
		JWT:          JWTConfig{SigningMethod: "HS256", Secret: "z"},
		APIKey:       APIKeyConfig{Header: "X-API-Key"},
		KeyValidator: &stubKV{want: "abc", out: want},
	})
	h := http.Header{}
	h.Set("X-API-Key", "abc")
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c != want {
		t.Errorf("expected fallback to API key claims, got %+v", c)
	}
}

func TestAuthenticateHeaders_Both_BothFail(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider:     ProviderBoth,
		JWT:          JWTConfig{SigningMethod: "HS256", Secret: "z"},
		KeyValidator: &stubKV{want: "right"},
	})
	_, err := v.AuthenticateHeaders(context.Background(), http.Header{}, InterceptorOptions{})
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestValidate_DirectCall(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "HS256", Secret: "k"},
	})
	tokStr := signHS256(t, "k", jwt.MapClaims{
		"sub": "user-x",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	c, err := v.Validate(tokStr)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.UserID != "user-x" {
		t.Errorf("UserID=%q want user-x", c.UserID)
	}
}

// TestInterceptor_Type provides a smoke check that the constructor returns a
// non-nil interceptor that wraps a unary func. Branch coverage of the
// interceptor body is provided by the AuthenticateHeaders tests above —
// constructing an arbitrary connect.AnyRequest in unit tests requires
// spinning up a real Connect server (the type is sealed via internalOnly).
func TestInterceptor_Type(t *testing.T) {
	v, _ := NewValidator(Config{Provider: ProviderJWT})
	ic := v.Interceptor(InterceptorOptions{}, nil)
	if ic == nil {
		t.Fatal("Interceptor must not return nil")
	}
	wrapped := ic.WrapUnary(func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) { return nil, nil })
	if wrapped == nil {
		t.Fatal("WrapUnary returned nil")
	}
}
