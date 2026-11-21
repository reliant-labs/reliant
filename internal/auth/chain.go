// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	forgeauth "github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

// Source names the validator that proved a credential. It is half of what
// decides the request's Credential (credentialFor) and a label of
// reliant_auth_credentials_total, which is how the Supabase traffic is seen
// falling to zero before the Supabase entry is removed (design-auth-swap §3).
type Source string

const (
	// SourceSupabase is a Supabase JWT, verified by JWTValidator.
	SourceSupabase Source = "supabase"
	// SourceDevSigner is a dev tier HS256 token (WithDevSigner).
	SourceDevSigner Source = "dev_signer"
	// SourceIdentity is a token of the managed identity (forge.ManagedIdentity,
	// a Zitadel instance), verified by forge's JWT validator.
	SourceIdentity Source = "identity"
	// SourceSharedKey is the self-host shared bearer key (AUTH_MODE=apikey).
	SourceSharedKey Source = "shared_key"
	// SourceAccessToken is an `rlat_` access token, resolved by the token
	// authority. It never reaches the TokenChain.
	SourceAccessToken Source = "access_token"
)

// identityDiscoveryTimeout bounds one OpenID configuration lookup of the
// identity's issuer (its JWKS URL, its userinfo endpoint).
const identityDiscoveryTimeout = 10 * time.Second

// TokenChain is the user audience's token validators, as forge's
// token-validator chain composes them (forge/pkg/auth TokenValidator), with
// one difference that is the point of it: a token is checked by EXACTLY ONE
// validator, never tried against each in turn.
//
//   - A token whose (unverified) iss is the managed identity's issuer goes to
//     the identity validator alone: forge's JWT validator over the issuer's
//     JWKS, enforcing alg, iss, aud and exp.
//   - Every other token goes to the legacy validator alone: Supabase's
//     JWTValidator (ES256, iss and aud when configured) with the dev signer
//     beside it, or, in AUTH_MODE=apikey, the shared key.
//
// forge's MultiValidator tries each validator and keeps the first success.
// Routing by issuer instead means a token cannot shop for the validator that
// accepts it (the rule JWTValidator already applies to a named kid), the
// error a caller gets is the one validator that judged it, and the validator
// that proved it is known (Source) — which decides whether it is a person's
// session or software's token.
type TokenChain struct {
	identityIssuer string
	// identity is nil when no managed identity is configured: the chain is
	// then exactly the legacy validator, as before.
	identity forgeauth.TokenValidator
	// legacy is nil when only the identity is configured.
	legacy    TokenValidator
	sharedKey bool
}

// NewTokenChain builds the chain s describes. It fetches each JWKS it is
// pointed at, so an unreachable issuer is an error here (forge's rule: a
// validator that cannot verify anything refuses to start, unless a fresh
// snapshot of the identity's keys was delivered).
func NewTokenChain(ctx context.Context, s Settings) (*TokenChain, error) {
	chain := &TokenChain{}
	if s.ModeName() == ModeAPIKey {
		// The shared key, and the managed identity beside it when one is
		// configured (the dev stack signs in with both). A key is never a
		// JWT naming the identity's issuer, so the two cannot be confused.
		if s.APIKey == "" {
			return nil, fmt.Errorf("AUTH_MODE=apikey requires AUTH_API_KEY to be set")
		}
		key, err := NewAPIKeyValidator(s.APIKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create API key validator: %w", err)
		}
		chain.legacy, chain.sharedKey = key, true
		if err := chain.addIdentity(s.Identity); err != nil {
			return nil, err
		}
		return chain, nil
	}

	var supabase TokenValidator
	switch {
	case s.PublicKeyPEM != "":
		v, err := NewJWTValidator(s.PublicKeyPEM, s.JWTOptions()...)
		if err != nil {
			return nil, err
		}
		supabase = v
	case s.JWKSURL != "":
		v, err := LoadJWKS(ctx, s.JWKSURL, s.JWTOptions()...)
		if err != nil {
			return nil, fmt.Errorf("failed to load JWKS from %s: %w", s.JWKSURL, err)
		}
		supabase = v
	}
	if supabase == nil && s.DevJWTSecret != "" {
		// The dev signer sits beside a session validator; with no Supabase
		// key, beside one that verifies nothing.
		supabase = refuseAll{}
	}
	if supabase != nil {
		legacy, err := WithDevSigner(supabase, s)
		if err != nil {
			return nil, err
		}
		chain.legacy = legacy
	}

	if err := chain.addIdentity(s.Identity); err != nil {
		return nil, err
	}

	if chain.legacy == nil && chain.identity == nil {
		return nil, ErrInvalidPublicKey
	}
	return chain, nil
}

// addIdentity adds the managed identity's validator when s names one.
func (c *TokenChain) addIdentity(s IdentitySettings) error {
	if !s.Configured() {
		return nil
	}
	identity, err := newIdentityValidator(s)
	if err != nil {
		return err
	}
	c.identity, c.identityIssuer = identity, s.Issuer
	return nil
}

