package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
)

// stubTokenValidator is a no-network TokenValidator used in tests.
type stubTokenValidator struct {
	name   string
	accept bool
	out    *Claims
	calls  int
}

func (s *stubTokenValidator) ValidateToken(_ string) (*Claims, error) {
	s.calls++
	if s.accept {
		c := s.out
		if c == nil {
			c = &Claims{UserID: "from-" + s.name}
		}
		return c, nil
	}
	return nil, fmt.Errorf("%s: rejected", s.name)
}

func TestMultiValidator_FirstAcceptWins(t *testing.T) {
	a := &stubTokenValidator{name: "a", accept: true, out: &Claims{UserID: "uA"}}
	b := &stubTokenValidator{name: "b", accept: true, out: &Claims{UserID: "uB"}}
	m, err := NewMultiValidator(a, b)
	if err != nil {
		t.Fatalf("NewMultiValidator: %v", err)
	}
	c, err := m.ValidateToken("tok")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if c.UserID != "uA" {
		t.Errorf("first validator should win; got UserID=%q", c.UserID)
	}
	if b.calls != 0 {
		t.Errorf("second validator should not have been consulted; calls=%d", b.calls)
	}
}

func TestMultiValidator_SecondAcceptsAfterFirstRejects(t *testing.T) {
	a := &stubTokenValidator{name: "a", accept: false}
	b := &stubTokenValidator{name: "b", accept: true, out: &Claims{UserID: "uB"}}
	cc := &stubTokenValidator{name: "c", accept: true, out: &Claims{UserID: "uC"}}
	m, _ := NewMultiValidator(a, b, cc)
	c, err := m.ValidateToken("tok")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if c.UserID != "uB" {
		t.Errorf("second validator should have won; got UserID=%q", c.UserID)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("expected a=1 b=1 calls, got a=%d b=%d", a.calls, b.calls)
	}
	if cc.calls != 0 {
		t.Errorf("third validator should not have been consulted; calls=%d", cc.calls)
	}
}

