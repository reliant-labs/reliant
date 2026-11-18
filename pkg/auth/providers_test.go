package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Clerk and Firebase pin RS256, so these tests sign with a throwaway RSA key
// and inject its public key via the KeyFunc override (standing in for the
// JWKS-fetched key) — no network, no keyfunc HTTP fetch.

func rsaKeyFor(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return k
}

func signRS256(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign RS256: %v", err)
	}
	return s
}

func pubKeyFunc(key *rsa.PrivateKey) jwt.Keyfunc {
	return func(*jwt.Token) (interface{}, error) { return &key.PublicKey, nil }
}

func TestClerk_MapsSessionClaims(t *testing.T) {
	key := rsaKeyFor(t)
	v, err := Clerk(ClerkOpts{KeyFunc: pubKeyFunc(key)})
	if err != nil {
		t.Fatalf("Clerk: %v", err)
	}
	tok := signRS256(t, key, jwt.MapClaims{
		"sub":             "user_2abc",
		"org_id":          "org_9xyz",
		"org_role":        "org:admin",
		"org_permissions": []any{"org:billing:read", "org:members:manage"},
		"exp":             time.Now().Add(time.Hour).Unix(),
	})
	c, err := v.Validate(tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.UserID != "user_2abc" {
		t.Errorf("UserID = %q, want user_2abc", c.UserID)
	}
	if c.OrgID != "org_9xyz" {
		t.Errorf("OrgID = %q, want org_9xyz", c.OrgID)
	}
	if c.Role != "org:admin" {
		t.Errorf("Role = %q, want org:admin", c.Role)
	}
	// org_permissions plus the appended org_role.
	wantRoles := map[string]bool{"org:billing:read": true, "org:members:manage": true, "org:admin": true}
	if len(c.Roles) != len(wantRoles) {
		t.Fatalf("Roles = %v, want the 2 permissions + org_role", c.Roles)
	}
	for _, r := range c.Roles {
		if !wantRoles[r] {
			t.Errorf("unexpected role %q in %v", r, c.Roles)
		}
	}
	if c.Raw["org_id"] != "org_9xyz" {
		t.Errorf("Raw not populated: %v", c.Raw)
	}
}

func TestClerk_RequiresJWKSURL(t *testing.T) {
	if _, err := Clerk(ClerkOpts{}); err == nil {
		t.Error("expected error when neither JWKSURL nor KeyFunc is set")
	}
}

func TestClerk_EnforcesIssuer(t *testing.T) {
	key := rsaKeyFor(t)
	v, err := Clerk(ClerkOpts{KeyFunc: pubKeyFunc(key), Issuer: "https://good.clerk.accounts.dev"})
	if err != nil {
		t.Fatalf("Clerk: %v", err)
	}
	tok := signRS256(t, key, jwt.MapClaims{
		"sub": "user_1",
		"iss": "https://evil.clerk.accounts.dev",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Validate(tok); err == nil {
		t.Error("expected token with wrong issuer to be rejected")
	}
}

func TestFirebase_MultiProject(t *testing.T) {
	key := rsaKeyFor(t)
	v, err := Firebase(FirebaseOpts{
		ProjectIDs: []string{"proj-a", "proj-b"},
		KeyFunc:    pubKeyFunc(key),
	})
	if err != nil {
		t.Fatalf("Firebase: %v", err)
	}

	// A valid token for the second accepted project.
	good := signRS256(t, key, jwt.MapClaims{
		"sub":   "firebase-uid-1",
		"email": "u@example.com",
		"aud":   "proj-b",
		"iss":   firebaseIssuerPrefix + "proj-b",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	c, err := v.Validate(good)
	if err != nil {
		t.Fatalf("Validate(good): %v", err)
	}
	if c.UserID != "firebase-uid-1" {
		t.Errorf("UserID = %q, want firebase-uid-1", c.UserID)
	}
	if c.Email != "u@example.com" {
		t.Errorf("Email = %q, want u@example.com", c.Email)
	}

	// aud is a project we don't accept → rejected.
	badAud := signRS256(t, key, jwt.MapClaims{
		"sub": "x",
		"aud": "proj-c",
		"iss": firebaseIssuerPrefix + "proj-c",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Validate(badAud); err == nil {
		t.Error("expected token for unaccepted project to be rejected")
	}

	// aud accepted but iss doesn't match that project → rejected.
	badIss := signRS256(t, key, jwt.MapClaims{
		"sub": "x",
		"aud": "proj-a",
		"iss": firebaseIssuerPrefix + "proj-b",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Validate(badIss); err == nil {
		t.Error("expected token with mismatched issuer/audience to be rejected")
	}
}

func TestFirebase_RequiresProjectID(t *testing.T) {
	if _, err := Firebase(FirebaseOpts{}); err == nil {
		t.Error("expected error when no project IDs configured")
	}
	if _, err := Firebase(FirebaseOpts{ProjectIDs: []string{"", "  "}}); err == nil {
		t.Error("expected error when project IDs are all empty/whitespace")
	}
}

func TestFirebase_UIDFallback(t *testing.T) {
	key := rsaKeyFor(t)
	v, err := Firebase(FirebaseOpts{ProjectIDs: []string{"proj-a"}, KeyFunc: pubKeyFunc(key)})
	if err != nil {
		t.Fatalf("Firebase: %v", err)
	}
	tok := signRS256(t, key, jwt.MapClaims{
		"user_id": "legacy-uid",
		"aud":     "proj-a",
		"iss":     firebaseIssuerPrefix + "proj-a",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	c, err := v.Validate(tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.UserID != "legacy-uid" {
		t.Errorf("UserID = %q, want legacy-uid (uid fallback)", c.UserID)
	}
}

func TestJWKSKeyfunc_RequiresURL(t *testing.T) {
	if _, err := JWKSKeyfunc(); err == nil {
		t.Error("expected error when no URLs supplied")
	}
}
