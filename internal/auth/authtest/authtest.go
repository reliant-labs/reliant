// Copyright (c) 2025 Reliant Labs

// Package authtest mints the credentials reliant's user audience accepts, for
// tests: a managed identity's issuer (an RSA key behind a JWKS on an
// httptest server) and a Supabase project (an ES256 key). It is test
// infrastructure, as net/http/httptest is; nothing that serves imports it.
//
//forge:exclude-contract: test fixtures (issuers that mint tokens), not a service
package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Identity is a managed identity's issuer: what JWT_ISSUER, JWT_AUDIENCE and
// JWT_JWKS_URL name.
type Identity struct {
	Issuer   string
	Audience string
	JWKSURL  string
	key      *rsa.PrivateKey
}

// NewIdentity starts an issuer whose JWKS is served until t ends.
func NewIdentity(t testing.TB) *Identity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "identity-1", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(srv.Close)
	return &Identity{
		Issuer:   "https://reliant.reliantidentity.com",
		Audience: "312309114829078531",
		JWKSURL:  srv.URL,
		key:      key,
	}
}

// Sign mints an access token of the identity for sub, valid for an hour,
// with over merged onto its claims (a nil value deletes the claim).
func (i *Identity) Sign(t testing.TB, sub string, over map[string]any) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":            i.Issuer,
		"aud":            []string{i.Audience},
		"sub":            sub,
		"email":          sub + "@example.com",
		"email_verified": true,
		"client_id":      "312309114829078532@reliant",
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Unix(),
	}
	merge(claims, over)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "identity-1"
	s, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Supabase is a Supabase project's session signer: what JWT_PUBLIC_KEY and
// SUPABASE_JWT_ISSUER name.
type Supabase struct {
	PublicKeyPEM string
	Issuer       string
	key          *ecdsa.PrivateKey
}

// NewSupabase makes a project with a fresh ES256 key.
func NewSupabase(t testing.TB) *Supabase {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return &Supabase{
		PublicKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
		Issuer:       "https://project.supabase.co/auth/v1",
		key:          key,
	}
}

// Sign mints a session JWT for sub, valid for an hour, with over merged onto
// its claims (a nil value deletes the claim).
func (s *Supabase) Sign(t testing.TB, sub string, over map[string]any) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":   s.Issuer,
		"aud":   "authenticated",
		"sub":   sub,
		"email": sub + "@example.com",
		"role":  "authenticated",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	merge(claims, over)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func merge(claims jwt.MapClaims, over map[string]any) {
	for k, v := range over {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
}
