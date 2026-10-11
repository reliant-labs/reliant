package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"
)

// signES256 builds a Supabase-shaped ES256 token over claims.
func signES256(t *testing.T, key *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signingInput := enc(map[string]string{"alg": "ES256", "typ": "JWT"}) + "." + enc(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newTestKeyAndPEM(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// TestValidateToken_EmailVerifiedClaim pins the email rule shared with
// control-plane's validator. The Supabase custom access-token hook
// (control-plane deploy/supabase/hooks/) stamps a top-level email_verified.
// Present, it decides: only boolean true keeps the address. Absent, the hook
// is not live yet and AUTH_REQUIRE_EMAIL_VERIFIED_CLAIM decides: off keeps the
// email (today's behaviour), on drops it.
func TestValidateToken_EmailVerifiedClaim(t *testing.T) {
	const keep = "person@example.com"
	for _, tc := range []struct {
		name    string
		require string
		extra   map[string]any
		want    string
	}{
		{"claim true keeps the email", "", map[string]any{"email_verified": true}, keep},
		{"claim true keeps the email with the switch on", "true", map[string]any{"email_verified": true}, keep},
		{"claim false drops the email", "", map[string]any{"email_verified": false}, ""},
		{"claim false drops the email with the switch on", "true", map[string]any{"email_verified": false}, ""},
		{"a string \"true\" is not verified", "", map[string]any{"email_verified": "true"}, ""},
		{"a null claim is not verified", "", map[string]any{"email_verified": nil}, ""},
		{"claim absent with the switch off keeps today's behaviour", "", nil, keep},
		{"claim absent with the switch explicitly false keeps today's behaviour", "false", nil, keep},
		{"claim absent with the switch on yields no email", "true", nil, ""},
		// GoTrue never confirms an email on a token that is still anonymous.
		{"an anonymous token carries no email", "", map[string]any{"is_anonymous": true}, ""},
		{"user_metadata.email_verified does not count", "true",
			map[string]any{"user_metadata": map[string]any{"email_verified": true}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(RequireEmailVerifiedClaimEnv, tc.require)
			key, pubPEM := newTestKeyAndPEM(t)
			v, err := NewJWTValidator(pubPEM)
			if err != nil {
				t.Fatalf("NewJWTValidator: %v", err)
			}
			claims := map[string]any{
				"sub":          "6d093c9d-fdb1-41d0-b43a-ba12178a4b42",
				"email":        keep,
				"role":         "authenticated",
				"is_anonymous": false,
				"exp":          time.Now().Add(time.Hour).Unix(),
			}
			for k, val := range tc.extra {
				claims[k] = val
			}
			got, err := v.ValidateToken(signES256(t, key, claims))
			if err != nil {
				t.Fatalf("ValidateToken: %v", err)
			}
			if got.Email != tc.want {
				t.Errorf("email = %q, want %q", got.Email, tc.want)
			}
			if got.Sub == "" {
				t.Error("sub lost")
			}
		})
	}
}

// TestJWKSValidatorReadsTheSwitch: the JWKS constructor — the one every cloud
// env uses (RELIANT_JWKS_URL) — must honor the switch too.
func TestJWKSValidatorReadsTheSwitch(t *testing.T) {
	t.Setenv(RequireEmailVerifiedClaimEnv, "true")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdhPub, err := key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	raw := ecdhPub.Bytes()
	v, err := NewJWTValidatorFromJWKS(jwksFor(t, "P-256", raw[1:33], raw[33:]))
	if err != nil {
		t.Fatalf("NewJWTValidatorFromJWKS: %v", err)
	}
	got, err := v.ValidateToken(signES256(t, key, map[string]any{
		"sub":   "user-1",
		"email": "person@example.com",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}))
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if got.Email != "" {
		t.Errorf("email = %q, want empty: the switch is on and the token has no email_verified", got.Email)
	}
}
