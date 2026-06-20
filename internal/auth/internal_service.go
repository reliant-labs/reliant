// Copyright (c) 2025 Reliant Labs
package auth

import (
	"errors"
	"fmt"
	"os"

	"github.com/golang-jwt/jwt/v5"
)

// Internal-service authentication (receiver side).
//
// The control-plane operator provisions per-daemon PATs asynchronously at
// pod-create time. There is no end-user request in scope for that work, so the
// operator cannot present a Supabase user JWT. Instead it mints a short-lived
// HS256 token signed with a secret shared with reliant
// (INTERNAL_SERVICE_SECRET) and calls reliant's managed-daemon-token RPCs with
// it.
//
// This file is the reliant-side *verifier* for those tokens. It deliberately
// mirrors control-plane's signer
// (control-plane/internal/auth/internal_service.go SignInternalServiceToken)
// claim-for-claim so the token round-trips:
//
//	alg  = HS256
//	sub  = "internal-service"
//	role = "admin"
//	iss  = "control-plane"
//	aud  = "authenticated"
//	exp  = iat + 5m
//
// The signing/verification key is the raw bytes of INTERNAL_SERVICE_SECRET,
// which must hold the identical value on both sides.
//
// This verifier is intentionally narrow: it gates ONLY the managed-daemon-token
// RPCs (MintManagedDaemonToken / RevokeManagedDaemonToken) via
// InternalServiceInterceptor. It is NOT wired into the general gRPC auth
// interceptor, so it cannot be used to authenticate any user-facing RPC.

// InternalServiceSubject is the sub claim minted onto internal-service tokens
// by the control-plane operator. Verified so a leaked Supabase user JWT (which
// has a real user UUID as sub) cannot be replayed against these RPCs.
const InternalServiceSubject = "internal-service"

// InternalServiceRole is the role claim minted onto internal-service tokens.
const InternalServiceRole = "admin"

var (
	// ErrInternalServiceSecretUnset is returned when INTERNAL_SERVICE_SECRET is
	// not configured but an internal-service-gated RPC is invoked. We fail
	// closed: if the operator cannot be authenticated, the RPC is rejected.
	ErrInternalServiceSecretUnset = errors.New("internal-service auth: INTERNAL_SERVICE_SECRET is not configured")
	// ErrInvalidInternalServiceToken is returned when a presented token fails
	// signature, expiry, or claim validation.
	ErrInvalidInternalServiceToken = errors.New("internal-service auth: invalid token")
)

// InternalServiceVerifier validates internal-service tokens signed by the
// control-plane operator with the shared INTERNAL_SERVICE_SECRET.
type InternalServiceVerifier struct {
	secret []byte
}

// NewInternalServiceVerifier constructs a verifier from the shared secret.
// An empty secret yields a verifier that rejects every token with
// ErrInternalServiceSecretUnset (fail-closed), so wiring it up without the
// secret configured never silently allows traffic.
func NewInternalServiceVerifier(secret string) *InternalServiceVerifier {
	return &InternalServiceVerifier{secret: []byte(secret)}
}

// NewInternalServiceVerifierFromEnv reads INTERNAL_SERVICE_SECRET from the
// environment. See NewInternalServiceVerifier for the empty-secret behaviour.
func NewInternalServiceVerifierFromEnv() *InternalServiceVerifier {
	return NewInternalServiceVerifier(os.Getenv("INTERNAL_SERVICE_SECRET"))
}

// Enabled reports whether a secret is configured. When false, ValidateToken
// always fails closed; callers can use this to log a clear setup warning.
func (v *InternalServiceVerifier) Enabled() bool {
	return len(v.secret) > 0
}

// ValidateToken parses and verifies a raw internal-service JWT (no "Bearer "
// prefix). On success it returns the claims. It enforces:
//   - HS256 signing method (rejects "alg: none" and asymmetric algs)
//   - signature against the shared secret
//   - non-expired (exp)
//   - sub == InternalServiceSubject
//   - role == InternalServiceRole
func (v *InternalServiceVerifier) ValidateToken(rawToken string) (*JWTClaims, error) {
	if len(v.secret) == 0 {
		return nil, ErrInternalServiceSecretUnset
	}

	parsed, err := jwt.Parse(rawToken, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: unexpected signing method %v", ErrInvalidInternalServiceToken, t.Header["alg"])
		}
		return v.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInternalServiceToken, err)
	}

	mapClaims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return nil, ErrInvalidInternalServiceToken
	}

	sub, _ := mapClaims["sub"].(string)
	role, _ := mapClaims["role"].(string)
	if sub != InternalServiceSubject {
		return nil, fmt.Errorf("%w: unexpected sub %q", ErrInvalidInternalServiceToken, sub)
	}
	if role != InternalServiceRole {
		return nil, fmt.Errorf("%w: unexpected role %q", ErrInvalidInternalServiceToken, role)
	}

	email, _ := mapClaims["email"].(string)
	return &JWTClaims{
		Sub:   sub,
		Email: email,
		Role:  role,
	}, nil
}
