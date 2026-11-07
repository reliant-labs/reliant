package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// supabaseResolver is the example provider-specific resolver used to confirm
// the UserResolver hook receives the raw payload and can project it onto
// the standard [Claims] shape.
type supabaseResolver struct {
	called bool
}

func (r *supabaseResolver) Resolve(mc map[string]any) (*Claims, error) {
	r.called = true
	c := &Claims{Raw: mc}
	if sub, ok := mc["sub"].(string); ok {
		c.UserID = sub
	}
	// Supabase nests the user-displayed email under user_metadata.email,
	// not the top-level email claim — the resolver pulls it out.
	if um, ok := mc["user_metadata"].(map[string]any); ok {
		if e, ok := um["email"].(string); ok {
			c.Email = e
		}
		if r, ok := um["role"].(string); ok {
			c.Role = r
		}
	}
	return c, nil
}

func TestUserResolver_ReceivesRawPayload(t *testing.T) {
	resolver := &supabaseResolver{}
	v, err := NewValidator(Config{
		Provider: ProviderJWT,
		JWT: JWTConfig{
			SigningMethod: "HS256",
			Secret:        "rs-secret",
		},
		UserResolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	tok := signHS256(t, "rs-secret", jwt.MapClaims{
		"sub":   "user-99",
		"email": "ignored-top-level@example.com",
		"user_metadata": map[string]any{
			"email": "real@supa.io",
			"role":  "billing-admin",
		},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("AuthenticateHeaders: %v", err)
	}
	if !resolver.called {
		t.Error("resolver was not consulted")
	}
	if c.UserID != "user-99" {
		t.Errorf("UserID=%q want user-99", c.UserID)
	}
	if c.Email != "real@supa.io" {
		t.Errorf("Email=%q want real@supa.io (from user_metadata)", c.Email)
	}
	if c.Role != "billing-admin" {
		t.Errorf("Role=%q want billing-admin", c.Role)
	}
	// Raw must carry the full payload so downstream code can still
	// inspect provider-specific fields the resolver didn't promote.
	if c.Raw == nil {
		t.Fatal("Claims.Raw is nil; resolver should preserve raw payload")
	}
	if _, ok := c.Raw["user_metadata"]; !ok {
		t.Error("Claims.Raw missing user_metadata")
	}
}

func TestUserResolver_NilFallsBackToBuiltIn(t *testing.T) {
	v, err := NewValidator(Config{
		Provider: ProviderJWT,
		JWT: JWTConfig{
			SigningMethod: "HS256",
			Secret:        "default-secret",
		},
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	tok := signHS256(t, "default-secret", jwt.MapClaims{
		"sub":   "u",
		"email": "u@example.com",
		"role":  "admin",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("AuthenticateHeaders: %v", err)
	}
	if c.UserID != "u" || c.Email != "u@example.com" || c.Role != "admin" {
		t.Errorf("built-in extraction lost fields: %+v", c)
	}
	// Backwards compat for callers that DO want provider-specific fields:
	// Raw is populated even with no resolver.
	if c.Raw == nil {
		t.Error("Claims.Raw should be populated even with no resolver")
	}
}

type erroringResolver struct{}

func (erroringResolver) Resolve(map[string]any) (*Claims, error) {
	return nil, errors.New("boom")
}

func TestUserResolver_ErrorFallsBackToBuiltIn(t *testing.T) {
	v, _ := NewValidator(Config{
		Provider: ProviderJWT,
		JWT: JWTConfig{
			SigningMethod: "HS256",
			Secret:        "z",
		},
		UserResolver: erroringResolver{},
	})
	tok := signHS256(t, "z", jwt.MapClaims{
		"sub": "u",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("AuthenticateHeaders: %v", err)
	}
	if c.UserID != "u" {
		t.Errorf("expected fallback to built-in extraction; got %+v", c)
	}
}
