//forge:lint-disable-next-line forge-exclude-contract-outbound-io: resolver.go's TCP dial is a local port-availability probe inside a KCL plugin callback; there is no seam the KCL runtime would let us inject
//forge:exclude-contract: KCL-plugin framework glue: registers a jwk resolver with the KCL runtime, which dictates the shape
package kclplugin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
)

// DeriveJWK derives the PUBLIC JWK document from a private-key PEM, so a
// forge.TestJWKS can publish the public half of the exact key its signer uses
// — the JWKS and the signer can never drift.
//
// Two key types, because two signing algorithms matter:
//
//   - ES256 (P-256 EC) — what Supabase-style end-user tokens use.
//   - RS256 (RSA) — what the common OIDC providers (Zitadel, Auth0, Okta,
//     Keycloak) sign with. A verifier configured for one of them PINS RS256,
//     so a test IdP limited to ES256 cannot stand in for any of them.
//
// alg is a CLAIM about the key and is checked against it: a JWKS advertising
// RS256 over an EC key makes every verifier reject every token, and that
// surfaces as a 401 far from the declaration that caused it. An empty alg is
// inferred from the key. A symmetric alg (HS*) is refused — it has no public
// half to publish.
//
// Returns a map[string]any (KCL-plugin-friendly) shaped exactly like one
// entry in a JWKS `keys` array, carrying ONLY public members.
func DeriveJWK(privateKeyPEM, kid, alg string) (map[string]any, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("derive_jwk: no PEM block in private key")
	}
	key, err := parsePrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("derive_jwk: parse private key: %w", err)
	}
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		if alg == "" {
			alg = "ES256"
		}
		if alg != "ES256" {
			return nil, fmt.Errorf("derive_jwk: alg %q cannot be published for an EC key: an EC (P-256) key signs ES256 only", alg)
		}
		return ecJWK(k, kid, alg)
	case *rsa.PrivateKey:
		if alg == "" {
			alg = "RS256"
		}
		if alg != "RS256" {
			return nil, fmt.Errorf("derive_jwk: alg %q cannot be published for an RSA key: forge derives RS256 JWKs from RSA keys", alg)
		}
		return map[string]any{
			"kty": "RSA",
			"n":   base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
			"kid": kid,
			"alg": alg,
			"use": "sig",
		}, nil
	default:
		return nil, fmt.Errorf("derive_jwk: unsupported private key type %T (want an EC P-256 or RSA key)", key)
	}
}

// ecJWK encodes a P-256 public key. The fixed-width coordinates come from
// crypto/ecdh, whose Bytes() is the uncompressed SEC1 point (0x04 || X || Y)
// with each 32-byte field element left-padded — so a leading-zero coordinate
// still encodes to the fixed width JWK consumers expect.
func ecJWK(key *ecdsa.PrivateKey, kid, alg string) (map[string]any, error) {
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("derive_jwk: EC key is on %s, want P-256 (ES256)", key.Curve.Params().Name)
	}
	const coordLen = 32
	ecdhPub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, fmt.Errorf("derive_jwk: convert public key to ECDH: %w", err)
	}
	point := ecdhPub.Bytes() // 0x04 || X(32) || Y(32)
	if len(point) != 1+2*coordLen {
		return nil, fmt.Errorf("derive_jwk: unexpected point length %d", len(point))
	}
	return map[string]any{
		"kty": "EC",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(point[1 : 1+coordLen]),
		"y":   base64.RawURLEncoding.EncodeToString(point[1+coordLen : 1+2*coordLen]),
		"kid": kid,
		"alg": alg,
		"use": "sig",
	}, nil
}

// parsePrivateKey parses SEC1 ("EC PRIVATE KEY"), PKCS#1 ("RSA PRIVATE KEY")
// or PKCS#8 ("PRIVATE KEY"), so whichever PEM header a user holds works.
func parsePrivateKey(der []byte) (any, error) {
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	return x509.ParsePKCS8PrivateKey(der)
}
