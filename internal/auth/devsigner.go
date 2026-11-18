// Copyright (c) 2025 Reliant Labs
package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/reliant-labs/reliant/internal/runenv"
)

// ErrDevSignerOutsideDev refuses a dev signer (AUTH_DEV_JWT_SECRET) in a
// process that is not in the dev tier. It is a configuration error, not a
// credential rejection: the process must not start with it.
var ErrDevSignerOutsideDev = errors.New("AUTH_DEV_JWT_SECRET is a dev-only signer and is refused outside the dev tier (RELIANT_ENV=dev)")

// CheckDevSigner reports whether s may carry a dev signer in this process: no
// error when none is configured, ErrDevSignerOutsideDev when one is and the
// process is not in the dev tier. The api-server calls it as its settings
// load, so a misconfigured process refuses to start rather than serving.
func CheckDevSigner(s Settings) error {
	if s.DevJWTSecret != "" && !runenv.IsDev() {
		return ErrDevSignerOutsideDev
	}
	return nil
}

// WithDevSigner returns session, extended to accept HS256 tokens signed with
// s.DevJWTSecret: the tokens control-plane's `auth mint-dev-token` mints for
// its dev stack, so a browser can be signed in there without a real Supabase
// login (control-plane#2735).
//
// The dev signer sits BESIDE the session validator, never instead of it: a
// token's header alg picks the verifier, so HS256 goes to the secret and
// everything else to session, exactly as before. The session validator stays
// ES256-only, so a real token can never be checked against the shared dev
// secret, and an HS256 token never reaches it.
//
// With no secret configured it returns session unchanged. With one configured
// outside the dev tier it fails (ErrDevSignerOutsideDev): there is no
// runtime switch that turns this on in prod.
func WithDevSigner(session TokenValidator, s Settings) (TokenValidator, error) {
	if s.DevJWTSecret == "" {
		return session, nil
	}
	if err := CheckDevSigner(s); err != nil {
		return nil, err
	}
	return &devSignerDispatch{
		session: session,
		dev: &devSignerValidator{
			secret:                    []byte(s.DevJWTSecret),
			requireEmailVerifiedClaim: s.RequireEmailVerifiedClaim,
		},
	}, nil
}

// devSignerDispatch sends HS256 tokens to the dev signer and every other token
// to the session validator.
type devSignerDispatch struct {
	session TokenValidator
	dev     *devSignerValidator
}

func (d *devSignerDispatch) ValidateToken(token string) (*JWTClaims, error) {
	if tokenAlg(token) == jwt.SigningMethodHS256.Alg() {
		return d.dev.ValidateToken(token)
	}
	return d.session.ValidateToken(token)
}

// tokenAlg is the alg a JWT's header names, or "" when it has none or is not
// a JWT. It verifies nothing: it only picks which validator verifies.
func tokenAlg(token string) string {
	header, _, ok := strings.Cut(token, ".")
	if !ok {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return ""
	}
	var h JWTHeader
	if json.Unmarshal(raw, &h) != nil {
		return ""
	}
	return h.Alg
}

// devSignerValidator verifies HS256 tokens signed with the dev secret and maps
// them to the claims a Supabase session token yields.
type devSignerValidator struct {
	secret                    []byte
	requireEmailVerifiedClaim bool
}

func (v *devSignerValidator) ValidateToken(token string) (*JWTClaims, error) {
	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return v.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return nil, ErrExpiredToken
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return nil, ErrInvalidSignature
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	raw, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected claims", ErrInvalidToken)
	}
	// The same claims, decoded the way the session validator decodes them, so
	// a dev token and a Supabase one mean the same thing downstream.
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to parse claims", ErrInvalidToken)
	}
	var claims JWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("%w: failed to parse claims", ErrInvalidToken)
	}
	if claims.Sub == "" {
		return nil, fmt.Errorf("%w: no sub claim", ErrInvalidToken)
	}
	claims.Email = verifiedEmailClaim(claims.Email, raw, v.requireEmailVerifiedClaim)
	claims.Credential = CredentialSession
	if claims.ClientID != "" {
		claims.Credential = CredentialOAuthClient
	}
	return &claims, nil
}
