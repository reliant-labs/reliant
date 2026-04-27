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

	// Use the first key
	key := jwks.Keys[0]
	if key.Kty != "EC" {
		return nil, fmt.Errorf("%w: key type must be EC, got %s", ErrInvalidPublicKey, key.Kty)
	}

	// Determine curve
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

	// Decode X coordinate
	xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to decode X coordinate: %v", ErrInvalidPublicKey, err)
	}

	// Decode Y coordinate
	yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to decode Y coordinate: %v", ErrInvalidPublicKey, err)
	}

	ecdsaPub := &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}

	return &JWTValidator{
		publicKey: ecdsaPub,
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

	// Verify signature
	if err := v.verifySignature(parts[0], parts[1], parts[2]); err != nil {
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

// verifySignature verifies the JWT signature using ECDSA (ES256)
func (v *JWTValidator) verifySignature(header, payload, signature string) error {
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
	if !ecdsa.Verify(v.publicKey, hash[:], r, s) {
		return ErrInvalidSignature
	}

	return nil
}
