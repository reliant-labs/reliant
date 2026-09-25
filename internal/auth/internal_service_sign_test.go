package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// TestSignInternalServiceToken_CarriesTheControlPlaneClaimSet pins the exact
// claims control-plane's validator requires (sub, role, HS256, a future exp).
func TestSignInternalServiceToken_CarriesTheControlPlaneClaimSet(t *testing.T) {
	tok, err := SignInternalServiceToken("shared-secret")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte("shared-secret"), nil },
		jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("signed token did not verify: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["sub"] != InternalServiceSubject || claims["role"] != InternalServiceRole {
		t.Fatalf("claims %v", claims)
	}
	if _, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return []byte("other"), nil }); err == nil {
		t.Fatal("a token verified under the wrong secret")
	}
	if _, err := SignInternalServiceToken(""); err == nil {
		t.Fatal("signed with an empty secret")
	}
}
