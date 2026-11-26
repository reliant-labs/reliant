package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const requireExpirySecret = "require-expiry-secret-at-least-32-bytes!"

// The times every token here carries: no assertion depends on today, except
// the leeway case, which is relative to now by what it tests.
var (
	expiryFuture = time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	expiryPast   = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
)

// TestRequireExpiry: with RequireExpiry a token must carry an exp claim, and
// a numeric one that has not passed (beyond the 5s leeway every validator
// allows). Without it — the v1 default — a token with no exp is accepted, as
// it always was, and an exp that is present is still enforced.
func TestRequireExpiry(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	type validator struct {
		sign     func(jwt.MapClaims) string
		validate func(require bool, token string) error
	}
	hmacSign := func(c jwt.MapClaims) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(requireExpirySecret))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	rsaSign := func(c jwt.MapClaims) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(rsaKey)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	validators := map[string]validator{
		"Validator (JWT config)": {sign: hmacSign, validate: func(req bool, tok string) error {
			v, err := NewValidator(Config{Provider: ProviderJWT, JWT: JWTConfig{SigningMethod: "HS256", Secret: requireExpirySecret, RequireExpiry: req}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Validate(tok)
			return err
		}},
		"Validator (TokenValidators chain)": {sign: rsaSign, validate: func(req bool, tok string) error {
			jv, err := NewJWKSValidator(JWKSValidatorConfig{KeyFunc: func(*jwt.Token) (any, error) { return &rsaKey.PublicKey, nil }, RequireExpiry: req})
			if err != nil {
				t.Fatal(err)
			}
			v, err := NewValidator(Config{Provider: ProviderJWT, TokenValidators: []TokenValidator{jv}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Validate(tok)
			return err
		}},
		"HMACValidator": {sign: hmacSign, validate: func(req bool, tok string) error {
			v, err := NewHMACValidator(HMACValidatorConfig{SigningMethod: "HS256", Secret: requireExpirySecret, RequireExpiry: req})
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.ValidateToken(tok)
			return err
		}},
		"JWKSValidator": {sign: rsaSign, validate: func(req bool, tok string) error {
			v, err := NewJWKSValidator(JWKSValidatorConfig{KeyFunc: func(*jwt.Token) (any, error) { return &rsaKey.PublicKey, nil }, RequireExpiry: req})
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.ValidateToken(tok)
			return err
		}},
	}
	for name, v := range validators {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				claims   jwt.MapClaims
				required bool // accepted with RequireExpiry
				legacy   bool // accepted without it
			}{
				{name: "a future exp", claims: jwt.MapClaims{"sub": "u", "exp": expiryFuture}, required: true, legacy: true},
				{name: "no exp", claims: jwt.MapClaims{"sub": "u"}, required: false, legacy: true},
				// exp 0 decodes as no expiry (golang-jwt): refused only when required.
				{name: "exp 0", claims: jwt.MapClaims{"sub": "u", "exp": 0}, required: false, legacy: true},
				{name: "a past exp", claims: jwt.MapClaims{"sub": "u", "exp": expiryPast}, required: false, legacy: false},
				{name: "a non-numeric exp", claims: jwt.MapClaims{"sub": "u", "exp": "2100-01-01"}, required: false, legacy: false},
				// The bounded leeway: 2s past is inside it, 60s past is not.
				{name: "exp 2s ago (inside the leeway)", claims: jwt.MapClaims{"sub": "u", "exp": time.Now().Add(-2 * time.Second).Unix()}, required: true, legacy: true},
				{name: "exp 60s ago (past the leeway)", claims: jwt.MapClaims{"sub": "u", "exp": time.Now().Add(-time.Minute).Unix()}, required: false, legacy: false},
			} {
				tok := v.sign(tc.claims)
				if err := v.validate(true, tok); (err == nil) != tc.required {
					t.Errorf("%s, RequireExpiry: err = %v, want accepted=%v", tc.name, err, tc.required)
				}
				if err := v.validate(false, tok); (err == nil) != tc.legacy {
					t.Errorf("%s, default: err = %v, want accepted=%v", tc.name, err, tc.legacy)
				}
			}
		})
	}
}