func TestMultiValidator_AllRejectReturnsLastError(t *testing.T) {
	a := &stubTokenValidator{name: "a", accept: false}
	b := &stubTokenValidator{name: "b", accept: false}
	m, _ := NewMultiValidator(a, b)
	_, err := m.ValidateToken("tok")
	if err == nil {
		t.Fatal("expected error when all validators reject")
	}
	if want := "b: rejected"; err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

func TestMultiValidator_RequiresAtLeastOne(t *testing.T) {
	if _, err := NewMultiValidator(); err == nil {
		t.Error("expected error when constructed with zero validators")
	}
	if _, err := NewMultiValidator(nil); err == nil {
		t.Error("expected error when constructed with a nil validator")
	}
}

// A validator trusts exactly the key its constructor was handed. This
// package used to accept the NAME of an environment variable and read the
// secret itself, which put the key a service authenticates with outside
// everything the app declares: the value appears in no config proto, so no
// deploy renders it, no config validation sees it, and a stale export in the
// process environment is indistinguishable from a configured issuer.
//
// The key is a config field (`sensitive: true`), so it arrives from the
// environment's secret provider and the app hands it in.
func TestHMACValidator_TrustsOnlyTheKeyItWasHanded(t *testing.T) {
	t.Setenv("SUPABASE_JWT_SECRET", "supa-secret")
	if _, err := NewHMACValidator(HMACValidatorConfig{SigningMethod: "HS256"}); err == nil {
		t.Fatal("a validator constructed with no Secret must refuse, however many plausible secrets are in the environment")
	}

	v, err := NewHMACValidator(HMACValidatorConfig{
		SigningMethod: "HS256",
		Secret:        "supa-secret",
	})
	if err != nil {
		t.Fatalf("NewHMACValidator: %v", err)
	}
	tok := signHS256(t, "supa-secret", jwt.MapClaims{
		"sub": "u-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	c, err := v.ValidateToken(tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if c.UserID != "u-1" {
		t.Errorf("UserID = %q, want u-1", c.UserID)
	}
}

func TestHMACValidator_WrongSigningMethod(t *testing.T) {
	if _, err := NewHMACValidator(HMACValidatorConfig{
		SigningMethod: "RS256",
		Secret:        "x",
	}); err == nil {
		t.Error("expected error when SigningMethod is not HS*")
	}
}

func TestJWKSValidator_KeyFuncRequired(t *testing.T) {
	if _, err := NewJWKSValidator(JWKSValidatorConfig{}); err == nil {
		t.Error("expected error when KeyFunc is nil")
	}
}

func TestJWKSValidator_ValidToken(t *testing.T) {
	secret := []byte("kf-secret")
	kf := func(_ *jwt.Token) (interface{}, error) { return secret, nil }
	v, err := NewJWKSValidator(JWKSValidatorConfig{
		SigningMethod: "HS256",
		KeyFunc:       kf,
	})
	if err != nil {
		t.Fatalf("NewJWKSValidator: %v", err)
	}
	tok := signHS256(t, string(secret), jwt.MapClaims{
		"sub": "j-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	c, err := v.ValidateToken(tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if c.UserID != "j-1" {
		t.Errorf("UserID = %q, want j-1", c.UserID)
	}
}

// TestConfig_TokenValidators_HMACThenJWKS exercises the Multi+HMAC+JWKS path
// through NewValidator → AuthenticateHeaders. This is the cp-forge migration
// scenario: same service must accept Supabase HMAC tokens AND Auth0 JWKS
// tokens during the cutover.
func TestConfig_TokenValidators_HMACThenJWKS(t *testing.T) {
	supabaseSecret := "supabase-shared"
	auth0Secret := []byte("auth0-jwks-key") // stand-in for a fetched JWKS key

	hmac, err := NewHMACValidator(HMACValidatorConfig{
		SigningMethod: "HS256",
		Secret:        supabaseSecret,
	})
	if err != nil {
		t.Fatalf("NewHMACValidator: %v", err)
	}
	jwks, err := NewJWKSValidator(JWKSValidatorConfig{
		SigningMethod: "HS256",
		KeyFunc:       func(*jwt.Token) (interface{}, error) { return auth0Secret, nil },
	})
	if err != nil {
		t.Fatalf("NewJWKSValidator: %v", err)
	}

	v, err := NewValidator(Config{
		Provider:        ProviderJWT,
		TokenValidators: []TokenValidator{hmac, jwks},
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	// Token #1: signed by Supabase secret → HMAC validator accepts.
	supTok := signHS256(t, supabaseSecret, jwt.MapClaims{
		"sub":   "supa-user",
		"email": "u@supa.io",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	h := http.Header{}
	h.Set("Authorization", "Bearer "+supTok)
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("supabase token: %v", err)
	}
	if c.UserID != "supa-user" {
		t.Errorf("supabase token: UserID=%q want supa-user", c.UserID)
	}

	// Token #2: signed by the JWKS key → HMAC rejects, JWKS accepts.
	jwksTok := signHS256(t, string(auth0Secret), jwt.MapClaims{
		"sub":   "auth0|user-2",
		"email": "u@auth0.io",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	h = http.Header{}
	h.Set("Authorization", "Bearer "+jwksTok)
	c, err = v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("auth0 token: %v", err)
	}
	if c.UserID != "auth0|user-2" {
		t.Errorf("auth0 token: UserID=%q want auth0|user-2", c.UserID)
	}

	// Token #3: signed by neither → both reject → Unauthenticated.
	badTok := signHS256(t, "nope", jwt.MapClaims{"sub": "x", "exp": time.Now().Add(time.Hour).Unix()})
	h = http.Header{}
	h.Set("Authorization", "Bearer "+badTok)
	_, err = v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad token: expected Unauthenticated, got %v", err)
	}
}
