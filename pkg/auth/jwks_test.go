package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// These tests stand up a REAL JWKS endpoint on loopback (httptest) and drive
// the validator through the same code path a hosted OIDC issuer — Auth0,
// Keycloak, Zitadel, Supabase, Okta — takes. Nothing here reaches the
// network; the "issuer" is a local handler serving the public half of a
// throwaway key.
//
// This is the pin for the defect that made JWT_JWKS_URL a dead knob: it was
// scaffolded into every project, documented as THE RS256 path, and the
// validator answered every token with "JWKS key fetching not yet
// implemented". Before the fix TestJWKSURL_ValidatesRealIssuerToken fails on
// the very first Validate call.

// newJWKSServer serves a JWKS document containing the public half of key
// under kid, and returns its URL. The server is torn down with the test.
func newJWKSServer(t *testing.T, kid string, jwk map[string]string) string {
	t.Helper()
	doc, err := json.Marshal(map[string]any{"keys": []any{jwk}})
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/jwks.json") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/.well-known/jwks.json"
}

// rsaJWK projects an RSA public key onto the JWK fields a JWKS consumer
// reads (kty/n/e), tagged with kid + RS256.
func rsaJWK(t *testing.T, key *rsa.PrivateKey, kid string) map[string]string {
	t.Helper()
	return map[string]string{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(bigEndianExponent(key.PublicKey.E)),
	}
}

func bigEndianExponent(e int) []byte {
	var b []byte
	for e > 0 {
		b = append([]byte{byte(e & 0xff)}, b...)
		e >>= 8
	}
	return b
}

func signRS256WithKID(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign RS256: %v", err)
	}
	return s
}

// TestJWKSURL_ValidatesRealIssuerToken is the mission check for Fix 1: a
// token signed by a JWKS-backed issuer validates through the ORDINARY
// JWTConfig.JWKSURL knob — the one app-auth.go scaffolds and documents — with
// no KeyFunc and no fetched-and-pasted PEM.
func TestJWKSURL_ValidatesRealIssuerToken(t *testing.T) {
	key := rsaKeyFor(t)
	const kid = "issuer-key-1"
	url := newJWKSServer(t, kid, rsaJWK(t, key, kid))

	v, err := NewValidator(Config{
		Provider: ProviderJWT,
		JWT: JWTConfig{
			SigningMethod: "RS256",
			JWKSURL:       url,
			Issuer:        "https://issuer.example",
			Audience:      "my-api",
		},
	})
	if err != nil {
		t.Fatalf("NewValidator with JWKSURL: %v", err)
	}

	tok := signRS256WithKID(t, key, kid, jwt.MapClaims{
		"sub":   "user-42",
		"email": "u@example.com",
		"iss":   "https://issuer.example",
		"aud":   "my-api",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	c, err := v.AuthenticateHeaders(context.Background(), h, InterceptorOptions{})
	if err != nil {
		t.Fatalf("JWKS-backed token rejected: %v", err)
	}
	if c == nil || c.UserID != "user-42" || c.Email != "u@example.com" {
		t.Fatalf("claims mismatch: %+v", c)
	}
}

// A JWKS-backed validator still enforces the declared audience — the fix
// wires key RESOLUTION, it does not relax any check.
func TestJWKSURL_RejectsWrongAudience(t *testing.T) {
	key := rsaKeyFor(t)
	const kid = "issuer-key-1"
	url := newJWKSServer(t, kid, rsaJWK(t, key, kid))

	v, err := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "RS256", JWKSURL: url, Audience: "my-api"},
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	tok := signRS256WithKID(t, key, kid, jwt.MapClaims{
		"sub": "user-42",
		"aud": "someone-elses-api",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Validate(tok); err == nil {
		t.Fatal("expected a token minted for a different audience to be rejected")
	}
}

// A token signed by a key the JWKS does NOT publish is rejected: possession
// of the private half is the only way to obtain a principal, and publishing
// the public half is what lets a service verify without being able to issue.
func TestJWKSURL_RejectsUnpublishedKey(t *testing.T) {
	published := rsaKeyFor(t)
	attacker := rsaKeyFor(t)
	const kid = "issuer-key-1"
	url := newJWKSServer(t, kid, rsaJWK(t, published, kid))

	v, err := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "RS256", JWKSURL: url},
	})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	tok := signRS256WithKID(t, attacker, kid, jwt.MapClaims{
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Validate(tok); err == nil {
		t.Fatal("expected a token signed by an unpublished key to be rejected")
	}
}

// An unreachable JWKS endpoint fails at CONSTRUCTION — server boot — with the
// URL in the message, instead of surfacing as a per-request 401 nobody can
// attribute.
func TestJWKSURL_UnreachableFailsAtConstruction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	url := srv.URL + "/.well-known/jwks.json"
	srv.Close() // nothing is listening now

	_, err := NewValidator(Config{
		Provider: ProviderJWT,
		JWT:      JWTConfig{SigningMethod: "RS256", JWKSURL: url},
	})
	if err == nil {
		t.Fatal("expected NewValidator to fail fast on an unreachable JWKS endpoint")
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error must name the JWKS URL so the misconfiguration is attributable; got: %v", err)
	}
}

// Two key sources for one validator is a configuration error, not a
// precedence puzzle.
func TestJWKSURL_AndSecret_IsRejected(t *testing.T) {
	_, err := NewValidator(Config{
		Provider: ProviderJWT,
		JWT: JWTConfig{
			SigningMethod: "RS256",
			JWKSURL:       "https://issuer.example/.well-known/jwks.json",
			Secret:        "also-a-secret",
		},
	})
	if err == nil {
		t.Fatal("expected NewValidator to reject JWKSURL and Secret both being set")
	}
	if !strings.Contains(err.Error(), "TokenValidators") {
		t.Errorf("the error must point at the multi-issuer seam; got: %v", err)
	}
}
