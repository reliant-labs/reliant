// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

var (
	ErrInvalidToken     = errors.New("invalid token")
	ErrExpiredToken     = errors.New("token expired")
	ErrInvalidSignature = errors.New("invalid signature")
	ErrInvalidPublicKey = errors.New("invalid public key")
)

// IsCredentialRejection reports whether err is the credential's own verdict —
// verification RAN and the token was rejected (unknown, malformed, wrong kind,
// revoked, expired, bad signature).
//
// Every OTHER error from a validator means verification never happened: the
// token store was unreachable, a query deadline blew, a JWKS fetch failed.
// Those must not be reported as a rejection. A caller that reports them as one
// tells the operator their credential was refused and to mint a new one, which
// cannot fix an unreachable dependency and buries the only real signal — this
// is not hypothetical; a Postgres stall surfaced to the CLI as
// "the API token stored in context "default" was rejected ... run
// 'reliant auth token create'".
//
// ErrInvalidPublicKey is deliberately NOT a rejection: it covers server
// misconfiguration and JWKS fetch failure, which are ours, not the caller's.
//
// A new rejection sentinel belongs in this list. That is the whole point of
// having one predicate instead of an errors.Is chain at each call site.
func IsCredentialRejection(err error) bool {
	return errors.Is(err, ErrInvalidToken) ||
		errors.Is(err, ErrExpiredToken) ||
		errors.Is(err, ErrInvalidSignature) ||
		errors.Is(err, ErrInvalidAPIKey)
}

// JWTClaims represents the JWT claims from Supabase
type JWTClaims struct {
	Sub   string `json:"sub"`   // User ID
	Email string `json:"email"` // User email
	Role  string `json:"role"`  // User role
	Exp   int64  `json:"exp"`   // Expiration time
	Iat   int64  `json:"iat"`   // Issued at
}

// JWTHeader represents the JWT header
type JWTHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// JWTValidator validates Supabase JWT tokens using ECDSA public key (ES256)
type JWTValidator struct {
	publicKey *ecdsa.PublicKey
	// keysByKid holds every EC key the JWKS published, indexed by kid, so a
	// token is verified against the key its header names rather than always
	// the document's first key. Nil for PEM-constructed validators (single
	// key, no kid to match). During an IdP key rotation a JWKS carries both
	// the old and new key; picking Keys[0] unconditionally rejects every
	// token signed with the other one.
	keysByKid map[string]*ecdsa.PublicKey
}

// NewJWTValidator creates a new JWT validator with ECDSA public key from PEM format
func NewJWTValidator(publicKeyPEM string) (*JWTValidator, error) {
	if publicKeyPEM == "" {
		return nil, ErrInvalidPublicKey
	}

	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("%w: failed to decode PEM block", ErrInvalidPublicKey)
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPublicKey, err)
	}

	ecdsaPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an ECDSA public key", ErrInvalidPublicKey)
	}

	return &JWTValidator{
		publicKey: ecdsaPub,
	}, nil
}

// NewJWTValidatorFromJWKS creates a validator from JWKS JSON
func NewJWTValidatorFromJWKS(jwksJSON string) (*JWTValidator, error) {
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"` // Curve for EC keys
			X   string `json:"x"`   // X coordinate
			Y   string `json:"y"`   // Y coordinate
			Kid string `json:"kid"`
		} `json:"keys"`
	}

	if err := json.Unmarshal([]byte(jwksJSON), &jwks); err != nil {
		return nil, fmt.Errorf("%w: failed to parse JWKS: %v", ErrInvalidPublicKey, err)
	}

	if len(jwks.Keys) == 0 {
		return nil, fmt.Errorf("%w: no keys in JWKS", ErrInvalidPublicKey)
	}

	// Parse every EC key, indexed by kid, so validation can honor the token
	// header's kid. Non-EC entries are skipped (a JWKS may also publish RSA
	// keys for other consumers); at least one EC key is required.
	keysByKid := make(map[string]*ecdsa.PublicKey)
	var firstKey *ecdsa.PublicKey
	for _, key := range jwks.Keys {
		if key.Kty != "EC" {
			continue
		}

		var curve elliptic.Curve
		switch key.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("%w: unsupported curve %s", ErrInvalidPublicKey, key.Crv)
		}

		xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to decode X coordinate: %v", ErrInvalidPublicKey, err)
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to decode Y coordinate: %v", ErrInvalidPublicKey, err)
		}

		ecdsaPub := &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}
		if firstKey == nil {
			firstKey = ecdsaPub
		}
		if key.Kid != "" {
			keysByKid[key.Kid] = ecdsaPub
		}
	}

	if firstKey == nil {
		return nil, fmt.Errorf("%w: no EC keys in JWKS", ErrInvalidPublicKey)
	}

	return &JWTValidator{
		publicKey: firstKey,
		keysByKid: keysByKid,
	}, nil
}

