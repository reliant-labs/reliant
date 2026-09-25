// Copyright (c) 2025 Reliant Labs
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Internal-service authentication (SENDER side).
//
// Reliant presents an internal-service bearer to control-plane's
// AccessTokenInternalService to introspect and mint `rlat_` access tokens
// without owning a token table. The token is HS256 over the shared
// INTERNAL_SERVICE_SECRET, claim-for-claim identical to control-plane's own
// signer (control-plane/internal/auth/internal_service.go) so control-plane's
// validator accepts it:
//
//	alg = HS256, sub = "internal-service", role = "admin",
//	iss = "control-plane", aud = "authenticated", exp = iat + 5m
//
// Reliant no longer RECEIVES internal-service tokens: the managed-daemon-token
// RPCs they gated are gone (control-plane mints managed daemons' credentials
// itself), so the verifier was deleted with them.

// InternalServiceSubject is the sub claim of an internal-service token.
const InternalServiceSubject = "internal-service"

// InternalServiceRole is the role claim of an internal-service token.
const InternalServiceRole = "admin"

// InternalServiceTokenTTL bounds a signed internal-service token's lifetime.
const InternalServiceTokenTTL = 5 * time.Minute

// ErrInternalServiceSecretUnset is returned when signing without
// INTERNAL_SERVICE_SECRET.
var ErrInternalServiceSecretUnset = errors.New("internal-service auth: INTERNAL_SERVICE_SECRET is not configured")

// SignInternalServiceToken mints the internal-service bearer.
func SignInternalServiceToken(secret string) (string, error) {
	if secret == "" {
		return "", ErrInternalServiceSecretUnset
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  InternalServiceSubject,
		"role": InternalServiceRole,
		"iss":  "control-plane",
		"aud":  "authenticated",
		"iat":  now.Unix(),
		"exp":  now.Add(InternalServiceTokenTTL).Unix(),
	})
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("signing internal-service token: %w", err)
	}
	return signed, nil
}
