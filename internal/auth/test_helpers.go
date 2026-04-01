// Copyright (c) 2025 Reliant Labs
package auth

// NewTestMiddleware creates a test auth middleware that accepts any token as valid.
// The token value itself is used as the user ID, allowing tests to control user identity.
func NewTestMiddleware() *Middleware {
	return &Middleware{
		validator:        &testJWTValidator{},
		firstSeenTracker: newFirstSeenTracker(),
	}
}

// testJWTValidator is a mock validator that accepts any token
type testJWTValidator struct{}

// ValidateToken accepts any token and uses it as the user ID
func (v *testJWTValidator) ValidateToken(token string) (*JWTClaims, error) {
	// Use the token itself as the user ID
	// This allows tests to control user identity by setting the Bearer token
	userID := token
	if userID == "" {
		userID = "test-user-default"
	}

	return &JWTClaims{
		Sub:   userID,
		Email: userID + "@test.com",
		Role:  "authenticated",
		Exp:   9999999999, // Far future expiration
		Iat:   0,
	}, nil
}