// newIdentityValidator is forge's JWT validator for the managed identity:
// the same construction forge scaffolds into a project's SetupAuth.
func newIdentityValidator(s IdentitySettings) (forgeauth.TokenValidator, error) {
	if s.Audience == "" {
		// Without an audience every token any project of the same Zitadel
		// instance is issued would pass, and the roles of all of them would
		// merge.
		return nil, errors.New("JWT_ISSUER needs JWT_AUDIENCE: the identity project's id, so a token issued for another project of the same instance is refused")
	}
	var discover func(ctx context.Context) (string, error)
	if s.JWKSURL == "" {
		discover = func(ctx context.Context) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, identityDiscoveryTimeout)
			defer cancel()
			meta, err := oauth2.Discover(ctx, nil, s.Issuer)
			if err != nil {
				return "", fmt.Errorf("discover OIDC issuer %q: %w", s.Issuer, err)
			}
			return meta.JWKSURI, nil
		}
	}
	snapshot := s.Snapshot
	snapshot.Issuer = s.Issuer
	v, err := forgeauth.NewValidator(forgeauth.Config{
		Provider: forgeauth.ProviderJWT,
		JWT: forgeauth.JWTConfig{
			SigningMethod: s.SigningMethod,
			Issuer:        s.Issuer,
			Audience:      s.Audience,
			JWKSURL:       s.JWKSURL,
			JWKSDiscover:  discover,
			JWKSSnapshot:  snapshot,
			RolesClaim:    s.RolesClaim,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("managed identity validator (%s): %w", s.Issuer, err)
	}
	return validatorFunc(v.Validate), nil
}

// validatorFunc adapts forge's (*Validator).Validate to its TokenValidator.
type validatorFunc func(token string) (*forgeauth.Claims, error)

func (f validatorFunc) ValidateToken(token string) (*forgeauth.Claims, error) { return f(token) }

// refuseAll is a session validator that verifies nothing.
type refuseAll struct{}

func (refuseAll) ValidateToken(string) (*JWTClaims, error) {
	return nil, fmt.Errorf("%w: no session key is configured", ErrInvalidToken)
}

// IdentityIssuer is the managed identity's issuer this chain accepts tokens
// from, or "" when it accepts none.
func (c *TokenChain) IdentityIssuer() string { return c.identityIssuer }

// ValidateToken implements forge's TokenValidator. The claims it returns
// carry the chain's verdict (verdictOf), which no token can forge. Every
// error is a credential rejection (IsCredentialRejection): the keys were
// fetched at construction, so verification ran.
func (c *TokenChain) ValidateToken(token string) (*forgeauth.Claims, error) {
	if c.identity != nil && unverifiedIssuer(token) == c.identityIssuer {
		claims, err := c.identity.ValidateToken(token)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
		}
		if claims == nil || claims.UserID == "" {
			return nil, fmt.Errorf("%w: the identity's token names no subject", ErrInvalidToken)
		}
		return withVerdict(claims, verdict{source: SourceIdentity}), nil
	}
	if c.legacy == nil {
		return nil, fmt.Errorf("%w: not a token of the managed identity", ErrInvalidToken)
	}
	jc, err := c.legacy.ValidateToken(token)
	if err != nil {
		return nil, err
	}
	source := SourceSupabase
	switch {
	case c.sharedKey:
		source = SourceSharedKey
	case tokenAlg(token) == devSignerAlg:
		source = SourceDevSigner
	}
	claims := &forgeauth.Claims{
		UserID: jc.Sub,
		Email:  jc.Email,
		Role:   jc.Role,
		Raw:    map[string]any{"iss": jc.Iss, "client_id": jc.ClientID},
	}
	return withVerdict(claims, verdict{source: source, legacy: jc}), nil
}

// devSignerAlg is the alg devSignerDispatch sends to the dev signer.
const devSignerAlg = "HS256"

// verdict is what the chain decided about a token it accepted. It rides the
// forge claims (forge's Authenticator hands back the claims its token
// validator returned) under verdictKey, as a value of this unexported type:
// a token's payload decodes to strings, numbers, maps and slices, never to a
// verdict, so a claim of the same name in a token is not one.
type verdict struct {
	source Source
	// legacy is the legacy validator's claims (Supabase, the dev signer, the
	// shared key), whose Credential it decided.
	legacy *JWTClaims
}

const verdictKey = "\x00reliant.verdict"

func withVerdict(claims *forgeauth.Claims, v verdict) *forgeauth.Claims {
	out := *claims
	raw := make(map[string]any, len(claims.Raw)+1)
	for k, val := range claims.Raw {
		raw[k] = val
	}
	raw[verdictKey] = v
	out.Raw = raw
	return &out
}

// verdictOf is the chain's verdict on claims, and false for claims the chain
// did not return.
func verdictOf(claims *forgeauth.Claims) (verdict, bool) {
	if claims == nil {
		return verdict{}, false
	}
	v, ok := claims.Raw[verdictKey].(verdict)
	return v, ok
}

// unverifiedIssuer is the iss a JWT's payload names, or "" when it names
// none or is not a JWT. It verifies nothing: it only picks which validator
// verifies.
func unverifiedIssuer(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Iss
}
