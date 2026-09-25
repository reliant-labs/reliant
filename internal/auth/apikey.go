// Copyright (c) 2025 Reliant Labs
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: TokenValidator (JWT / API key / test) is already the strategy seam; moving it to contract.go is deferred with the rest of the auth split; tracked in H-RELIANT-CI-lint follow-ups
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: jwt.go fetches the JWKS over HTTP to verify tokens; splitting the fetcher into an adapter is deferred (reliant is not forge-generated); tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: JWT/JWKS validation and credential types used by middleware; the validators are selected per auth mode
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
)

var ErrInvalidAPIKey = errors.New("invalid API key")

// apiKeyValidator validates bearer tokens against a configured API key.
// It implements the tokenValidator interface for use with the auth middleware.
type apiKeyValidator struct {
	key    []byte // raw API key for constant-time comparison
	userID string // deterministic user ID derived from the key
}

// NewAPIKeyValidator creates a validator that checks bearer tokens against the
// given API key using constant-time comparison. A deterministic user ID is
// derived from the SHA-256 hash of the key so the same key always maps to the
// same identity.
func NewAPIKeyValidator(apiKey string) (*apiKeyValidator, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("%w: API key must not be empty", ErrInvalidAPIKey)
	}

	hash := sha256.Sum256([]byte(apiKey))
	userID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		hash[0:4],
		hash[4:6],
		hash[6:8],
		hash[8:10],
		hash[10:16],
	)

	return &apiKeyValidator{
		key:    []byte(apiKey),
		userID: userID,
	}, nil
}

// ValidateToken checks whether the provided token matches the configured API
// key using constant-time comparison. On success it returns synthetic JWTClaims
// with the deterministic user ID.
func (v *apiKeyValidator) ValidateToken(token string) (*JWTClaims, error) {
	if subtle.ConstantTimeCompare([]byte(token), v.key) != 1 {
		return nil, ErrInvalidAPIKey
	}

	return &JWTClaims{
		Sub:   v.userID,
		Role:  "authenticated",
		Email: "apikey@localhost",
	}, nil
}
