package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func jwksFor(t *testing.T, crv string, x, y []byte) string {
	t.Helper()
	doc, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": crv, "kid": "k1",
		"x": base64.RawURLEncoding.EncodeToString(x),
		"y": base64.RawURLEncoding.EncodeToString(y),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(doc)
}

// The key a JWKS publishes must come back as the same key, on every curve.
func TestJWKSKeyRoundTripsOnEveryCurve(t *testing.T) {
	for name, curve := range map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384(), "P-521": elliptic.P521()} {
		t.Run(name, func(t *testing.T) {
			priv, err := ecdsa.GenerateKey(curve, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ecdhPub, err := priv.PublicKey.ECDH()
			if err != nil {
				t.Fatal(err)
			}
			raw := ecdhPub.Bytes() // 0x04 || X || Y, full width
			size := (len(raw) - 1) / 2
			v, err := NewJWTValidatorFromJWKS(jwksFor(t, name, raw[1:1+size], raw[1+size:]))
			if err != nil {
				t.Fatalf("valid key rejected: %v", err)
			}
			if !v.publicKey.Equal(&priv.PublicKey) {
				t.Fatal("parsed key differs from the published one")
			}
		})
	}
}

// A coordinate with leading zero bytes (which a big-endian encoder may trim)
// must still parse: find a key whose X or Y has a leading zero and trim it.
func TestJWKSKeyAcceptsTrimmedLeadingZeros(t *testing.T) {
	for i := 0; i < 4000; i++ {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		ecdhPub, _ := priv.PublicKey.ECDH()
		raw := ecdhPub.Bytes()
		x, y := raw[1:33], raw[33:]
		if x[0] != 0 && y[0] != 0 {
			continue
		}
		trim := func(b []byte) []byte {
			for len(b) > 1 && b[0] == 0 {
				b = b[1:]
			}
			return b
		}
		v, err := NewJWTValidatorFromJWKS(jwksFor(t, "P-256", trim(x), trim(y)))
		if err != nil {
			t.Fatalf("trimmed coordinates rejected: %v", err)
		}
		if !v.publicKey.Equal(&priv.PublicKey) {
			t.Fatal("trimmed key parsed to a different key")
		}
		return
	}
	t.Skip("no key with a leading-zero coordinate found")
}

func TestJWKSKeyRejectsPointsNotOnTheCurve(t *testing.T) {
	x := make([]byte, 32)
	y := make([]byte, 32)
	x[31], y[31] = 1, 1 // (1,1) is not on P-256
	if _, err := NewJWTValidatorFromJWKS(jwksFor(t, "P-256", x, y)); err == nil {
		t.Fatal("a point off the curve was accepted")
	}
	long := make([]byte, 33)
	if _, err := NewJWTValidatorFromJWKS(jwksFor(t, "P-256", long, y)); err == nil {
		t.Fatalf("a %d-byte coordinate was accepted on P-256", len(long))
	}
}
