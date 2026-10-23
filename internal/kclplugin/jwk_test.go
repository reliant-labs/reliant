package kclplugin

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
)

const jwkTestPEM = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIB2fmGPM1CXk+kC+GBvg5pD/zwwK+XRy8+mKNfr0PABuoAoGCCqGSM49
AwEHoUQDQgAEGdqz6sZl229WS3ixXQmFory5kkkus2UT4cBGQuO3dpMN2FQ/8260
9YszSMpty7qF7I3/9elHmcVvzBglAF7CrQ==
-----END EC PRIVATE KEY-----`

func TestDeriveJWK_ES256(t *testing.T) {
	jwk, err := DeriveJWK(jwkTestPEM, "e2e-test-es256", "ES256")
	if err != nil {
		t.Fatalf("DeriveJWK: %v", err)
	}
	if jwk["kty"] != "EC" || jwk["crv"] != "P-256" {
		t.Errorf("kty/crv wrong: %v / %v", jwk["kty"], jwk["crv"])
	}
	if jwk["kid"] != "e2e-test-es256" || jwk["alg"] != "ES256" || jwk["use"] != "sig" {
		t.Errorf("kid/alg/use wrong: %v", jwk)
	}
	// x/y must be 32-byte (P-256) base64url-no-pad coordinates.
	for _, f := range []string{"x", "y"} {
		s, _ := jwk[f].(string)
		dec, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Errorf("%s not base64url: %v", f, err)
		}
		if len(dec) != 32 {
			t.Errorf("%s decoded len = %d want 32", f, len(dec))
		}
	}
	// Deterministic: same PEM => same JWK.
	jwk2, _ := DeriveJWK(jwkTestPEM, "e2e-test-es256", "ES256")
	if jwk["x"] != jwk2["x"] || jwk["y"] != jwk2["y"] {
		t.Error("derivation not deterministic")
	}
}

// An empty alg is inferred from the KEY, never assumed: an EC key is ES256.
func TestDeriveJWK_AlgInferredFromKey(t *testing.T) {
	jwk, err := DeriveJWK(jwkTestPEM, "k", "")
	if err != nil {
		t.Fatalf("DeriveJWK: %v", err)
	}
	if jwk["alg"] != "ES256" {
		t.Errorf("empty alg on an EC key should be ES256, got %v", jwk["alg"])
	}
	rsaPEM, _ := testRSAKey(t, "RSA PRIVATE KEY")
	jwk, err = DeriveJWK(rsaPEM, "k", "")
	if err != nil {
		t.Fatalf("DeriveJWK(rsa): %v", err)
	}
	if jwk["alg"] != "RS256" || jwk["kty"] != "RSA" {
		t.Errorf("empty alg on an RSA key should be RS256/RSA, got %v/%v", jwk["alg"], jwk["kty"])
	}
}

func TestDeriveJWK_BadPEM(t *testing.T) {
	if _, err := DeriveJWK("not a pem", "k", "ES256"); err == nil {
		t.Error("expected error for non-PEM input")
	}
}

// RS256 is what the common OIDC providers (Zitadel, Auth0, Okta, Keycloak)
// sign with, so a test IdP that can only publish ES256 cannot stand in for
// them: a verifier pinned to RS256 rejects every token it signs. The JWK must
// be the PUBLIC half of exactly the key given — proven by verifying, with the
// key reassembled from the JWK's n/e alone, a signature the private key made.
//
// Both PEM encodings a user might hold are accepted.
func TestDeriveJWK_RS256VerifiesWhatTheKeySigns(t *testing.T) {
	for _, header := range []string{"RSA PRIVATE KEY", "PRIVATE KEY"} {
		t.Run(header, func(t *testing.T) {
			pemStr, key := testRSAKey(t, header)
			jwk, err := DeriveJWK(pemStr, "op-1", "RS256")
			if err != nil {
				t.Fatalf("DeriveJWK: %v", err)
			}
			if jwk["kty"] != "RSA" || jwk["alg"] != "RS256" || jwk["kid"] != "op-1" || jwk["use"] != "sig" {
				t.Fatalf("jwk header fields wrong: %v", jwk)
			}
			for _, private := range []string{"d", "p", "q", "dp", "dq", "qi"} {
				if _, ok := jwk[private]; ok {
					t.Fatalf("the JWK carries private member %q: the published document would leak the key", private)
				}
			}
			n, err := base64.RawURLEncoding.DecodeString(jwk["n"].(string))
			if err != nil {
				t.Fatalf("n not base64url: %v", err)
			}
			e, err := base64.RawURLEncoding.DecodeString(jwk["e"].(string))
			if err != nil {
				t.Fatalf("e not base64url: %v", err)
			}
			pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}

			digest := sha256.Sum256([]byte("header.payload"))
			sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
				t.Fatalf("the published JWK does not verify a signature its private key made: %v", err)
			}
		})
	}
}

// The alg is a CLAIM about the key, so a mismatch is refused rather than
// published: a JWKS advertising RS256 over an EC key (or the reverse) makes
// every verifier reject every token, and the failure surfaces as a 401 far
// from the declaration that caused it. A symmetric alg can never be published.
func TestDeriveJWK_RefusesAnAlgTheKeyCannotSign(t *testing.T) {
	rsaPEM, _ := testRSAKey(t, "RSA PRIVATE KEY")
	for name, tc := range map[string]struct{ pem, alg, want string }{
		"RS256 over an EC key":  {jwkTestPEM, "RS256", "RS256"},
		"ES256 over an RSA key": {rsaPEM, "ES256", "ES256"},
		"symmetric HS256":       {rsaPEM, "HS256", "HS256"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DeriveJWK(tc.pem, "k", tc.alg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal naming %s", err, tc.want)
			}
		})
	}
}

func testRSAKey(t *testing.T, header string) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var der []byte
	if header == "PRIVATE KEY" {
		if der, err = x509.MarshalPKCS8PrivateKey(key); err != nil {
			t.Fatal(err)
		}
	} else {
		der = x509.MarshalPKCS1PrivateKey(key)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: header, Bytes: der})), key
}
