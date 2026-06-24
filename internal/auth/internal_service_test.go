// Copyright (c) 2025 Reliant Labs
package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signControlPlaneToken mirrors control-plane's SignInternalServiceToken
// (control-plane/internal/auth/internal_service.go) so the round-trip contract
// is exercised here. If control-plane's signer changes, this test must change
// in lockstep — that is the point: it is the cross-repo contract gate.
func signControlPlaneToken(t *testing.T, secret string, overrides map[string]interface{}) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":  InternalServiceSubject,
		"role": InternalServiceRole,
		"iss":  "control-plane",
		"aud":  "authenticated",
		"iat":  now.Unix(),
		"exp":  now.Add(5 * time.Minute).Unix(),
	}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("signing token: %v", err)
	}
	return signed
}

func TestInternalServiceVerifier_RoundTrip(t *testing.T) {
	const secret = "internal-service-secret-with-at-least-32-bytes-of-entropy"
	v := NewInternalServiceVerifier(secret)

	claims, err := v.ValidateToken(signControlPlaneToken(t, secret, nil))
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Sub != InternalServiceSubject {
		t.Errorf("sub = %q, want %q", claims.Sub, InternalServiceSubject)
	}
	if claims.Role != InternalServiceRole {
		t.Errorf("role = %q, want %q", claims.Role, InternalServiceRole)
	}
}

func TestInternalServiceVerifier_FailsClosedWithoutSecret(t *testing.T) {
	v := NewInternalServiceVerifier("")
	if v.Enabled() {
		t.Fatal("Enabled() = true for empty secret")
	}
	// Even a structurally valid token is rejected when no secret is configured.
	tok := signControlPlaneToken(t, "some-secret", nil)
	_, err := v.ValidateToken(tok)
	if !errors.Is(err, ErrInternalServiceSecretUnset) {
		t.Errorf("err = %v, want ErrInternalServiceSecretUnset", err)
	}
}

func TestInternalServiceVerifier_RejectsWrongSecret(t *testing.T) {
	v := NewInternalServiceVerifier("correct-secret-correct-secret-correct")
	tok := signControlPlaneToken(t, "WRONG-secret-WRONG-secret-WRONG-secret", nil)
	if _, err := v.ValidateToken(tok); err == nil {
		t.Error("ValidateToken with wrong-secret token = nil error, want error")
	}
}

func TestInternalServiceVerifier_RejectsExpired(t *testing.T) {
	const secret = "internal-service-secret-with-at-least-32-bytes-of-entropy"
	v := NewInternalServiceVerifier(secret)
	expired := signControlPlaneToken(t, secret, map[string]interface{}{
		"iat": time.Now().Add(-1 * time.Hour).Unix(),
		"exp": time.Now().Add(-30 * time.Minute).Unix(),
	})
	if _, err := v.ValidateToken(expired); err == nil {
		t.Error("ValidateToken with expired token = nil error, want error")
	}
}

func TestInternalServiceVerifier_RejectsWrongSubAndRole(t *testing.T) {
	const secret = "internal-service-secret-with-at-least-32-bytes-of-entropy"
	v := NewInternalServiceVerifier(secret)

	// A token signed with the SAME secret but carrying a user identity (the
	// shape a leaked / mis-issued token might have) must still be rejected —
	// only sub=internal-service / role=admin is accepted.
	wrongSub := signControlPlaneToken(t, secret, map[string]interface{}{"sub": "real-user-uuid"})
	if _, err := v.ValidateToken(wrongSub); err == nil {
		t.Error("ValidateToken with wrong sub = nil error, want error")
	}
	wrongRole := signControlPlaneToken(t, secret, map[string]interface{}{"role": "authenticated"})
	if _, err := v.ValidateToken(wrongRole); err == nil {
		t.Error("ValidateToken with wrong role = nil error, want error")
	}
}

func TestInternalServiceVerifier_RejectsNoneAlg(t *testing.T) {
	const secret = "internal-service-secret-with-at-least-32-bytes-of-entropy"
	v := NewInternalServiceVerifier(secret)

	// alg=none must never be accepted, regardless of claims.
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub":  InternalServiceSubject,
		"role": InternalServiceRole,
		"exp":  time.Now().Add(5 * time.Minute).Unix(),
	})
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("signing none token: %v", err)
	}
	if _, err := v.ValidateToken(signed); err == nil {
		t.Error("ValidateToken with alg=none = nil error, want error")
	}
}
