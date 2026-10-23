package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenValidator validates a single bearer JWT and returns parsed [Claims].
//
// Implementations are expected to be safe for concurrent use. Each Validator
// represents one issuer/key configuration (e.g. one HMAC secret, one JWKS
// endpoint); compose multiple via [MultiValidator] when a service needs to
// accept tokens from more than one issuer (typical during an auth-provider
// migration — Supabase HMAC tokens alongside Auth0 JWKS tokens).
type TokenValidator interface {
	// ValidateToken parses tokenString and returns the parsed Claims. The
	// returned error must be non-nil iff the token is invalid for THIS
	// validator. MultiValidator uses error to decide whether to try the
	// next validator in the chain.
	ValidateToken(tokenString string) (*Claims, error)
}

// HMACValidator validates JWTs signed with an HMAC algorithm (HS256/384/512).
//
// Zero value is not usable; construct with [NewHMACValidator]. The secret is
// captured at construction; rotating it means constructing a new validator
// (which is cheap — no I/O).
type HMACValidator struct {
	signingMethod string
	secret        []byte
	issuer        string
	audience      string
	resolver      UserResolver
	rolesClaim    string
}

// HMACValidatorConfig configures an [HMACValidator].
type HMACValidatorConfig struct {
	// SigningMethod is the expected JWT alg (e.g. "HS256"). Required.
	SigningMethod string

	// Secret is the symmetric secret. Required — this package reads no
	// environment variable of its own, so the key a validator trusts is
	// always the key its constructor was handed. Pass it from the app's
	// typed config (declare the field `sensitive: true` and it arrives from
	// the environment's secret provider, never as an inline manifest
	// value); an app accepting several issuers passes each their own.
	Secret string

	// Issuer, when set, is enforced via jwt.WithIssuer.
	Issuer string

	// Audience, when set, is enforced via jwt.WithAudience.
	Audience string

	// Resolver, when non-nil, is given the raw JWT payload so projects can
	// project provider-specific shapes onto [Claims] (and into Claims.Raw).
	// Nil means use the built-in shape extraction.
	Resolver UserResolver

	// RolesClaim names the claim roles are read from; see [JWTConfig.RolesClaim].
	RolesClaim string
}

// NewHMACValidator constructs a validator for HMAC-signed JWTs.
//
// Returns an error when Secret is empty or SigningMethod is not an HS*
// algorithm.
func NewHMACValidator(cfg HMACValidatorConfig) (*HMACValidator, error) {
	if cfg.SigningMethod == "" {
		cfg.SigningMethod = "HS256"
	}
	if !strings.HasPrefix(cfg.SigningMethod, "HS") {
		return nil, fmt.Errorf("auth: HMAC validator requires HS* signing method, got %q", cfg.SigningMethod)
	}
	secret := cfg.Secret
	if secret == "" {
		return nil, fmt.Errorf("auth: HMAC validator: Secret is empty — pass the signing key in, from the app's typed config")
	}
	return &HMACValidator{
		signingMethod: cfg.SigningMethod,
		secret:        []byte(secret),
		issuer:        cfg.Issuer,
		audience:      cfg.Audience,
		resolver:      cfg.Resolver,
		rolesClaim:    cfg.RolesClaim,
	}, nil
}

// ValidateToken implements [TokenValidator].
func (v *HMACValidator) ValidateToken(tokenString string) (*Claims, error) {
	return validateJWTWith(tokenString, v.signingMethod, func(*jwt.Token) (interface{}, error) {
		return v.secret, nil
	}, v.issuer, v.audience, v.resolver, v.rolesClaim)
}

// JWKSValidator validates JWTs whose signing key comes from a JWKS endpoint
// (or any other [jwt.Keyfunc] source — the pkg/auth library deliberately does
// not depend on a JWKS client implementation; the caller supplies one).
//
// Construct via [NewJWKSValidator] with a pre-built [jwt.Keyfunc] (e.g. one
// returned by github.com/MicahParks/keyfunc/v3.NewDefaultCtx).
type JWKSValidator struct {
	signingMethod string
	keyFunc       jwt.Keyfunc
	issuer        string
	audience      string
	resolver      UserResolver
	rolesClaim    string
}