// LoadJWKS fetches a JWKS document from the given URL and returns a JWTValidator.
func LoadJWKS(ctx context.Context, jwksURL string) (*JWTValidator, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create JWKS request: %v", ErrInvalidPublicKey, err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to fetch JWKS from %s: %v", ErrInvalidPublicKey, jwksURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: JWKS endpoint returned status %d", ErrInvalidPublicKey, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read JWKS response: %v", ErrInvalidPublicKey, err)
	}

	return NewJWTValidatorFromJWKS(string(body))
}

// ValidateToken validates a JWT token and returns the claims
func (v *JWTValidator) ValidateToken(tokenString string) (*JWTClaims, error) {
	// Split token into parts
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	// Decode and verify header
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: failed to decode header", ErrInvalidToken)
	}

	var header JWTHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("%w: failed to parse header", ErrInvalidToken)
	}

	// Verify algorithm
	if header.Alg != "ES256" {
		return nil, fmt.Errorf("%w: unsupported algorithm %s, expected ES256", ErrInvalidToken, header.Alg)
	}

	// Pick the verification key. When the JWKS published multiple keys and the
	// token names one by kid, verify against exactly that key — never fall
	// back to another on a named-but-unknown kid, which would let a token
	// shop for a more favorable key. Tokens without a kid, and validators
	// built from a single PEM key, use the default key as before.
	verifyKey := v.publicKey
	if header.Kid != "" && len(v.keysByKid) > 0 {
		key, ok := v.keysByKid[header.Kid]
		if !ok {
			return nil, fmt.Errorf("%w: unknown key id %q", ErrInvalidToken, header.Kid)
		}
		verifyKey = key
	}

	// Verify signature
	if err := v.verifySignatureWithKey(verifyKey, parts[0], parts[1], parts[2]); err != nil {
		return nil, err
	}

	// Decode payload
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: failed to decode payload", ErrInvalidToken)
	}

	// Parse claims
	var claims JWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("%w: failed to parse claims", ErrInvalidToken)
	}

	// Check expiration
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return nil, ErrExpiredToken
	}

	return &claims, nil
}

// verifySignature verifies the JWT signature using ECDSA (ES256) against the
// validator's default key.
func (v *JWTValidator) verifySignature(header, payload, signature string) error {
	return v.verifySignatureWithKey(v.publicKey, header, payload, signature)
}

// verifySignatureWithKey verifies the JWT signature using ECDSA (ES256)
// against the given key (selected by kid when the JWKS published several).
func (v *JWTValidator) verifySignatureWithKey(publicKey *ecdsa.PublicKey, header, payload, signature string) error {
	// Create the message that was signed
	message := header + "." + payload

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Decode the signature
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("%w: failed to decode signature", ErrInvalidSignature)
	}

	// ECDSA signature can be in two formats:
	// 1. ASN.1 DER encoded (standard)
	// 2. Raw format (r || s concatenated)
	var r, s *big.Int

	// Try ASN.1 format first
	var ecdsaSig struct {
		R, S *big.Int
	}
	if _, err := asn1.Unmarshal(sig, &ecdsaSig); err == nil {
		r = ecdsaSig.R
		s = ecdsaSig.S
	} else {
		// Try raw format (64 bytes for P-256: 32 bytes R + 32 bytes S)
		if len(sig) != 64 {
			return fmt.Errorf("%w: invalid ECDSA signature length %d, expected 64", ErrInvalidSignature, len(sig))
		}
		r = new(big.Int).SetBytes(sig[:32])
		s = new(big.Int).SetBytes(sig[32:])
	}

	// Verify the signature
	if !ecdsa.Verify(publicKey, hash[:], r, s) {
		return ErrInvalidSignature
	}

	return nil
}