// JWKSValidatorConfig configures a [JWKSValidator].
type JWKSValidatorConfig struct {
	// SigningMethod is the expected JWT alg. Defaults to "RS256".
	SigningMethod string

	// KeyFunc resolves the signing key for the token. Required.
	KeyFunc jwt.Keyfunc

	// Issuer, when set, is enforced via jwt.WithIssuer.
	Issuer string

	// Audience, when set, is enforced via jwt.WithAudience.
	Audience string

	// Resolver, when non-nil, projects the raw JWT payload onto [Claims].
	Resolver UserResolver

	// RolesClaim names the claim roles are read from; see [JWTConfig.RolesClaim].
	RolesClaim string
}

// NewJWKSValidator constructs a validator that delegates key resolution to
// cfg.KeyFunc (typically a JWKS-backed implementation).
func NewJWKSValidator(cfg JWKSValidatorConfig) (*JWKSValidator, error) {
	if cfg.KeyFunc == nil {
		return nil, fmt.Errorf("auth: JWKS validator requires a non-nil KeyFunc")
	}
	if cfg.SigningMethod == "" {
		cfg.SigningMethod = "RS256"
	}
	return &JWKSValidator{
		signingMethod: cfg.SigningMethod,
		keyFunc:       cfg.KeyFunc,
		issuer:        cfg.Issuer,
		audience:      cfg.Audience,
		resolver:      cfg.Resolver,
		rolesClaim:    cfg.RolesClaim,
	}, nil
}

// ValidateToken implements [TokenValidator].
func (v *JWKSValidator) ValidateToken(tokenString string) (*Claims, error) {
	return validateJWTWith(tokenString, v.signingMethod, v.keyFunc, v.issuer, v.audience, v.resolver, v.rolesClaim)
}

// MultiValidator tries each underlying validator in order and returns the
// first set of claims that parses cleanly.
//
// When every validator rejects the token, MultiValidator returns the LAST
// validator's error (the chain typically ends with the strictest validator,
// so its error is the most informative). Use [NewMultiValidator] to construct.
type MultiValidator struct {
	validators []TokenValidator
}

// NewMultiValidator constructs an ordered fallback chain. At least one
// underlying validator is required.
func NewMultiValidator(validators ...TokenValidator) (*MultiValidator, error) {
	if len(validators) == 0 {
		return nil, fmt.Errorf("auth: MultiValidator requires at least one underlying validator")
	}
	for i, v := range validators {
		if v == nil {
			return nil, fmt.Errorf("auth: MultiValidator: validator at index %d is nil", i)
		}
	}
	return &MultiValidator{validators: validators}, nil
}

// ValidateToken implements [TokenValidator] by trying each underlying
// validator in order.
func (m *MultiValidator) ValidateToken(tokenString string) (*Claims, error) {
	var lastErr error
	for _, v := range m.validators {
		claims, err := v.ValidateToken(tokenString)
		if err == nil {
			return claims, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("auth: no validators configured")
	}
	return nil, lastErr
}

// validateJWTWith is the shared parse-and-extract path used by every
// TokenValidator implementation. Keeping it in one place ensures parser
// options (leeway, issuer, audience), claims extraction, and UserResolver
// dispatch stay consistent across validators.
func validateJWTWith(tokenString, signingMethod string, keyFunc jwt.Keyfunc, issuer, audience string, resolver UserResolver, rolesClaim string) (*Claims, error) {
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{signingMethod}),
		jwt.WithLeeway(5 * time.Second),
	}
	if issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(issuer))
	}
	if audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(audience))
	}

	token, err := jwt.Parse(tokenString, keyFunc, parserOpts...)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("unexpected claims type")
	}

	return projectClaims(mapClaims, resolver, claimOpts{rolesClaim: rolesClaim, audience: audience}), nil
}
